// Davranış motoru AŞAMA 1 — WIRING testleri (v0.9.936).
//
// Saf çekirdek behavior_test.go'da doğrulanıyor; burada doğrulanan şey
// çekirdeğin GERÇEKTEN BAĞLI olduğu. Bu depoda en pahalı sessiz bozulma
// biçimi, çalışan bir mantığın hiç çağrılmaması (v0.9.827: metrik
// dedektörünün incident bağlaması Settings'teki hiçbir vidaya
// bağlanmıyordu ve ayar sayfası aylarca yalan söyledi).
package anomaly

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// TestScanCallsBehavior — dedektörün tik gövdesi davranış taramasını
// ÇAĞIRIYOR olmalı. Kaynak-düzeyi pin: çağrı silinirse motor sessizce
// ölür, hiçbir derleme/test hatası vermez ve /anomalies'te eksilen
// satırları kimse fark etmez.
func TestScanCallsBehavior(t *testing.T) {
	src, err := os.ReadFile("anomaly.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	i := strings.Index(body, "func (d *Detector) scan(")
	if i < 0 {
		t.Fatal("scan() bulunamadı — dosya yeniden düzenlenmişse bu pini güncelle")
	}
	// scan()'in gövdesi bir sonraki üst-düzey func'a kadar.
	rest := body[i:]
	if j := strings.Index(rest[1:], "\nfunc "); j > 0 {
		rest = rest[:j]
	}
	if !strings.Contains(rest, "d.scanBehavior(ctx, now, sens)") {
		t.Error("scan() davranış taramasını çağırmıyor — motor gemide ama ÖLÜ")
	}
}

// TestBehaviorSharesDetectorTickInputs — motor tik boyunca SABİT olan
// `now` ve `sens` ile çağrılmalı. Kendi time.Now()'ını alsaydı taramanın
// ilk ve son servisi farklı kovalara düşebilirdi (v0.8.507'nin
// düzelttiği tik-tutarsızlığı).
func TestBehaviorSharesDetectorTickInputs(t *testing.T) {
	src, err := os.ReadFile("behavior_scan.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	i := strings.Index(body, "func (d *Detector) scanBehavior(")
	if i < 0 {
		t.Fatal("scanBehavior bulunamadı")
	}
	sig := body[i : i+strings.Index(body[i:], "{")]
	if !strings.Contains(sig, "now time.Time") {
		t.Error("scanBehavior `now`'ı parametre olarak almıyor — kendi saatini " +
			"alırsa tarama kendi içinde tutarsız olur")
	}
	if !strings.Contains(sig, "cfg chstore.AnomalySensitivityConfig") {
		t.Error("scanBehavior ayarı parametre olarak almıyor — tik ortasında " +
			"değişen bir blob taramayı ikiye böler")
	}
}

// TestBehaviorEventMapping — adayın anomaly_events satırına dönüşümü.
// Bu eşleme mevcut terfi kapısının (evaluator.promoteStrongAnomalies)
// okuduğu alanları besliyor: yanlış alan = sessizce hiç terfi etmeyen
// ya da her tik terfi eden bir sinyal.
func TestBehaviorEventMapping(t *testing.T) {
	onset := int64(1_700_000_000)
	now := time.Unix(onset+1800, 0)
	c := behaviorCandidate{
		Service: "checkout", Metric: "p99_ms", Signal: "regime",
		Direction: "up", Ratio: 2.1, Z: 7.2,
		Baseline: 100, Current: 210, HOW: 34, Dwell: 6,
		OnsetUnix: onset, Spans: 18000,
	}
	ev := behaviorEvent(c, now)

	if ev.Kind != "behavior_change" {
		t.Errorf("Kind = %q, want behavior_change", ev.Kind)
	}
	if ev.ID != behaviorEventID("checkout", "p99_ms") {
		t.Error("ID fingerprint'ten gelmiyor — her tik yeni satır açılır")
	}
	if ev.Service != "checkout" {
		t.Errorf("Service = %q", ev.Service)
	}
	// started_at KAYMANIN BAŞLANGICI olmalı, tespit anı değil: terfi
	// kapısının MinSustainedSec hesabı buna bakıyor, tespit anı
	// yazılsaydı hiçbir davranış olayı "yeterince sürmüş" sayılmazdı.
	if ev.StartedAt != onset*int64(time.Second) {
		t.Errorf("StartedAt = %d, want %d (onset)", ev.StartedAt, onset*int64(time.Second))
	}
	if ev.LastSeen != now.UnixNano() {
		t.Errorf("LastSeen = %d, want %d (tespit anı)", ev.LastSeen, now.UnixNano())
	}
	// CurrentCount terfi kapısının hacim tabanı (MinCount, vars. 10).
	if ev.CurrentCount != 18000 {
		t.Errorf("CurrentCount = %d — terfi kapısının hacim tabanı beslenmiyor", ev.CurrentCount)
	}
	if ev.CurrentRatio != 2.1 {
		t.Errorf("CurrentRatio = %v", ev.CurrentRatio)
	}
	// PeakRatio ELLE SET EDİLMEMELİ: UpsertAnomalyEvent max(prev, current)
	// uyguluyor; burada da yazmak monotonluğu iki yerden yönetmek olurdu.
	if ev.PeakRatio != 0 {
		t.Errorf("PeakRatio elle set edilmiş (%v) — monotonluk Upsert'ün işi", ev.PeakRatio)
	}
	// Pattern insan-okunur; sample makine-okunur.
	if !strings.Contains(ev.Pattern, "davranış") {
		t.Errorf("Pattern insan-okunur değil: %q", ev.Pattern)
	}
	var d behaviorDetails
	if err := json.Unmarshal([]byte(ev.Sample), &d); err != nil {
		t.Fatalf("sample geçerli JSON değil: %v", err)
	}
	if d.Signal != "regime" || d.HourOfWeek != 34 {
		t.Errorf("ayrıntılar taşınmamış: %+v", d)
	}
}

// TestBehaviorEventStableAcrossSignals — mevsimsel başlayıp rejime
// dönüşen bir kayma AYNI satırı güncellemeli. Ayrı satır açsaydı
// operatör tek bir olayı iki kez görürdü ve started_at sıfırlanırdı.
func TestBehaviorEventStableAcrossSignals(t *testing.T) {
	now := time.Unix(1_700_003_600, 0)
	base := behaviorCandidate{Service: "checkout", Metric: "p99_ms", OnsetUnix: 1_700_000_000}
	seasonal, regime := base, base
	seasonal.Signal, regime.Signal = "seasonal", "regime"
	if behaviorEvent(seasonal, now).ID != behaviorEvent(regime, now).ID {
		t.Error("sinyal fingerprint'e girmiş — aynı olay iki satır olur")
	}
}

// TestBehaviorLogLine — operatör vidayı çevirdiğinde etkinin canlıya
// indiğini LOGTAN doğrulayabilmeli (çok-pod kurulumlarda "PUT hangi
// pod'a düştü" sorusunun tek cevabı bu satır). Kapalıyken de bunu
// açıkça söylemeli: sessiz bir motor ile kapalı bir motor logta aynı
// görünmemeli.
func TestBehaviorLogLine(t *testing.T) {
	on := behaviorLogLine(chstore.DefaultAnomalyBehavior())
	for _, want := range []string{"seasonalZ", "regimeRatio", "dwell", "tavan"} {
		if !strings.Contains(on, want) {
			t.Errorf("log satırında %q yok: %s", want, on)
		}
	}
	off := false
	if got := behaviorLogLine(chstore.AnomalyBehaviorConfig{Enabled: &off}); !strings.Contains(got, "KAPALI") {
		t.Errorf("kapalı motor logta açıkça söylenmiyor: %s", got)
	}
	// Hassasiyet özeti davranış ekini TAŞIMALI — ayrı satır olsaydı
	// ikinci bir "değişti mi" takibi gerekirdi.
	if !strings.Contains(sensitivityLogLine(chstore.DefaultAnomalySensitivity()), "davranış:") {
		t.Error("hassasiyet log satırı davranış ekini taşımıyor")
	}
}

// TestBehaviorObservabilityShape — self-obs anlık görüntüsü. Hiç
// koşmamışken SIFIR dönmeli ve LastError boş olmalı: "hiç koşmadı" ile
// "koştu ve patladı" ayırt edilebilmeli.
func TestBehaviorObservabilityShape(t *testing.T) {
	s := BehaviorObservability()
	if s.LastError != "" {
		t.Errorf("temiz süreçte LastError dolu: %q", s.LastError)
	}
	if s.LastUnix != 0 && s.Ticks == 0 {
		t.Error("hiç tik koşmadan LastUnix set edilmiş")
	}
}

// TestBehaviorDeploySuffix — deploy YOKLUĞU da bilgidir ve log satırı
// onu uydurmamalı ("deploy'suz kalıcı kayma" motorun kapsamında).
func TestBehaviorDeploySuffix(t *testing.T) {
	c := behaviorCandidate{OnsetUnix: 1_700_000_000}
	if got := behaviorDeploySuffix(c); got != "" {
		t.Errorf("deploy yokken ek yazılmış: %q", got)
	}
	c.Deploy = &chstore.RecentDeployEntry{Version: "v2", FirstSeenNs: (c.OnsetUnix - 600) * int64(time.Second)}
	got := behaviorDeploySuffix(c)
	if !strings.Contains(got, "v2") || !strings.Contains(got, "10 dk") {
		t.Errorf("deploy eki yanlış: %q", got)
	}
}
