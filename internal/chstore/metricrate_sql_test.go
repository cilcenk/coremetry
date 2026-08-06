package chstore

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// v0.9.685 — RATE SQL'İNDE YER TUTUCU SIRASI.
//
// v0.9.668'de değer kolonunu parametrik yaptım (sayaçta `value`,
// histogramda `count`) ve fmt.Sprintf argümanlarını YANLIŞ SIRAYA
// koydum. Şablon sk → gk → değer beklerken argümanlar değer → sk → gk
// gidiyordu:
//
//	count AS sk,                              ← UInt64
//	toString(cityHash64(...)) AS gk,
//	argMaxOrNull([]::Array(String), time) AS v
//
// Sonuç: `clickhouse [ScanRow]: (sk) converting UInt64 to *string`.
// HER İKİ YOL da kırıktı, ON ALTI SÜRÜM boyunca fark edilmedi — çünkü
// uç bir kez bile çalıştırılmadı. Operatörün tanılama kutusu (v0.9.683)
// yüzeye çıkarana kadar sessizdi.
//
// Aynı tipte dört `%s` taşıyan bir şablonda konumsal argüman,
// derleyicinin yakalayamadığı bir hata sınıfı. Bu testin tek işi
// sırayı çivilemek.

// colExpr — `<ifade> AS <alias>` satırından ifadeyi çeker.
func colExpr(t *testing.T, sql, alias string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^\s*(.+?)\s+AS ` + regexp.QuoteMeta(alias) + `,?\s*$`)
	m := re.FindStringSubmatch(sql)
	if m == nil {
		t.Fatalf("%q kolonu bulunamadı:\n%s", alias, sql)
	}
	return m[1]
}

func TestBuildRateCumulativeSQLPlaceholderOrder(t *testing.T) {
	sql := buildRateCumulativeSQL(rateSQLParams{Step: 60, SeriesKey: "SERIESKEY", GroupExpr: "GROUPEXPR", ValueExpr: "VALUECOL", Where: "WHERE x = 1"})

	if got := colExpr(t, sql, "sk"); got != "SERIESKEY" {
		t.Errorf("sk seri anahtarını almalı, alınan %q", got)
	}
	if got := colExpr(t, sql, "gk"); got != "GROUPEXPR" {
		t.Errorf("gk grup ifadesini almalı, alınan %q", got)
	}
	// Değer, toplama fonksiyonunun İÇİNDE olmalı.
	// v0.9.686 — toFloat64 sarmalaması eklendi: `count` UInt64, `value`
	// Float64, Go ikisini de *float64'e tarıyor.
	if got := colExpr(t, sql, "v"); got != "argMaxOrNull(toFloat64(VALUECOL), time)" {
		t.Errorf("v değer kolonunu toFloat64 ile sarmalı, alınan %q", got)
	}
	if !strings.Contains(sql, "WHERE x = 1") {
		t.Error("WHERE cümlesi düştü")
	}
	if !strings.Contains(sql, "INTERVAL 60 SECOND") {
		t.Error("adım geçmedi")
	}
}

func TestBuildRateDeltaSQLPlaceholderOrder(t *testing.T) {
	sql := buildRateDeltaSQL(rateSQLParams{Step: 30, GroupExpr: "GROUPEXPR", ValueExpr: "VALUECOL", Where: "WHERE y = 2"})

	if got := colExpr(t, sql, "gk"); got != "GROUPEXPR" {
		t.Errorf("gk grup ifadesini almalı, alınan %q", got)
	}
	if got := colExpr(t, sql, "v"); got != "sumOrNull(toFloat64(VALUECOL))" {
		t.Errorf("v değer kolonunu toFloat64 ile sarmalı, alınan %q", got)
	}
	if !strings.Contains(sql, "WHERE y = 2") {
		t.Error("WHERE cümlesi düştü")
	}
	if !strings.Contains(sql, "INTERVAL 30 SECOND") {
		t.Error("adım geçmedi")
	}
}

// GERÇEK İFADELERLE: sk String üretmeli (Go tarafı string'e tarıyor),
// değer kolonu ise sk'ya SIZMAMALI. Hatanın tam şekli buydu.
func TestRateSQLSeriesKeyIsStringNotValueColumn(t *testing.T) {
	for _, hasFp := range []bool{true, false} {
		key := metricSeriesKeyExpr(hasFp)
		sql := buildRateCumulativeSQL(rateSQLParams{Step: 60, SeriesKey: key, GroupExpr: "[]::Array(String)", ValueExpr: "count", Where: "WHERE 1"})
		sk := colExpr(t, sql, "sk")

		if !strings.HasPrefix(sk, "toString(") {
			t.Errorf("hasFp=%v: sk String üretmeli (Go string'e tarıyor), alınan %q", hasFp, sk)
		}
		if sk == "count" || sk == "value" {
			t.Errorf("hasFp=%v: DEĞER KOLONU sk'ya sızmış (%q) — v0.9.668 hatası", hasFp, sk)
		}
	}
}

// Histogram ve sayaç AYNI şablonu farklı değer kolonuyla kullanıyor;
// ikisinde de yalnız değer değişmeli, sk/gk sabit kalmalı.
func TestRateSQLValueColumnSwapsOnly(t *testing.T) {
	key := metricSeriesKeyExpr(false)
	counter := buildRateCumulativeSQL(rateSQLParams{Step: 60, SeriesKey: key, GroupExpr: "GRP", ValueExpr: "value", Where: "WHERE 1"})
	hist := buildRateCumulativeSQL(rateSQLParams{Step: 60, SeriesKey: key, GroupExpr: "GRP", ValueExpr: "count", Where: "WHERE 1"})

	if colExpr(t, counter, "sk") != colExpr(t, hist, "sk") {
		t.Error("sk değer kolonuna göre DEĞİŞMEMELİ")
	}
	if colExpr(t, counter, "gk") != colExpr(t, hist, "gk") {
		t.Error("gk değer kolonuna göre DEĞİŞMEMELİ")
	}
	if colExpr(t, counter, "v") == colExpr(t, hist, "v") {
		t.Error("v değer kolonuna göre DEĞİŞMELİ")
	}
}

// ÇAĞRI YERİ testi. Yukarıdaki testler kurucuyu KENDİ argümanlarıyla
// çağırıyor; v0.9.668 hatası ise ÇAĞRI YERİNDEYDİ ve o testler onu
// yakalamadı (mutasyonla kanıtlandı — hata geri konunca hepsi geçti).
//
// rateSQLParams struct'ı hatayı zaten imkânsız kılıyor, ama konumsal
// çağrıya geri dönülmesini de engellemek gerekiyor: bu tarama, çağrı
// yerlerinin alan ADLARIYLA kurulduğunu çiviliyor.
func TestRateSQLCallSitesUseNamedFields(t *testing.T) {
	src, err := os.ReadFile("metricrate.go")
	if err != nil {
		t.Fatal(err)
	}
	body := stripGoLineComments(string(src))

	for _, fn := range []string{"buildRateCumulativeSQL(", "buildRateDeltaSQL("} {
		i := strings.Index(body, fn)
		if i < 0 {
			t.Fatalf("%s çağrısı bulunamadı", fn)
		}
		// Çağrıdan sonraki kısa pencerede struct literali olmalı.
		// v0.9.714 — 220→320: struct MonoExpr alanı kazandı, Where: pencere
		// dışına taşmıştı (kapı doğru kızmıştı; sözleşme büyüdü).
		win := body[i : i+320]
		// `rateSQLParams{` aramak YETMEZ: konumsal literal de onu içerir
		// (ilk denememde tam bu yüzden mutasyon testi geçti). ALAN ADI
		// aranmalı — asıl koruma o.
		if !strings.Contains(win, "rateSQLParams{") {
			t.Errorf("%s rateSQLParams kullanmıyor:\n%s", fn, win)
			continue
		}
		fields := []string{"Step:", "GroupExpr:", "ValueExpr:", "Where:"}
		if fn == "buildRateCumulativeSQL(" {
			fields = append(fields, "MonoExpr:") // v0.9.714 — mono SELECT'e taşındı
		}
		for _, field := range fields {
			if !strings.Contains(win, field) {
				t.Errorf("%s konumsal kurulmuş — %q alan adı yok. Aynı tipte "+
					"dört string sessizce kayar (v0.9.668):\n%s", fn, field, win)
			}
		}
	}

	// Değer kolonu YALNIZ ValueExpr alanına gitmeli.
	if strings.Contains(body, "SeriesKey: src.valueExpr") ||
		strings.Contains(body, "GroupExpr: src.valueExpr") {
		t.Error("değer kolonu yanlış alana bağlanmış — v0.9.668 hatasının şekli")
	}
}

// v0.9.686 — DEĞER HER ZAMAN Float64 OLMALI.
//
// Operatörün ekranındaki ikinci hata:
//
//	clickhouse [ScanRow]: (v) converting UInt64 to *float64
//
// `count` UInt64, `value` Float64; Go tarafı ikisini de *float64'e
// tarıyor. v0.9.668'de histogram için kolonu count'a çevirdim ama tarama
// tipini düşünmedim — sk hatasıyla AYNI commit'te, aynı sınıf.
//
// Not: bu tipi kanıt olarak elimde vardı — alt-ajanın SQL doğrulaması
// `v=Nullable(UInt64)` yazıyordu ve bağlantıyı kurmadım. Veri
// oradaydı, okumadım.
func TestRateSQLValueIsAlwaysFloat(t *testing.T) {
	for _, src := range []rateSource{rateSourceCounter, rateSourceHistogramCount} {
		cum := buildRateCumulativeSQL(rateSQLParams{
			Step: 60, SeriesKey: "SK", GroupExpr: "GK", ValueExpr: src.valueExpr, Where: "WHERE 1"})
		if got := colExpr(t, cum, "v"); !strings.Contains(got, "toFloat64(") {
			t.Errorf("cumulative/%s: değer toFloat64 ile sarılmalı, alınan %q", src.instrument, got)
		}
		del := buildRateDeltaSQL(rateSQLParams{
			Step: 60, GroupExpr: "GK", ValueExpr: src.valueExpr, Where: "WHERE 1"})
		if got := colExpr(t, del, "v"); !strings.Contains(got, "toFloat64(") {
			t.Errorf("delta/%s: değer toFloat64 ile sarılmalı, alınan %q", src.instrument, got)
		}
	}
}

// Go tarafı gerçekten *float64 tarıyor — sözleşmenin diğer ucu.
func TestRateScanTargetIsFloat(t *testing.T) {
	src, err := os.ReadFile("metricrate.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stripGoLineComments(string(src)), "var v *float64") {
		t.Error("tarama hedefi *float64 değil — SQL tarafındaki toFloat64 sarmalaması onunla eşleşmeli")
	}
}

// v0.9.724 — okuma tarafı yazar kimliği zinciri ingest ile simetrik:
// synthetic sk, instance.id → k8s.pod.name → host_name zincirini
// kurmalı. Zincir düşerse instance.id yaymayan çok-pod'lu servislerin
// sayaçları tek sk'ya çöker (prod testere vakası) — ve bu sessiz olur.
func TestSeriesKeyWriterIdentityChain(t *testing.T) {
	for _, hasFp := range []bool{true, false} {
		key := metricSeriesKeyExpr(hasFp)
		for _, want := range []string{"service.instance.id", "k8s.pod.name", "host_name"} {
			if !strings.Contains(key, want) {
				t.Errorf("hasFp=%v: sk ifadesinde %q yok — yazar zinciri eksik", hasFp, want)
			}
		}
		// Zincir sırası: instance.id, pod, host_name (tercih sırası).
		i1 := strings.Index(key, "service.instance.id")
		i2 := strings.Index(key, "k8s.pod.name")
		i3 := strings.Index(key, "host_name")
		if !(i1 < i2 && i2 < i3) {
			t.Errorf("hasFp=%v: zincir sırası bozuk (%d,%d,%d)", hasFp, i1, i2, i3)
		}
	}
}
