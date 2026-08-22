package chstore

// v0.9.1255 regresyon testleri — dış bağımlılık yol kırılımı.
//
// Neden bu dosya var: operatörün gördüğü şey `esbprod.example.internal`
// düğümüydü, bilmek istediği şey onun ALTINDAKİ uçtu
// (/tibcoESB/ExternalServices/NVI/KPS/YerlesimYeriSorgulama2). Cevap
// ham url.full olamazdı (tek span'ın değeri, yüksek kardinalite), o
// yüzden NORMALIZE yol grupları gönderildi. Bu dosya normalizasyonun
// hem YAPTIĞINI hem YAPMADIĞINI pinler:
//
//   - yaptığı: rakam/uuid/hex segmentleri {id}'ye çöker, query+fragment
//     düşer, sondaki eğik çizgi ve çift eğik çizgiler tek biçime iner
//   - YAPMADIĞI (asıl risk): operatörün aradığı UZUN BETİMLEYİCİ
//     segmenti ("YerlesimYeriSorgulama2", 22 karakter) çökertmez.
//     path_template.go'daki b64IdRe bunu ≥20 alfanumerik diye yerdi;
//     bu yüzeyde o kural bilerek YOK ve bir test onu geri gelmekten
//     korur — kimliği çökerten kural kimliğin KENDİSİNİ silmemeli.
//
// Ayrıca kriter tekliği: yol okumasının host ve kind yüklemleri
// topology aggregator'ın ext:<host> düğümünü üreten ifadesinden
// türemeli. İki kriter iki farklı çağrı sayısı demek olurdu — grafikte
// N, çekmecede M.

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNormalizeExternalPath(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// ── operatörün prod örneği: HİÇBİR şey çökmemeli ──────────
		{
			"prod örneği bozulmadan geçer",
			"http://esbprod.example.internal/tibcoESB/ExternalServices/NVI/KPS/YerlesimYeriSorgulama2",
			"/tibcoESB/ExternalServices/NVI/KPS/YerlesimYeriSorgulama2",
		},
		{
			"uzun betimleyici segment (22 karakter) ÇÖKMEZ — b64 kuralı bilerek yok",
			"/ExternalServices/YerlesimYeriSorgulama2",
			"/ExternalServices/YerlesimYeriSorgulama2",
		},

		// ── query / fragment ──────────────────────────────────────
		{"query düşer", "http://h/a/b?x=1&y=2", "/a/b"},
		{"fragment düşer", "http://h/a/b#frag", "/a/b"},
		{"göreli yolun query'si de düşer", "/a/b?x=1", "/a/b"},
		{"şemasız hostsuz kök", "http://h", "/"},
		{"şemalı kök", "http://h/", "/"},

		// ── rakam segmenti ────────────────────────────────────────
		{"rakam segmenti ortada", "/orders/12345/items", "/orders/{id}/items"},
		{"rakam segmenti SONDA (guard olmadan ıskalanırdı)", "/orders/12345", "/orders/{id}"},
		{"ardışık rakam segmentleri (tek geçiş yetmez)", "/a/1/2/b", "/a/{id}/{id}/b"},
		{"üç ardışık rakam segmenti", "/1/2/3", "/{id}/{id}/{id}"},

		// ── uuid / hex ────────────────────────────────────────────
		{
			"kanonik uuid",
			"/users/1b4e28ba-2fa1-11d2-883f-0016d3cca427/orders",
			"/users/{id}/orders",
		},
		{"32 hane md5", "/blob/d41d8cd98f00b204e9800998ecf8427e", "/blob/{id}"},
		{"16 hane trace id", "/t/4bf92f3577b34da6", "/t/{id}"},
		{"tam 8 hane hex çöker", "/s/deadbe12", "/s/{id}"},

		// ── ÇÖKMEMESİ gerekenler ──────────────────────────────────
		{"kısa hex /v2/ ÇÖKMEZ", "/api/v2/users", "/api/v2/users"},
		{"7 hane hex ÇÖKMEZ (eşik 8)", "/s/abc1234", "/s/abc1234"},
		{"segment içindeki rakam ÇÖKMEZ", "/api2/users", "/api2/users"},
		{"hex olmayan harf içeren segment ÇÖKMEZ", "/s/deadbeezg", "/s/deadbeezg"},

		// ── eğik çizgi normalizasyonu ─────────────────────────────
		{"sondaki eğik çizgi düşer", "/a/b/", "/a/b"},
		{"çift eğik çizgi tekleşir", "/a//b", "/a/b"},
		{"kök korunur", "/", "/"},
		{"boş girdi kök olur", "", "/"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := normalizeExternalPath(c.in); got != c.want {
				t.Errorf("normalizeExternalPath(%q) = %q, beklenen %q", c.in, got, c.want)
			}
		})
	}
}

// Idempotanlık: Go tarafı, CH'nin ZATEN normalize ettiği satırların
// üstünde bir kez daha koşuyor (GetExternalHostPaths). O ikinci geçiş
// etiketi değiştirirse gruplar sessizce bölünür.
func TestNormalizeExternalPathIdempotent(t *testing.T) {
	for _, in := range []string{
		"/orders/12345/items", "/users/1b4e28ba-2fa1-11d2-883f-0016d3cca427",
		"/tibcoESB/ExternalServices/NVI/KPS/YerlesimYeriSorgulama2", "/", "/a/b/",
	} {
		once := normalizeExternalPath(in)
		twice := normalizeExternalPath(once)
		if once != twice {
			t.Errorf("idempotan değil: %q → %q → %q", in, once, twice)
		}
	}
}

// Kural listesi TEK kaynak: Go tarafına eklenen bir kural SQL'e de
// inmeli. İnmezse gruplama (CH'de, LIMIT'ten ÖNCE) o kimliği
// çökertmez ve tavan ham varyantlarla dolar — Go'nun son geçişi
// etiketi düzeltir ama grup zaten kaybolmuştur.
func TestExternalPathRulesReachSQL(t *testing.T) {
	sql := externalPathNormalizeSQL("bp")
	for _, r := range externalPathRules {
		want := fmt.Sprintf("'%s', '%s'", r.Pattern, r.Replace)
		if !strings.Contains(sql, want) {
			t.Errorf("kural SQL'e inmemiş (%s): %s", r.Why, r.Pattern)
		}
		// Her kural externalPathPasses kadar koşmalı — tek geçiş
		// ardışık segmentleri ıskalar (RE2 örtüşmez).
		if n := strings.Count(sql, want); n != externalPathPasses {
			t.Errorf("kural %s SQL'de %d kez var, %d bekleniyordu", r.Pattern, n, externalPathPasses)
		}
	}
	if !strings.Contains(sql, externalPathSlashCollapse) {
		t.Error("eğik çizgi sadeleştirmesi SQL'e inmemiş — sondaki '/' guard'ı sökülmez")
	}
	// Geri-referans yasağı: CH'nin replaceRegexpAll kaçış kuralına
	// hiç girmiyoruz, desende de değişmezde de '\' olmamalı.
	if strings.Contains(sql, `\`) {
		t.Error("SQL'de ters eğik çizgi var — desenler geri-referanssız olmalı (CH kaçış tuzağı)")
	}
}

// Sorgunun sınırları + KRİTERİ. clickhouse-schema adım 4: ham spans
// okuması LIMIT + max_execution_time + indeksli zaman yüklemi taşır;
// buna ek olarak service_name ön eki (spans ORDER BY'ın ilk kolonu)
// taramayı hostu gerçekten çağıran servislere indirir.
func TestExternalPathsQueryBounds(t *testing.T) {
	q := externalPathsSQL()
	for _, want := range []string{
		"FROM spans",
		"time >= ? AND time < ?",
		"service_name IN ?",
		"LIMIT ?",
		"max_execution_time",
		"quantileTDigest(0.99)(duration)",
		"GROUP BY path",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("sorguda %q yok", want)
		}
	}
	// Ham quantile() milyon satırda yasak (clickhouse-schema
	// anti-pattern listesi) — tdigest dışında bir quantile çağrısı
	// sızmamalı.
	if strings.Contains(q, "quantile(0.99)") {
		t.Error("ham quantile() kullanılmış — quantileTDigest olmalı")
	}
	// Normalizasyon GRUPLAMADAN ÖNCE koşmalı: gruplama ham yolları
	// alırsa LIMIT 10 varyantlara bölünmüş bir grubu hiç göstermez.
	// (feedback: sabitin şeklini pinlemek bağlantıyı kanıtlamaz —
	// gerçekten koşan metne assert ediyoruz.)
	if !strings.Contains(q, externalPathNormalizeSQL("bp")) {
		t.Error("normalize ifadesi sorguya girmemiş — gruplama ham yollar üstünde koşuyor")
	}
}

// Kriter tekliği: yol okumasının host ifadesi topology aggregator'ın
// ext:<host> düğümünü üreten `infra_host` coalesce'iyle AYNI olmalı.
// İkinci bir kriter, grafikteki çağrı sayısıyla çekmecedekinin
// ıraksaması demekti (ve hangisinin doğru olduğu ölçülemezdi).
func TestExternalPathsHostCriterionMatchesAggregator(t *testing.T) {
	b, err := os.ReadFile("topology.go")
	if err != nil {
		t.Fatalf("topology.go okunamadı: %v", err)
	}
	src := string(b)
	// Aggregator ifadeyi çok satırlı yazıyor; boşlukları sıkıştırıp
	// karşılaştırıyoruz.
	squash := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	agg := squash(src)
	for _, part := range []string{
		"nullIf(peer_service, '')",
		"nullIf(attr_values[indexOf(attr_keys, 'server.address')], '')",
		"nullIf(attr_values[indexOf(attr_keys, 'net.peer.name')], '')",
	} {
		if !strings.Contains(agg, part) {
			t.Errorf("aggregator'da %q yok — infra_host zinciri değişmişse "+
				"externalPeerHostSQL de güncellenmeli (tek kriter kuralı)", part)
		}
		if !strings.Contains(squash(externalPeerHostSQL), part) {
			t.Errorf("externalPeerHostSQL %q taşımıyor — aggregator ile ıraksadı", part)
		}
	}
	q := externalPathsSQL()
	if !strings.Contains(q, "kind = 'client'") {
		t.Error("kind='client' yüklemi yok — aggregator'ın iki ext dalının da ortak şartı")
	}
	if !strings.Contains(q, "db_system = '' AND msg_system = ''") {
		t.Error("db/queue dışlaması yok — aggregator'ın multiIf'i db/queue dallarını " +
			"ext'ten ÖNCE alıyor, o span'lar dış düğüme katkı vermez")
	}
}

// Pencere kırpması: ham spans okuması 7 günlük bir seçimle koşamaz.
// Kırpma SONA yaslanır (en taze pencere) ve dar aralıklara dokunmaz.
func TestClampExternalPathsWindow(t *testing.T) {
	to := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		from    time.Time
		wantDur time.Duration
	}{
		{"1 saat dokunulmaz", to.Add(-time.Hour), time.Hour},
		{"tam sınır dokunulmaz", to.Add(-externalPathsMaxWindow), externalPathsMaxWindow},
		{"7 gün kırpılır", to.Add(-7 * 24 * time.Hour), externalPathsMaxWindow},
		{"30 gün kırpılır", to.Add(-30 * 24 * time.Hour), externalPathsMaxWindow},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, tt := clampExternalPathsWindow(c.from, to)
			if !tt.Equal(to) {
				t.Errorf("üst sınır kaydı: %v != %v", tt, to)
			}
			if got := tt.Sub(f); got != c.wantDur {
				t.Errorf("pencere %v, beklenen %v", got, c.wantDur)
			}
		})
	}
}
