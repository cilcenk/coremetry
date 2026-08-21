package anomaly

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.510 — CoSRE P1 soruşturması. Playbook SAF olduğu için sözleşmesi
// burada pinli: hangi problem şekli hangi sinyal ailelerini tetikler.
//
// Neden tablo-testi: dallanma "gerçek SRE refleksi"ni kodluyor (hata
// oranı → exception+log, gecikme → doygunluk+operasyon). Bir metrik adı
// eklenip yanlış dala düşerse soruşturma sessizce YANLIŞ kanıtı toplar ve
// model onu güvenle anlatır — sessiz kalitesizlik. Test o sessizliği kırar.

func TestInvestigationPlan(t *testing.T) {
	cases := []struct {
		name   string
		metric string
		rule   string
		want   []signalFamily
	}{
		// Hata ailesi
		{"error_rate", "error_rate", "Checkout hata oranı", []signalFamily{familyExceptions, familyLogs, familyBusiness}},
		{"errors mutlak", "errors", "", []signalFamily{familyExceptions, familyLogs, familyBusiness}},
		{"kural adı exception der", "custom_metric", "Exception spike", []signalFamily{familyExceptions, familyLogs, familyBusiness}},

		// Gecikme ailesi — doygunluk ÖNCE (heap/GC sessiz yavaşlatıcıdır)
		{"p99", "p99_ms", "", []signalFamily{familySaturation, familyOperations, familyBusiness}},
		{"p95", "p95_ms", "", []signalFamily{familySaturation, familyOperations, familyBusiness}},
		{"avg", "avg_ms", "", []signalFamily{familySaturation, familyOperations, familyBusiness}},
		{"latency", "latency_ms", "", []signalFamily{familySaturation, familyOperations, familyBusiness}},

		// Ayakta mı
		{"down", "down", "", []signalFamily{familyRuntime, familyLogs}},
		{"availability", "availability", "", []signalFamily{familyRuntime, familyLogs}},
		{"kural adı DOWN der", "custom", "Service DOWN", []signalFamily{familyRuntime, familyLogs}},

		// Doygunluk kuralları
		{"heap", "jvm_heap_pct", "", []signalFamily{familySaturation, familyRuntime}},
		{"gc", "gc_pause_ms", "", []signalFamily{familySaturation, familyRuntime}},
		{"restart", "pod_restarts", "", []signalFamily{familySaturation, familyRuntime}},

		// Derin soruşturma EKLEMEYENLER — hacim düşüşü genelde yukarı
		// akıştan gelir; bugünkü komşuluk paketi zaten doğru araç ve
		// gereksiz okuma maliyettir.
		{"request_rate", "request_rate", "", nil},
		{"throughput", "throughput", "", nil},
		{"bilinmeyen metrik", "some_custom_gauge", "", nil},
		{"boş", "", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := investigationPlan(chstore.Problem{Metric: c.metric, RuleName: c.rule})
			if len(got) != len(c.want) {
				t.Fatalf("plan(%q,%q) = %v, beklenen %v", c.metric, c.rule, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("plan(%q) [%d] = %q, beklenen %q — SIRA da sözleşmenin parçası",
						c.metric, i, got[i], c.want[i])
				}
			}
		})
	}
}

// Metrik adı büyük/küçük harf ya da boşlukla gelirse de doğru dala
// gitmeli — kural adları operatör-şekilli serbest metin.
func TestInvestigationPlanNormalizes(t *testing.T) {
	for _, m := range []string{"ERROR_RATE", "  error_rate  ", "Error_Rate"} {
		got := investigationPlan(chstore.Problem{Metric: m})
		if len(got) == 0 || got[0] != familyExceptions {
			t.Errorf("plan(%q) = %v — normalize edilmeli", m, got)
		}
	}
}

// Denetim izi DÜRÜST olmalı: okuma patlarsa "bakılamadı", kayıt yoksa
// "bakıldı, kayıt yok". Sessizce atlamak izi yalancı yapar ve izin tek
// işi modelin anlatımını denetlenebilir kılmak.
func TestDeepEvidenceNoteHonesty(t *testing.T) {
	var d chstore.DeepEvidence

	noteChecked(&d, familyExceptions, errFake{}, 0, func() string { return "olmamalı" })
	if len(d.Checked) != 1 || d.Checked[0].Found {
		t.Fatal("hata durumu Found=false olmalı")
	}
	if !strings.Contains(d.Checked[0].Detail, "okunamadı") {
		t.Errorf("hata detayı okunamadığını söylemeli, got %q", d.Checked[0].Detail)
	}

	noteChecked(&d, familyLogs, nil, 0, func() string { return "olmamalı" })
	if d.Checked[1].Found || !strings.Contains(d.Checked[1].Detail, "kayıt yok") {
		t.Errorf("boş sonuç 'bakıldı, kayıt yok' demeli, got %+v", d.Checked[1])
	}

	noteChecked(&d, familyRuntime, nil, 3, func() string { return "üç kayıt" })
	if !d.Checked[2].Found || d.Checked[2].Records != 3 {
		t.Errorf("dolu sonuç Found=true + Records taşımalı, got %+v", d.Checked[2])
	}
}

// renderDeepEvidence boş izde HİÇBİR ŞEY basmamalı — derin soruşturma
// koşmamış bir problemin kanıt bloğuna boş "SORUŞTURMA" başlığı düşmesi
// modele "bakıldı ama bulunamadı" izlenimi verir. Yanlış izlenim.
func TestRenderDeepEvidenceEmpty(t *testing.T) {
	var sb strings.Builder
	renderDeepEvidence(&sb, chstore.DeepEvidence{})
	if sb.Len() != 0 {
		t.Errorf("boş kanıtta çıktı olmamalı, got %q", sb.String())
	}
}

func TestRenderDeepEvidenceListsWhatWasChecked(t *testing.T) {
	var sb strings.Builder
	renderDeepEvidence(&sb, chstore.DeepEvidence{
		Checked: []chstore.CheckedSignal{
			{Family: string(familySaturation), Found: true, Detail: "2 pod heap örneği", Records: 2},
			{Family: string(familyOperations), Found: false, Detail: "bakıldı, kayıt yok"},
		},
	})
	out := sb.String()
	for _, want := range []string{"SORUŞTURMA", "saturation", "VAR", "operations", "yok"} {
		if !strings.Contains(out, want) {
			t.Errorf("çıktıda %q yok:\n%s", want, out)
		}
	}
}

type errFake struct{}

func (errFake) Error() string { return "ch down" }

// v0.9.511 — iş boyutu (kanal/fonksiyon kodu) YALNIZ operatöre görünen
// etkiyi anlatan dallarda okunur: hata ve gecikme. DOWN dalında servis
// zaten ayakta değil, kırılım gürültü; doygunluk dalı pod-içi bir sorun.
// Gereksiz dala eklemek P1 başına iki fazla spans okuması demek.
func TestBusinessDimOnlyOnUserFacingBranches(t *testing.T) {
	has := func(fs []signalFamily) bool {
		for _, f := range fs {
			if f == familyBusiness {
				return true
			}
		}
		return false
	}
	for _, m := range []string{"error_rate", "p99_ms"} {
		if !has(investigationPlan(chstore.Problem{Metric: m})) {
			t.Errorf("%s dalında iş boyutu olmalı", m)
		}
	}
	for _, m := range []string{"down", "jvm_heap_pct", "request_rate"} {
		if has(investigationPlan(chstore.Problem{Metric: m})) {
			t.Errorf("%s dalında iş boyutu OLMAMALI — gereksiz spans okuması", m)
		}
	}
}

// v0.9.512 — kanal kodu sözlüğü RAG dokümanı olarak yükleniyor; kodun
// anlamı chunk'ın TAMAMI değil kodu İÇEREN SATIR olarak alınır. 2B'nin
// bağlamına koca bir doküman parçası koymak asıl kanıtı (kırılım
// sayılarını) bastırır.
func TestCodeLineFrom(t *testing.T) {
	doc := `Kanal kodları
0001 - Internet Şube (Bireysel)
0012 - Mobil Kanal (Bireysel)
0044 - Çağrı Merkezi
`
	cases := []struct{ code, want string }{
		{"0012", "0012 - Mobil Kanal (Bireysel)"},
		{"0044", "0044 - Çağrı Merkezi"},
		{"0001", "0001 - Internet Şube (Bireysel)"},
	}
	for _, c := range cases {
		if got := codeLineFrom(doc, c.code); got != c.want {
			t.Errorf("codeLineFrom(%q) = %q, beklenen %q", c.code, got, c.want)
		}
	}

	// Kod hiçbir satırda yoksa BOŞ döner. Chunk sözcüksel skorla gelmiş
	// olabilir ama eşleşme başka bir jetondan gelmiş olabilir; yanlış
	// satır göstermektense hiç göstermemek doğru.
	if got := codeLineFrom(doc, "9999"); got != "" {
		t.Errorf("eşleşmeyen kod boş dönmeli, got %q", got)
	}
	if got := codeLineFrom(doc, ""); got != "" {
		t.Errorf("boş kod boş dönmeli, got %q", got)
	}

	// Uzun satır kırpılır ama kırpıldığı belli olur.
	long := strings.Repeat("x", 300) + " 0012"
	got := codeLineFrom(long, "0012")
	if len(got) > 130 || !strings.HasSuffix(got, "…") {
		t.Errorf("uzun satır kırpılmalı ve kırpıldığı belli olmalı, got len=%d", len(got))
	}
}

// ── v0.9.1206 (Faz 6.2) — render guard'ları + Runtime branşı ─────────

// Limit==0 heap satırı +Inf basmamalı (girdi SAKLANAN JSON — eski
// yazıcının bıraktığı satır render'da patlamamalı), GC örnekleri
// tavansız da yaşamalı (JVMGCPodPause Limit doldurmaz; eski koşulsuz
// filtre GC'nin tamamını eliyor, render dalını ölü koda çeviriyordu).
func TestRenderDeepEvidenceLimitZeroGuard(t *testing.T) {
	var sb strings.Builder
	renderDeepEvidence(&sb, chstore.DeepEvidence{
		Checked: []chstore.CheckedSignal{{Family: "saturation", Found: true}},
		Heap: []chstore.CapacitySample{
			{Subkey: "pod-a", Usage: 82, Limit: 100},
			{Subkey: "pod-b", Usage: 3.2e8, Limit: 0}, // tavansız — %'ye bölünemez
		},
		GCPause: []chstore.CapacitySample{{Subkey: "pod-a", Usage: 1850, Limit: 0}},
	})
	out := sb.String()
	if strings.Contains(out, "Inf") || strings.Contains(out, "NaN") {
		t.Fatalf("tavansız heap satırı çöp üretti:\n%s", out)
	}
	for _, want := range []string{"pod-a: heap %82", "pod-b: heap 3.2e+08 (tavan bilinmiyor)", "GC duraklama 1850"} {
		if !strings.Contains(out, want) {
			t.Errorf("render %q içermeli:\n%s", want, out)
		}
	}
}

func TestSamplesForServiceGCKeepsLimitless(t *testing.T) {
	in := []chstore.CapacitySample{
		{Instance: "svc", Subkey: "pod-a", Usage: 1850, Limit: 0},
		{Instance: "other", Subkey: "pod-x", Usage: 5, Limit: 0},
	}
	if got := samplesForService(in, "svc", false); len(got) != 1 || got[0].Subkey != "pod-a" {
		t.Fatalf("tavansız GC örneği yaşamalıydı: %+v", got)
	}
	if got := samplesForService(in, "svc", true); len(got) != 0 {
		t.Fatalf("requireLimit=true tavansızı elemeli: %+v", got)
	}
}

func TestRenderDeepEvidenceRuntime(t *testing.T) {
	var sb strings.Builder
	renderDeepEvidence(&sb, chstore.DeepEvidence{
		Checked: []chstore.CheckedSignal{{Family: "runtime", Found: true}},
		Runtime: &chstore.ServiceRuntime{Language: "java", RuntimeVersion: "21.0.1+12", Host: "app-01", OS: "linux"},
	})
	if !strings.Contains(sb.String(), "Çalışma zamanı: java 21.0.1+12, host app-01, os linux") {
		t.Fatalf("runtime satırı yok:\n%s", sb.String())
	}

	// GetServiceRuntime satır yokken boş-alanlı non-nil döner — satır
	// hiç basılmamalı (boş "Çalışma zamanı:" başlığı model için gürültü).
	var sb2 strings.Builder
	renderDeepEvidence(&sb2, chstore.DeepEvidence{
		Checked: []chstore.CheckedSignal{{Family: "runtime", Found: false}},
		Runtime: &chstore.ServiceRuntime{Service: "svc"},
	})
	if strings.Contains(sb2.String(), "Çalışma zamanı") {
		t.Fatalf("boş runtime satır basmamalı:\n%s", sb2.String())
	}
}
