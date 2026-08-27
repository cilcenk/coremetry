package anomaly

// rootcause_nodecause_test.go — v0.10.94, dikey eksen dilim ②:
// appendNodeCauses'un davranışı + İKİ anchor yoluna da bağlılığı
// ([[feedback-fixes-have-second-halves]] — sözleşme iki yerde yaşıyor).

import (
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/correlator"
)

func TestAppendNodeCauses(t *testing.T) {
	in := evidenceInputs{
		runsOn: []chstore.RunsOnPlacement{
			{Service: "checkout", Node: "w1"},
			{Service: "payments", Node: "w1"},
		},
		openProblems: []chstore.Problem{{Service: "payments"}},
		// Anchor'ın kendi aktif anomalisi "alarmda komşu" DEĞİLDİR.
		events: []chstore.AnomalyEvent{{Service: "checkout", Status: "active"}},
	}
	var dst correlator.SynthesisInput
	appendNodeCauses(&dst, in, "checkout")
	if len(dst.Neighbours) != 1 || dst.Neighbours[0].Service != "node:w1" ||
		dst.Neighbours[0].Kind != chstore.NodeKindNode {
		t.Fatalf("node adayı eklenmedi: %+v", dst.Neighbours)
	}
	// Yerleşim yoksa dokunma.
	var empty correlator.SynthesisInput
	appendNodeCauses(&empty, evidenceInputs{}, "checkout")
	if len(empty.Neighbours) != 0 {
		t.Errorf("boş yerleşimde aday üredi: %+v", empty.Neighbours)
	}
}

// İki anchor yolu + fusion fetch — kaynak pinleri (saf çekirdek yeşilken
// çağrı yolu düşerse dikey eksen sessizce kaybolur).
func TestNodeCausesAreReachable(t *testing.T) {
	worker, err := os.ReadFile("rootcause_worker.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(worker), "appendNodeCauses("); n != 3 {
		// 1 tanım + 2 çağrı (anomali + problem anchor'ları).
		t.Errorf("appendNodeCauses %d kez geçiyor, 3 olmalı (tanım + iki anchor)", n)
	}
	fusion, err := os.ReadFile("fusion.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fusion), "GetRunsOnPlacements(ctx, evidenceWindow)") {
		t.Error("fusion yerleşimleri çekmiyor — adaylık girdisiz kalır")
	}
}
