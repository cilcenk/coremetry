// v0.9.571 regresyon testi — incident satırında durum rozeti TEK kez.
//
// Operator-reported: "open open iki defa yazan kayıtlar var."
//
// Başlık satırı v0.9.255'ten beri paylaşılan StatusBadge basıyor ve
// incidentToInbox `Status: inc.Status` set ediyor. DetailLine'ın
// incident dalı ise KENDİ rozetini basmaya devam ediyordu — aynı durum
// iki kez, üstelik iki FARKLI tonda (başlıkta amber, detayda kırmızı),
// sanki iki ayrı şey söylüyorlarmış gibi.
//
// Bu testin işi ikisinin AYNI kaynaktan beslendiğini sabitlemek: eğer
// incidentToInbox bir gün Status'ü set etmeyi bırakırsa, başlık rozeti
// sessizce kaybolur ve detaydakini de sildiğimiz için durum HİÇ
// görünmez olur.
package api

import (
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestIncidentInboxItemCarriesStatus(t *testing.T) {
	for _, st := range []string{"open", "acknowledged", "resolved"} {
		it := incidentToInbox(chstore.Incident{ID: "i1", Status: st, Severity: "critical"})
		if it.Status != st {
			t.Errorf("incidentToInbox Status=%q, beklenen %q — paylaşılan başlık "+
				"rozeti bu alandan besleniyor; boş kalırsa durum HİÇ görünmez",
				it.Status, st)
		}
		if it.Incident == nil || it.Incident.Status != st {
			t.Errorf("Incident.Status=%v, beklenen %q", it.Incident, st)
		}
	}
}

func TestInboxIncidentDetailHasNoSecondStatusBadge(t *testing.T) {
	b, err := os.ReadFile("../../frontend/src/pages/Inbox.tsx")
	if err != nil {
		t.Skipf("Inbox.tsx okunamadı: %v", err)
	}
	src := string(b)

	i := strings.Index(src, "if (it.kind === 'incident' && it.incident) {")
	if i < 0 {
		t.Fatal("incident DetailLine dalı bulunamadı")
	}
	body := src[i:]
	if end := strings.Index(body, "\n  if ("); end > 0 {
		body = body[:end]
	}
	// Yorumlar hariç: düzeltmenin kendi açıklaması eski kodu anlatıyor
	// (aynı tuzak v0.9.564'te çıktı).
	var code []string
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "//") || strings.HasPrefix(t, "*") ||
			strings.HasPrefix(t, "{/*") || strings.HasPrefix(t, "/*") {
			continue
		}
		code = append(code, line)
	}
	joined := strings.Join(code, "\n")

	if strings.Contains(joined, "it.incident.status") {
		t.Error("incident detay satırı KENDİ durum rozetini basıyor — başlıkta " +
			"zaten paylaşılan StatusBadge var, aynı durum iki kez ve iki farklı " +
			"tonda çizilir (\"open open\")")
	}
}
