package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/copilot"
)

// alert_noise_explain.go — F3.3 alert-storm anlatıcısı (v0.9.1080).
//
// /alerts'teki Noisy-rules paneli rakamları ve deterministik önerileri
// zaten üretiyor (v0.5.131 + deriveSuggestion); eksik olan tek bakışta
// "bu gürültünün hikâyesi ne, önce hangi vidayı sıkayım" cümlesiydi.
// ✨ düğmesi HAZIR kanıt paketini anlatır — model yeniden inceleme
// YAPMAZ (shift emsali v0.9.1071). Ton "sustur" değil "ayarla":
// öneriler zaten panelde tek tıkla uygulanabiliyor.

// alertNoiseWindows — izinli pencereler. Serbest pencere yok: sorgu
// maliyeti sınırlı kalır ve anlatım tuning panelinin 24h'lik raporuyla
// aynı dille konuşur (v0.8.270 rung disiplini).
var alertNoiseWindows = map[time.Duration]string{
	6 * time.Hour:      "6 saat",
	24 * time.Hour:     "24 saat",
	7 * 24 * time.Hour: "7 gün",
}

// alertNoiseWindow — rung çözümü (saf). Bilinmeyen → 24h.
func alertNoiseWindow(raw string) (time.Duration, string) {
	d := parseDuration(raw, 24*time.Hour)
	if label, ok := alertNoiseWindows[d]; ok {
		return d, label
	}
	return 24 * time.Hour, alertNoiseWindows[24*time.Hour]
}

// explainAlertNoise — POST /api/copilot/explain-alert-noise?since=24h.
// Admin-only: tuning verisiyle aynı kapı (alertTuningNoisyRules).
func (s *Server) explainAlertNoise(w http.ResponseWriter, r *http.Request) {
	// 503 ön-kapısı ROUTE'ta: requireCopilot (ai_routes.go, v0.9.1118).
	claims := auth.FromContext(r.Context())
	if claims == nil || claims.Role != auth.RoleAdmin {
		http.Error(w, "admin only", http.StatusForbidden)
		return
	}
	since, label := alertNoiseWindow(r.URL.Query().Get("since"))
	to := time.Now()
	from := to.Add(-since)
	rules, err := s.noisyRulesWithSuggestions(r.Context(), from, to, 10)
	if err != nil {
		writeErr(w, fmt.Errorf("noisy rules: %w", err))
		return
	}
	// Bildirim dökümü soft-fail: kural tarafı tek başına da anlatılabilir;
	// nil logs kanıtta "okunamadı" olarak İTİRAF edilir, 0 sanılmaz.
	logs, lerr := s.store.ListNotificationLog(r.Context(), from, to, "", alertNoiseLogCap, 0)
	if lerr != nil {
		logs = nil
	}
	evidence := renderAlertNoiseEvidence(label, rules, logs, lerr == nil)
	r, xid := withExchange(r)
	out, err := s.copilotExplain(r, copilot.SystemPromptAlertNoise(), evidence)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"explanation": out, "exchangeId": xid, "window": label})
}

// alertNoiseLogCap — bildirim dökümü okuma tavanı. Kanıtta tavana
// dayanınca "en az N" diye İFŞA edilir (no-silent-caps).
const alertNoiseLogCap = 1000

// renderAlertNoiseEvidence — kanıt paketini Türkçe düz metne çevirir
// (saf, tablo testli). Model yalnız buradaki rakamları kullanır.
func renderAlertNoiseEvidence(windowLabel string, rules []NoisyRuleWithSuggestion, logs []chstore.NotificationLog, logsRead bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Pencere: son %s.\n", windowLabel)

	switch {
	case !logsRead:
		b.WriteString("Bildirim dökümü okunamadı — hacim bilinmiyor (sıfır DEĞİL).\n")
	case len(logs) == 0:
		b.WriteString("Pencerede hiç bildirim gönderilmemiş.\n")
	default:
		byKind := map[string]int{}
		failed := 0
		for _, l := range logs {
			byKind[l.ChannelKind]++
			if !l.OK {
				failed++
			}
		}
		kinds := make([]string, 0, len(byKind))
		for k := range byKind {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		parts := make([]string, 0, len(kinds))
		for _, k := range kinds {
			parts = append(parts, fmt.Sprintf("%s %d", k, byKind[k]))
		}
		count := fmt.Sprintf("%d", len(logs))
		if len(logs) == alertNoiseLogCap {
			count = fmt.Sprintf("en az %d (okuma tavanı)", alertNoiseLogCap)
		}
		fmt.Fprintf(&b, "Bildirim hacmi: %s gönderim (%s)", count, strings.Join(parts, ", "))
		if failed > 0 {
			fmt.Fprintf(&b, "; %d gönderim BAŞARISIZ", failed)
		}
		b.WriteString(".\n")
	}

	if len(rules) == 0 {
		b.WriteString("Pencerede problem açan kural yok.\n")
		return b.String()
	}
	b.WriteString("En gürültülü kurallar (problem açılışına göre):\n")
	for i, r := range rules {
		fmt.Fprintf(&b, "%d. %q (%s) — %d açılış, medyan süre %.0fs; mevcut ayarlar: for=%ds min_samples=%d cooldown=%ds",
			i+1, r.RuleName, r.Severity, r.OpenCount, r.MedianDurSec,
			r.CurrentFor, r.CurrentMin, r.CurrentCD)
		if r.Suggestion != "" {
			fmt.Fprintf(&b, "; deterministik öneri: %s", r.Suggestion)
		}
		b.WriteString("\n")
	}
	return b.String()
}
