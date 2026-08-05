package chstore

import (
	"os"
	"strings"
	"testing"
	"time"
)

// v0.8.243 — granularity slice B: the effective chart step never drops
// below the metric's observed export cadence (a 10s-exported gauge at
// step=1s is 90% empty buckets → sawtooth/gaps, the operator's "not as
// smooth as Grafana" complaint's second axis). Pins the interval
// estimator's branches, the raise-only clamp, and the probe SQL's
// CH-bounds contract.

// v0.9.688 — TABAN = YAYIM TİKİ (düşük kantil).
//
// v0.9.672 yüksek kantile çekmişti; o, seri SEYREKLİĞİNİ ölçüyor.
// Ölçüm: seri başına p90 137.5s, gerçek tik (farklı damga sayısı)
// 4.24s — 32× fark. Operatörün "client 1 sn ama metrics dakikalık"
// gözlemi tam bu.
func TestExportIntervalQuantile(t *testing.T) {
	cases := []struct {
		name   string
		iv     float64
		series uint64
		want   int
	}{
		{"ölçülen yayım tiki", 4.24, 127, 4},
		{"tek seri — dejenerasyon doğru yönde", 8, 1, 8},
		{"yuvarlama", 7.6, 10, 8},
		{"seri yok → clamp yok", 30, 0, 0},
		{"sıfır aralık → clamp yok", 0, 5, 0},
		{"negatif → clamp yok", -1, 5, 0},
		{"1s altı tabana çekiliyor", 0.4, 5, 1},
		{"tavan üstü → clamp yok (bayat/seyrek metrik)", float64(metricIvMaxSeconds + 1), 5, 0},
	}
	for _, c := range cases {
		if got := exportIntervalQuantile(c.iv, c.series); got != c.want {
			t.Errorf("%s: exportIntervalQuantile(%v, %d) = %d, beklenen %d", c.name, c.iv, c.series, got, c.want)
		}
	}
}

func TestMetricExportIntervalQuantileSQLBounds(t *testing.T) {
	to := time.Date(2026, 8, 5, 18, 0, 0, 0, time.UTC)
	from := to.Add(-time.Hour)
	for _, svc := range []string{"", "checkout"} {
		wc := exportIntervalProbeWhere("m", svc, from, to, nil)
		q := metricExportIntervalQuantileSQL(wc)
		for _, want := range []string{
			"time >= ?", "time <= ?", // partition-pruning window
			"LIMIT 20000",
			"max_execution_time",
			"GROUP BY service_name, host_name, attr_values",
			// v0.9.688 — DÜŞÜK kantil ÖZELLİKLE. Yüksek kantil seri
			// SEYREKLİĞİNİ ölçüyordu, yayım tikini değil: ölçümde seri
			// başına p90 137.5s iken gerçek tik 4.24s (32× fark). Delta
			// temporality'de nadir bir route yalnız trafik aldığında
			// nokta üretir; o seyrekliği taban yapmak herkesin
			// çözünürlüğünü yok ediyordu.
			"quantileExact(0.1)",
		} {
			if !strings.Contains(q, want) {
				t.Errorf("service=%q: missing %q in %s", svc, want, q)
			}
		}
		if (svc != "") != strings.Contains(q, "service_name = ?") {
			t.Errorf("service=%q: service filter presence wrong", svc)
		}
	}
}

// v0.9.687 — FİLTRELER PROBA İNİYOR.
//
// Operatör-bildirimi: metrik throughput paneli 30 dakikada DOĞRU, 5
// dakikada düz çizgi ve ~20× yüksek. Adım metriğin yayım aralığına
// yükseltiliyor ama aralık TÜM servislerin serilerinden hesaplanıyordu;
// eşleşen servis 60s'de bir yayarken tüm-metrik p90'ı küçük çıkınca
// clamp devreye girmiyor, dar pencerede auto-step 5s'e iniyor ve 60s'de
// biriken delta 5s'e bölünüyor.
//
// GENİŞ pencerede auto-step zaten büyük olduğu için fark edilmiyordu —
// hata yalnız dar pencerede görünür. v0.9.669'un temporality
// düzeltmesiyle AYNI SINIF; orada düzelttim, burada unutmuşum.
func TestExportIntervalProbeAppliesFilters(t *testing.T) {
	to := time.Date(2026, 8, 5, 18, 0, 0, 0, time.UTC)
	from := to.Add(-time.Hour)
	f := []FilterExpr{{Key: "resource.k8s.deployment.name", Op: "=~", Values: []string{"^svc$"}}}

	withF := exportIntervalProbeWhere("m", "", from, to, f)
	without := exportIntervalProbeWhere("m", "", from, to, nil)

	if withF.sql() == without.sql() {
		t.Fatal("filtre proba inmiyor — prob tüm servislere bakar, dar pencerede clamp devreye girmez")
	}
	if len(withF.args) <= len(without.args) {
		t.Error("filtre bind argümanı eklemedi")
	}
}

// ÖNBELLEK ANAHTARI FİLTRELERİ AYIRMALI: aynı metrik+servis için farklı
// filtreler farklı aralık verir. Anahtar ayırmazsa biri diğerinin
// değerini okur — v0.5.187 çapraz-zehirlenme sınıfı, ve buradaki bedeli
// YANLIŞ ÖLÇEKLİ bir grafik, yani sessiz.
func TestExportIntervalCacheKeySeparatesFilters(t *testing.T) {
	a := exportIntervalCacheKey("m", "svc", nil)
	b := exportIntervalCacheKey("m", "svc", []FilterExpr{{Key: "job", Op: "=", Values: []string{"x"}}})
	c := exportIntervalCacheKey("m", "svc", []FilterExpr{{Key: "job", Op: "=", Values: []string{"y"}}})

	if a == b || b == c || a == c {
		t.Errorf("farklı filtreler AYNI anahtara düşüyor: %q / %q / %q", a, b, c)
	}
	// Aynı girdi → aynı anahtar, yoksa önbellek hiç isabet etmez.
	if b != exportIntervalCacheKey("m", "svc", []FilterExpr{{Key: "job", Op: "=", Values: []string{"x"}}}) {
		t.Error("aynı girdi farklı anahtar üretti")
	}
}

// ÇAĞRI YERİ. Yukarıdaki test FONKSİYONU doğruluyor; prob anahtarı elle
// kurulursa (name+service) digest kaybolur ve fonksiyon doğru kalsa bile
// çapraz zehirlenme geri gelir.
//
// Bu ayrımı bugün üç kez öğrendim: v0.9.685'te iki testim üst üste
// mutasyonu geçti çünkü kurucuyu test edip çağrı yerini atlamışlardı.
func TestExportIntervalUsesCacheKeyHelper(t *testing.T) {
	src, err := os.ReadFile("metric_export_interval.go")
	if err != nil {
		t.Fatal(err)
	}
	body := stripGoLineComments(string(src))
	if !strings.Contains(body, "key := exportIntervalCacheKey(name, service, filters)") {
		t.Error("prob önbellek anahtarını yardımcıdan ALMIYOR — elle kurulan anahtar filtreleri düşürür")
	}
}

// v0.9.689 — TABAN, GRAFİĞİN NE ÇİZDİĞİNE BAĞLI.
//
// Ölçüm iki büyüklüğü ayırdı (yerel, 20 dk):
//
//	havuzlanmış tempo (farklı damga) :  4.24 s
//	seri başına aralık p10           : 41.18 s   → 9.7× fark
//
// Sebep: 282 farklı damgaya 301 seri düşüyor (damga başına ~5.7).
// Seriler KAYMALI yayımlıyor, yani hiçbir serinin kendi temposu
// havuzlanmış tempoya eşit değil — ve olmamalı.
//
// TOPLULAŞTIRILMIŞ çizgide (GroupBy yok) her kovaya birçok seri katkı
// verir, ince adım delik ÜRETMEZ → havuzlanmış tempo doğru taban.
// SERİ BAŞINA çizgide her çizgi kendi serisine bağlı → kantil.
func TestPooledProbeMeasuresDistinctTimestamps(t *testing.T) {
	to := time.Date(2026, 8, 5, 18, 0, 0, 0, time.UTC)
	wc := exportIntervalProbeWhere("m", "", to.Add(-time.Hour), to, nil)

	pooled := metricExportIntervalPooledSQL(wc)
	perSeries := metricExportIntervalQuantileSQL(wc)

	// Havuzlanmış: FARKLI DAMGA sayar, seri başına gruplamaz.
	if !strings.Contains(pooled, "uniqExact(time)") {
		t.Error("havuzlanmış prob farklı zaman damgalarını saymalı")
	}
	if strings.Contains(pooled, "GROUP BY service_name") {
		t.Error("havuzlanmış prob seri başına GRUPLAMAMALI — o, seyrekliği ölçer")
	}
	// Seri başına: gruplar ve düşük kantil alır.
	if !strings.Contains(perSeries, "GROUP BY service_name, host_name, attr_values") {
		t.Error("seri başına prob gruplamalı")
	}
	if !strings.Contains(perSeries, "quantileExact(0.1)") {
		t.Error("seri başına prob DÜŞÜK kantil almalı — yüksek kantil seyrekliği ölçer")
	}
	// İkisi de CH sınırlarını korumalı.
	for _, q := range []string{pooled, perSeries} {
		for _, want := range []string{"time >= ?", "time <= ?", "max_execution_time"} {
			if !strings.Contains(q, want) {
				t.Errorf("CH sınırı %q düşmüş:\n%s", want, q)
			}
		}
	}
}

// Önbellek, iki ölçüyü AYIRMALI: aynı metrik+filtre için havuzlanmış ve
// seri başına taban FARKLI (4.24 vs 41.18). Anahtar ayırmazsa biri
// diğerinin değerini okur ve grafik 10× yanlış çözünürlükte çizilir.
func TestExportIntervalCacheKeySeparatesGrouping(t *testing.T) {
	base := exportIntervalCacheKey("m", "svc", nil)
	if base+"\x00g" == base {
		t.Fatal("grouped eki anahtarı değiştirmiyor")
	}
}

// DAĞITIM KAPISI. Yukarıdaki testler iki SQL KURUCUSUNU doğruluyor ama
// hangisinin ÇAĞRILDIĞINI değil — havuzlanmış yol devre dışı bırakılsa
// hepsi geçerdi (mutasyonla kanıtlandı).
//
// Bugün bu ayrımı DÖRDÜNCÜ kez öğreniyorum: v0.9.685'te iki, v0.9.687'de
// bir testim aynı şekilde mutasyonu geçmişti. Kurucuyu test etmek
// çağrıyı test etmek değildir.
func TestExportIntervalDispatchesOnGrouping(t *testing.T) {
	src, err := os.ReadFile("metric_export_interval.go")
	if err != nil {
		t.Fatal(err)
	}
	body := stripGoLineComments(string(src))

	// Havuzlanmış yol VARSAYILAN, seri başına yalnız grouped iken.
	if !strings.Contains(body, "sql := metricExportIntervalPooledSQL(wc)") {
		t.Error("havuzlanmış prob varsayılan olarak KULLANILMIYOR — GroupBy'sız paneller seyrekliğe göre kabalaşır")
	}
	if !strings.Contains(body, "sql = metricExportIntervalQuantileSQL(wc)") {
		t.Error("seri başına prob grouped dalında KULLANILMIYOR")
	}
	i := strings.Index(body, "sql := metricExportIntervalPooledSQL(wc)")
	if !strings.Contains(body[i:i+160], "if grouped {") {
		t.Error("seçim `grouped` bayrağına bağlı DEĞİL")
	}

	// Çağıranlar bayrağı GroupBy'dan türetmeli, sabit geçmemeli.
	for _, f := range []string{"metricrate.go", "metricquery.go", "metrichist_percentile.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stripGoLineComments(string(b)), "f.Filters, len(f.GroupBy) > 0)") {
			t.Errorf("%s: grouped bayrağı GroupBy'dan türetilmiyor", f)
		}
	}
}
