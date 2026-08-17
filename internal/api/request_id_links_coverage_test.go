package api

import (
	"os"
	"strings"
	"testing"
)

// v0.9.1140 kaynak-pini — Operator-reported: log köprüsü kuruluyken
// drawer sohbetindeki request_id linksiz kalıyordu. v0.9.709 köprü
// çiplerini yalnız guided'a (sonra serbest döngüye) bağlamıştı; dört
// cevap kademesinin DÖRDÜ de answerRequestIDLinks'i çağırmak zorunda.
// Yeni bir kademe eklenirse bu listeye girer.
func TestAllAnswerTiersCarryRequestIDLinks(t *testing.T) {
	tiers := map[string]string{
		"copilot_guided.go": "answerRequestIDLinks(",
		"copilot_chat.go":   "answerRequestIDLinks(",
		"copilot_drawer.go": "answerRequestIDLinks(",
		"rag.go":            "answerRequestIDLinks(",
	}
	for f, needle := range tiers {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s okunamadı: %v", f, err)
		}
		if !strings.Contains(string(b), needle) {
			t.Errorf("%s cevabına request_id log-köprüsü çipleri bağlı değil (v0.9.709 hattı)", f)
		}
	}
}
