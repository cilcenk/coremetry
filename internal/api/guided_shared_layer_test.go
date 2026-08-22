package api

// guided_shared_layer_test.go — v0.9.1147 (AI Faz 3.4, D6).
//
// Faz 3.4 dört guided kanıt paketinin OKUMASINI mcptools'un ortak
// katmanına taşıdı (aynı okumanın iki kopyası kalmasın). Taşımanın tek
// gerçek riski GUIDED CEVAP KALİTESİ: bloklar kalibre edilmiş metinler
// ve ortak katmana geçerken bir alan/sıra kayması cevabı sessizce
// bozabilir. Bu dosya iki katmanı pinliyor:
//
//	(a) METİN BAYT-PİNİ — renderer'lar saf, beklenen çıktı harfi harfine
//	    yazılı (v0.9.416/420/376'daki hâlleriyle). Kasıtlı iki ekleme
//	    (store tavanı uyarıları) ayrı testlerde ve YALNIZ tavan
//	    dolduğunda yanıyor.
//	(b) BAĞLANMA MUTASYON PİNİ — bundle'lar gerçekten ortak katmandan
//	    besleniyor mu; api'de eski okumadan KOPYA kalmadı mı. Saf test
//	    tek başına bağlanma kanıtı değil (bu depoda yanan ders).

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/mcptools"
)

// ── (a) metin bayt-pinleri ─────────────────────────────────────

func TestRenderDBHealthEvidenceTRByteShape(t *testing.T) {
	data := mcptools.DBHealthData{
		Rows: []mcptools.DBHealthRow{
			{System: "oracle", Instance: "ora-prod", DBName: "ORCL", Calls: 1200, ErrorRatePct: 1.234, AvgMs: 12.34, P95Ms: 88.76},
			{System: "postgresql", Instance: "pg-1", DBName: "default", Calls: 300, ErrorRatePct: 0, AvgMs: 3.1, P95Ms: 9.4},
		},
		Total:     12,
		Truncated: true,
	}
	got, src, err := renderDBHealthEvidenceTR(data, "", 3600)
	if err != nil {
		t.Fatalf("hata: %v", err)
	}
	want := "Veritabanı kırılımı — son 1sa, filo geneli.\n" +
		"(12 instance'tan en yoğun 2'u)\n" +
		"- oracle/ora-prod·ORCL · 1200 çağrı · hata %1.23 · avg 12.3ms · p95 88.8ms\n" +
		"- postgresql/pg-1 · 300 çağrı · hata %0.00 · avg 3.1ms · p95 9.4ms\n" +
		"p95'e göre en yavaşlar: oracle/ora-prod (88.8ms), postgresql/pg-1 (9.4ms)\n"
	if got != want {
		t.Errorf("kanıt metni değişti:\n--- gelen ---\n%s\n--- beklenen ---\n%s", got, want)
	}
	// 'default' db adı satır kimliğine GİRMEZ (OTel db.name yokluğunun
	// yedeği; adı basmak "ORCL gibi gerçek bir şema" izlenimi verir).
	if strings.Contains(got, "pg-1·default") {
		t.Error("'default' db adı basılmış")
	}
	if src != "db_summary_5m MV (12 instance, 1sa)" {
		t.Errorf("kaynak dizesi değişti: %q", src)
	}
}

func TestRenderDBHealthEvidenceTREmptyAndServiceNote(t *testing.T) {
	got, src, _ := renderDBHealthEvidenceTR(mcptools.DBHealthData{}, "checkout-service", 1800)
	want := "Veritabanı kırılımı — son 30dk, filo geneli.\n" +
		"Not: soru checkout-service servisini anıyor; bu kırılım TÜM çağıranları kapsar — servis-özel detay Databases sayfasında.\n" +
		"Pencerede DB çağrısı görülmedi (db.system'li span yok).\n"
	if got != want {
		t.Errorf("boş/servisli metin değişti:\n--- gelen ---\n%s\n--- beklenen ---\n%s", got, want)
	}
	if src != "db_summary_5m (boş)" {
		t.Errorf("boş kaynak dizesi: %q", src)
	}
}

// Kasıtlı EKLEME: store tavanı dolduğunda alt-sınır uyarısı. Eski hâl
// 5000/200'lük tavanı "tam bu kadar var" gibi gösteriyordu (v0.9.813'ün
// ısırdığı sınıf) ve bayrak zaten elimizdeydi.
func TestRenderDepEvidenceStoreCappedNoteOnlyWhenCapped(t *testing.T) {
	rows := []mcptools.DBHealthRow{{System: "redis", Instance: "r1", Calls: 5}}
	plain, _, _ := renderDBHealthEvidenceTR(mcptools.DBHealthData{Rows: rows, Total: 1}, "", 3600)
	if strings.Contains(plain, "tavana") {
		t.Error("tavan dolmadan uyarı basılmış — sık yol bayt-bayt eski kalmalı")
	}
	capped, _, _ := renderDBHealthEvidenceTR(mcptools.DBHealthData{
		Rows: rows, Total: 5000, StoreCapped: true, StoreRowLimit: 5000,
	}, "", 3600)
	if !strings.Contains(capped, "Not: DB satır okuması tavana (5000) dayandı — liste EKSİK olabilir, sayılar alt sınırdır.\n") {
		t.Errorf("tavan uyarısı yok:\n%s", capped)
	}
	msgPlain, _, _ := renderMessagingEvidenceTR(mcptools.MessagingHealthData{
		Rows: []mcptools.MessagingHealthRow{{System: "kafka", Cluster: "(default)", Destination: "t", Calls: 5}}, Total: 1,
	}, "", 3600)
	if strings.Contains(msgPlain, "tavana") {
		t.Error("messaging: tavan dolmadan uyarı basılmış")
	}
	msgCapped, _, _ := renderMessagingEvidenceTR(mcptools.MessagingHealthData{
		Rows:  []mcptools.MessagingHealthRow{{System: "kafka", Cluster: "(default)", Destination: "t", Calls: 5}},
		Total: 200, StoreCapped: true, StoreRowLimit: 200,
	}, "", 3600)
	if !strings.Contains(msgCapped, "Not: destination okuması tavana (200) dayandı") {
		t.Errorf("messaging tavan uyarısı yok:\n%s", msgCapped)
	}
}

func TestRenderMessagingEvidenceTRByteShape(t *testing.T) {
	data := mcptools.MessagingHealthData{
		Rows: []mcptools.MessagingHealthRow{
			{System: "kafka", Cluster: "kafka-mobile", Destination: "orders", Calls: 900, ErrorRatePct: 0.5, AvgMs: 4.44, P95Ms: 120.05},
		},
		Total: 1,
	}
	got, src, err := renderMessagingEvidenceTR(data, "", 43200)
	if err != nil {
		t.Fatalf("hata: %v", err)
	}
	want := "Mesajlaşma kırılımı — son 12sa, filo geneli.\n" +
		"- kafka/kafka-mobile · orders · 900 mesaj-span · hata %0.50 · avg 4.4ms · p95 120.0ms\n" +
		"Not: consumer-lag doğrudan ölçülmez (broker metriği ingest edilmiyor) — yüksek avg/p95 consumer tarafındaki işleme süresidir; lag şüphesinde Messaging sayfasındaki trend'e bak.\n"
	if got != want {
		t.Errorf("kanıt metni değişti:\n--- gelen ---\n%s\n--- beklenen ---\n%s", got, want)
	}
	if src != "messaging MV (1 destination, 12sa)" {
		t.Errorf("kaynak dizesi değişti: %q", src)
	}
	// Lag notu HER satırlı gövdede: modelin en olası halüsinasyonu bir lag
	// sayısı uydurmak (broker metrikleri ingest edilmiyor).
	empty, _, _ := renderMessagingEvidenceTR(mcptools.MessagingHealthData{}, "", 43200)
	if !strings.Contains(empty, "Pencerede messaging çağrısı görülmedi (messaging.system'li span yok).\n") {
		t.Errorf("boş metin değişti:\n%s", empty)
	}
}

func TestRenderPodHealthEvidenceTRFleetMode(t *testing.T) {
	data := mcptools.PodHealthData{
		Heap: []mcptools.PodHeapRow{
			{Service: "billing", Pod: "billing-7c9", HeapPct: 92.6},
			{Service: "checkout", Pod: "checkout-1a2", HeapPct: 41.2},
		},
		HeapTotal:     5,
		HeapTruncated: true,
		HeapWindowS:   600,
	}
	got, src, err := renderPodHealthEvidenceTR(data)
	if err != nil {
		t.Fatalf("hata: %v", err)
	}
	want := "Filo-geneli JVM heap doluluğu (son 10 dk ortalaması, 5 pod), en dolu önce:\n" +
		"- billing / billing-7c9: heap %93\n" +
		"- checkout / checkout-1a2: heap %41\n" +
		"… ve 3 pod daha (hepsi daha az dolu).\n" +
		"Not: restart sayıları ve pod fazı kube-state-metrics/Thanos ister — servis sayfasının Pods sekmesinde.\n"
	if got != want {
		t.Errorf("filo metni değişti:\n--- gelen ---\n%s\n--- beklenen ---\n%s", got, want)
	}
	if src != "filo JVM heap doluluğu (OTel runtime, canlı)" {
		t.Errorf("kaynak dizesi: %q", src)
	}
	empty, _, _ := renderPodHealthEvidenceTR(mcptools.PodHealthData{HeapWindowS: 600})
	if !strings.HasPrefix(empty, "Filoda JVM heap metriği (jvm.memory.used/limit, OTel runtime) yayınlayan pod yok — JVM olmayan servislerde bu normaldir.\n") {
		t.Errorf("boş filo metni değişti:\n%s", empty)
	}
}

func TestRenderPodHealthEvidenceTRServiceMode(t *testing.T) {
	data := mcptools.PodHealthData{
		Service: "checkout-service",
		Instances: []mcptools.PodInstanceRow{
			{ID: "pod-down", Up: false, CPUPct: 0, MemBytes: 0},
			{ID: "pod-hot", Up: true, CPUPct: 87.4, MemBytes: 1_500_000_000},
		},
		InstanceTotal: 2,
		UpCount:       1,
		Heap: []mcptools.PodHeapRow{
			{Service: "checkout-service", Pod: "pod-hot", HeapPct: 88.9, UsedBytes: 889_000_000, LimitBytes: 1_000_000_000},
		},
		HeapTotal:   1,
		HeapWindowS: 600,
	}
	got, src, err := renderPodHealthEvidenceTR(data)
	if err != nil {
		t.Fatalf("hata: %v", err)
	}
	want := "checkout-service pod/instance envanteri (pencere içinde metrik yayınlayanlar): 2 pod, 1 canlı (2dk tazelik).\n" +
		"- pod-down: SESSİZ (örnek yok — düşmüş ya da drene olmuş olabilir), cpu %0, bellek 0MB\n" +
		"- pod-hot: CANLI, cpu %87, bellek 1500MB\n" +
		"JVM heap doluluğu (son 10 dk ortalaması):\n" +
		"- pod-hot: heap %89 (889MB / 1000MB -Xmx)\n" +
		"Not: restart sayıları ve pod fazı kube-state-metrics/Thanos ister — servis sayfasının Pods sekmesinde.\n"
	if got != want {
		t.Errorf("servis metni değişti:\n--- gelen ---\n%s\n--- beklenen ---\n%s", got, want)
	}
	if src != "checkout-service pod envanteri + JVM heap (OTel runtime, canlı)" {
		t.Errorf("kaynak dizesi: %q", src)
	}
}

// Heap okuması DÜŞERSE envanter tek başına cevaptır ve heap hakkında
// HİÇBİR cümle basılmaz — "yayınlamıyor" demek yanlış olurdu (okuma
// başarısız, metrik yok değil).
func TestRenderPodHealthEvidenceTRHeapUnavailableStaysSilent(t *testing.T) {
	got, _, _ := renderPodHealthEvidenceTR(mcptools.PodHealthData{
		Service:         "checkout-service",
		Instances:       []mcptools.PodInstanceRow{{ID: "p1", Up: true}},
		InstanceTotal:   1,
		UpCount:         1,
		HeapUnavailable: true,
		HeapWindowS:     600,
	})
	if strings.Contains(got, "JVM heap") && !strings.Contains(got, "kube-state-metrics") {
		t.Errorf("heap okunamadığında heap cümlesi basılmış:\n%s", got)
	}
	if strings.Contains(got, "yayınlamıyor") {
		t.Errorf("okuma hatası 'metrik yok' diye çevrilmiş:\n%s", got)
	}
	if !strings.Contains(got, "- p1: CANLI") {
		t.Errorf("envanter kaybolmuş:\n%s", got)
	}
	// Servis modunda pod yoksa eski cümle aynen kalmalı.
	none, _, _ := renderPodHealthEvidenceTR(mcptools.PodHealthData{Service: "x", HeapWindowS: 600})
	if !strings.Contains(none, "Bu pencerede metrik yayınlayan pod yok.\n") {
		t.Errorf("boş envanter cümlesi değişti:\n%s", none)
	}
	if !strings.Contains(none, "Bu servis JVM heap metriği yayınlamıyor (JVM değilse normal).\n") {
		t.Errorf("heap yokluğu cümlesi değişti:\n%s", none)
	}
}

func TestRenderShiftProblemsTRByteShape(t *testing.T) {
	// to = referans an; satırlar "N önce" olarak yazılıyor.
	to := time.Unix(10_000, 0)
	data := mcptools.ProblemWindowData{
		Rows: []mcptools.ProblemWindowRow{
			{Priority: "P1", Severity: "critical", Service: "checkout", RuleName: "error rate",
				Status: "open", StartedAtNs: time.Unix(9_400, 0).UnixNano()},
			{Priority: "P2", Severity: "warning", Service: "billing", RuleName: "latency",
				Status: "resolved", StartedAtNs: time.Unix(6_400, 0).UnixNano(),
				ResolvedAtNs: time.Unix(9_100, 0).UnixNano()},
		},
		Total: 2, Opened: 2, Resolved: 1, StillOpen: 1,
	}
	want := "\nPROBLEMLER: 2 açıldı, 1 çözüldü, 1 hâlâ açık.\n" +
		"- [P1/critical] checkout · error rate · open (açılış: 10dk önce)\n" +
		"- [P2/warning] billing · latency · çözüldü 15dk önce (açılış: 1sa önce)\n"
	if got := renderShiftProblemsTR(data, to); got != want {
		t.Errorf("vardiya problem bloğu değişti:\n--- gelen ---\n%s\n--- beklenen ---\n%s", got, want)
	}
	// Sıfır olay: yalnız sayaç satırı (eski davranış — satır bloğu hiç açılmaz).
	if got := renderShiftProblemsTR(mcptools.ProblemWindowData{}, to); got != "\nPROBLEMLER: 0 açıldı, 0 çözüldü, 0 hâlâ açık.\n" {
		t.Errorf("boş blok değişti: %q", got)
	}
}

func TestRenderShiftProblemsTRTruncationAndCap(t *testing.T) {
	to := time.Unix(10_000, 0)
	rows := make([]mcptools.ProblemWindowRow, 12)
	for i := range rows {
		rows[i] = mcptools.ProblemWindowRow{Priority: "P3", Severity: "info", Service: "s", RuleName: "r",
			Status: "open", StartedAtNs: time.Unix(9_000, 0).UnixNano()}
	}
	got := renderShiftProblemsTR(mcptools.ProblemWindowData{Rows: rows, Total: 40, Truncated: true, Opened: 40}, to)
	if !strings.Contains(got, "(en yeni 12 satır gösteriliyor, toplam 40)\n") {
		t.Errorf("kesme satırı değişti:\n%s", got)
	}
	// Kasıtlı EKLEME: 500'lük store tavanında sayaçlar ALT SINIR.
	capped := renderShiftProblemsTR(mcptools.ProblemWindowData{
		Rows: rows[:1], Total: 500, StoreCapped: true, StoreRowLimit: 500, Opened: 500,
	}, to)
	if !strings.Contains(capped, "(okuma 500 satır tavanına dayandı — sayılar ALT SINIR)\n") {
		t.Errorf("tavan uyarısı yok:\n%s", capped)
	}
	if strings.Contains(got, "ALT SINIR") {
		t.Error("tavan dolmadan alt-sınır uyarısı basılmış")
	}
}

// ── (b) bağlanma mutasyon pinleri ──────────────────────────────

// Bundle'lar ORTAK KATMANDAN beslenmeli. Eski okumanın api'de kopyası
// kalırsa iki yüzey yeniden ayrışır (D6'nın kendisi) ve hiçbir saf test
// bunu görmez.
func TestGuidedBundlesReadThroughSharedLayer(t *testing.T) {
	cases := []struct {
		file string
		// wantCall — ortak katman çağrısı.
		wantCall string
		// forbidden — api'de KALMAMASI gereken doğrudan store okumaları.
		forbidden []string
	}{
		{
			file:      "copilot_deps.go",
			wantCall:  "mcptools.ReadDBHealth(ctx, s.mcpDeps(), from, to, guidedDepRows)",
			forbidden: []string{"s.store.GetDatabases", "s.store.GetDatabasesRollup"},
		},
		{
			file:      "copilot_deps.go",
			wantCall:  "mcptools.ReadMessagingHealth(ctx, s.mcpDeps(), from, to, guidedDepRows)",
			forbidden: []string{"s.store.GetMessaging("},
		},
		{
			file:      "copilot_pods.go",
			wantCall:  "mcptools.ReadPodHealth(ctx, s.mcpDeps(), service, from, to, guidedPodRows, heapRows)",
			forbidden: []string{"s.store.JVMHeapPodUsage", "s.store.ServiceInstances"},
		},
		{
			file:      "copilot_shift.go",
			wantCall:  "mcptools.ReadProblemWindowEvents(ctx, s.mcpDeps(), service, from, to, guidedShiftProblemRows)",
			forbidden: []string{"s.store.ListProblemWindowEvents", "s.enrichProblemsForRead"},
		},
	}
	for _, c := range cases {
		t.Run(c.file+"/"+strings.SplitN(c.wantCall, "(", 2)[0], func(t *testing.T) {
			src := guidedSharedSource(t, c.file)
			if !strings.Contains(src, c.wantCall) {
				t.Errorf("ortak katman çağrısı yok — bekleniyor: %s", c.wantCall)
			}
			for _, f := range c.forbidden {
				if strings.Contains(src, f) {
					t.Errorf("api'de doğrudan store okuması KALMIŞ (%s) — ortak katmanın ikinci kopyası, D6 geri gelir", f)
				}
			}
		})
	}
	// Pod bundle'ı copilot_guided.go'dan TAŞINDI; orada kopya kalmamalı.
	guided := guidedSharedSource(t, "copilot_guided.go")
	if strings.Contains(guided, "func (s *Server) guidedPodHealthBundle(") {
		t.Error("guidedPodHealthBundle hâlâ copilot_guided.go'da — taşıma kopya bıraktı")
	}
}

// Pod bundle'ının HEAP tavanı moda göre değişmeli (filo 10, servis 12) —
// eski iki döngünün tavanları. Tek sabite düşürmek kanıt bloğunun
// uzunluğunu sessizce değiştirirdi.
func TestGuidedPodBundleKeepsPerModeHeapCaps(t *testing.T) {
	src := guidedSharedSource(t, "copilot_pods.go")
	if !strings.Contains(src, "heapRows := guidedPodRows") || !strings.Contains(src, "heapRows = guidedFleetHeapRows") {
		t.Error("mod başına heap tavanı kaybolmuş (filo 10 / servis 12)")
	}
	// Adım çipi SIRASI: heap adımı önce, servis envanteri sonra — eski
	// emit sırası bu ve UI çipleri sırayla çiziyor.
	iHeap := strings.Index(src, `emitGuidedStep(emit, "jvm_heap_usage"`)
	iInst := strings.Index(src, `emitGuidedStep(emit, "service_instances"`)
	if iHeap < 0 || iInst < 0 {
		t.Fatal("adım çipleri kaybolmuş")
	}
	if iHeap > iInst {
		t.Error("adım çipi sırası ters — UI çipleri emit sırasına göre çiziliyor")
	}
}

func guidedSharedSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("%s okunamadı: %v", name, err)
	}
	// Yorumları AYIKLA: gerekçe yorumları aranan dizeleri (eski çağrı
	// adlarını) içeriyor ve ayıklamayan bir tarayıcı bu depoda KÖR koştu.
	var sb strings.Builder
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	return sb.String()
}

// TAKIM SEAM'İ (v0.9.1244) — guided'ın takım bundle'ı ortak katmandan
// beslenmeli. list_teams / get_team_services tool'ları AYNI çözümlemeyi
// ve AYNI okumayı kullanıyor; api'de ikinci bir kopya belirirse takım
// cevabı iki yüzeyde sessizce ayrışır (v0.9.553 sınıfı) — "MCP takımın
// 100 servisini saydı, guided 80'ini" gibi.
func TestGuidedTeamPathUsesSharedLayer(t *testing.T) {
	src := guidedSharedSource(t, "copilot_guided.go")
	i := strings.Index(src, "func (s *Server) guidedTeamServicesBundle(")
	if i < 0 {
		t.Fatal("guidedTeamServicesBundle bulunamadı — taşındıysa bu pini de taşı")
	}
	body := src[i:]
	if j := strings.Index(body[1:], "\nfunc "); j >= 0 {
		body = body[:j]
	}
	for _, want := range []string{
		"mcptools.ReadTeamServiceNames(",
		"mcptools.ReadTeamServicesRED(",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("guided takım bundle'ı ortak katmandan beslenmiyor — bekleniyor: %s", want)
		}
	}
	if strings.Contains(body, "GetServicesAggFilteredIn(") {
		t.Error("guided takım bundle'ı store'u DOĞRUDAN okuyor — okumanın ikinci kopyası geri geldi")
	}
	if strings.Contains(body, "ListServiceMetadata(") {
		t.Error("guided takım bundle'ı çözümlemeyi kendi yapıyor — tavan/alias kuralı ikiye bölündü")
	}
	// Katalog tarafı da aynı okumadan: "hangi takım?" çipleri ile
	// list_teams aynı takımları, aynı sırayla göstermeli.
	if !strings.Contains(src, "mcptools.ReadTeamCatalogue(") {
		t.Error("guidedTeamNames ortak katalog okumasını kullanmıyor")
	}
	// Tavan TEK yazımda: api kendi 100'ünü tanımlarsa iki sayı ayrışır.
	if !strings.Contains(src, "mcptools.MaxTeamServices") {
		t.Error("maxTeamServices ortak sabitten türemiyor")
	}
}
