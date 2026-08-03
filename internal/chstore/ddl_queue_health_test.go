// v0.9.613 — DDL kuyruğu verdict'inin tablo testi.
//
// Verdict SAF çünkü karar burada: yanlış bir verdict operatörü yanlış
// düzeltmeye gönderir (takılı worker'a DNS düzeltmesi, uyuşmazlığa
// restart) ve üçüncü geceyi dördüncü yapar.
package chstore

import (
	"strings"
	"testing"
)

func TestDDLQueueVerdict(t *testing.T) {
	cases := []struct {
		name            string
		stuck           uint64
		approx          bool
		hosts           []DDLHostProgress
		unreachable     []string
		queueFailed     bool
		progressErrored bool
		want            string
	}{
		{"boş kuyruk sağlıklı", 0, false, []DDLHostProgress{{Host: "a", Behind: 0}}, nil, false, false, "healthy"},
		// Kuyruk boşsa geride kalmanın maliyeti yok — healthy kalır.
		{"boş kuyruk + geride host yine sağlıklı", 0, false, []DDLHostProgress{{Host: "a", Behind: 7}}, nil, false, false, "healthy"},
		// Verify bulgusu: stuck=0 + unreachable dolu → yine healthy.
		// Bilinçli sıralama; değişirse sessiz regresyon olmasın.
		{"boş kuyruk + unreachable yine sağlıklı", 0, false, nil, []string{"b"}, false, false, "healthy"},
		{"probe düştü → körüz", 5, false, nil, nil, true, false, "probe_failed"},
		// Verify bulgusu: queueFailed her şeyi gölgeler.
		{"queueFailed + unreachable → probe_failed", 5, false, nil, []string{"b"}, true, false, "probe_failed"},
		{"host ulaşılamıyor", 50, false, []DDLHostProgress{{Host: "a", Behind: 0}}, []string{"b"}, false, false, "unreachable"},
		{"worker takılı", 50, false, []DDLHostProgress{{Host: "a", Behind: 0}, {Host: "b", Behind: 120}}, nil, false, false, "worker_stuck"},
		// OPERATÖRÜN PROD VAKASI: girdiler Inactive, kimse geride
		// değil → worker'lar canlı ve atlıyor.
		{"prod vakası: atlama", 50, false, []DDLHostProgress{{Host: "a", Behind: 0}, {Host: "b", Behind: 0}}, nil, false, false, "worker_skipping"},
		// İlerleme verisi YOKKEN atlama teşhisi UYDURMA olur.
		{"ilerleme hiç dönmedi → körüz", 50, false, nil, nil, false, false, "probe_failed"},
		// Verify bulgusu (kritik): probe 2 HATAYLA kesildiyse eldeki
		// kısmi host listesi de güvenilmez — worker_skipping/unreachable
		// üretme, probe_failed de.
		{"ilerleme KESİLDİ → körüz", 50, false, []DDLHostProgress{{Host: "a", Behind: 0}}, nil, false, true, "probe_failed"},
		// Öncelik: unreachable, stuck'tan önce (en somut arıza önce).
		{"unreachable stuck'ı gölgeler", 50, false, []DDLHostProgress{{Host: "a", Behind: 9}}, []string{"c"}, false, false, "unreachable"},
		// Yaklaşık sayım detail'de İTİRAF edilmeli.
		{"yaklaşık sayım", 7, true, []DDLHostProgress{{Host: "a", Behind: 0}}, nil, false, false, "worker_skipping"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, detail := ddlQueueVerdict(c.stuck, c.approx, c.hosts, c.unreachable, c.queueFailed, c.progressErrored)
			if got != c.want {
				t.Errorf("verdict %q, beklenen %q — yanlış verdict operatörü "+
					"yanlış düzeltmeye gönderir", got, c.want)
			}
			if detail == "" {
				t.Error("detail boş — verdict tek başına eylem söylemez")
			}
		})
	}
	// worker_stuck detayı GERÇEK düzeltmeyi söylemeli: restart + neden
	// güvenli olduğu. Operatör gece 3'te gerekçe ister.
	_, d := ddlQueueVerdict(5, false, []DDLHostProgress{{Host: "x", Behind: 3}}, nil, false, false)
	for _, want := range []string{"yeniden başlat", "Keeper", "TEKER TEKER"} {
		if !strings.Contains(d, want) {
			t.Errorf("worker_stuck detayı %q içermiyor: %q", want, d)
		}
	}
}

// TestHostPrefixMatching — "chc-1" ↔ "chc-10" tuzağı.
func TestHostPrefixMatching(t *testing.T) {
	if !hasHostPrefix("chc-0.chc-headless", "chc-0") {
		t.Error("kısa ad FQDN'in öneki sayılmadı")
	}
	if hasHostPrefix("chc-10.chc-headless", "chc-1") {
		t.Error("chc-1, chc-10'a eşleşti — nokta sınırı şart, yoksa yanlış " +
			"host 'cevap veriyor' sayılır ve gerçek unreachable gizlenir")
	}
	if !hasHostPrefix("lckhsdbp01.aknet.akb", "lckhsdbp01") {
		t.Error("prod biçimi eşleşmedi")
	}
	if !hostRespondedTo(map[string]bool{"chc-0": true}, "chc-0.chc-headless") {
		t.Error("kısa ad ↔ FQDN çift yönlü eşleşme çalışmıyor")
	}
	if hostRespondedTo(map[string]bool{"chc-0": true}, "chc-1.chc-headless") {
		t.Error("farklı host eşleşti")
	}
}

// TestDDLQueueVerdictApproxAdmitted — yaklaşık sayım gizlenmez.
func TestDDLQueueVerdictApproxAdmitted(t *testing.T) {
	_, d := ddlQueueVerdict(7, true, []DDLHostProgress{{Host: "a", Behind: 1}}, nil, false, false)
	if !strings.Contains(d, "en az 7") {
		t.Errorf("yaklaşık sayım itiraf edilmiyor: %q — sessiz kırpma \"hepsi bu\" okunur", d)
	}
}

// TestDDLEntryNumber — restart dayanıklılığının temeli.
func TestDDLEntryNumber(t *testing.T) {
	if n := ddlEntryNumber("query-0000647099"); n != 647099 {
		t.Errorf("prod biçimi çözülmedi: %d", n)
	}
	for _, bad := range []string{"", "query", "query-", "query-abc"} {
		if n := ddlEntryNumber(bad); n != -1 {
			t.Errorf("%q için -1 beklenirdi, %d döndü", bad, n)
		}
	}
}

// TestReverseHostMatch — verify bulgusu: ters yön (responding FQDN,
// cluster kısa ad) de eşleşmeli.
func TestReverseHostMatch(t *testing.T) {
	if !hostRespondedTo(map[string]bool{"chc-0.chc-headless": true}, "chc-0") {
		t.Error("FQDN cevap verirken kısa cluster adı eşleşmedi")
	}
}
