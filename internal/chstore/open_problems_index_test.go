// v0.9.575 regresyon testi — açık problem snapshot'ının İKİ indeksi.
//
// Bug: snapshot her zaman (rule_id|service) ile anahtarlanıyordu, ama
// deterministik-ID üreten dedektörler problem ID'siyle arıyordu:
//
//	snap["runtime:jvm-gc:odeme-api:pod-abc"]    ← ID, "|" YOK
//	harita anahtarı: "runtime:jvm-gc|odeme-api"  ← rule|service
//
// İki uzay kesişmiyor → hasOpen DAİMA false. Sonuçları ağır ve sessiz:
// "aç" dalı her tik yeniden koşuyor (dakikada bir PROBLEM OPENED +
// BİLDİRİM), StartedAt sıfırlanıyor (yaş eskalasyonu ölü), histerezis
// devreye girmiyor, "kapat" dalı hiç çalışmıyor, acknowledged her tik
// open'a geri yazılıyor.
//
// Kök sebep v0.9.522: FindOpenProblemByID(ctx, xProblemID(...)) çağrıları
// snap[xProblemID(...)] yapıldı ama ANAHTAR ÇEVRİLMEDİ. Derledi, testler
// yeşil kaldı, dört dedektör sessizce bozuldu.
package chstore

import "testing"

func testSnapshot() *OpenProblems {
	o := &OpenProblems{byKey: map[string]*Problem{}, byID: map[string]*Problem{}}
	add := func(p Problem) {
		q := p
		reduceLatestProblem(o.byKey, &q)
		o.byID[q.ID] = &q
		o.all = append(o.all, &q)
	}
	// Aynı servisin İKİ pod'u: per-pod granülerlik ByKey'de KAYBOLUR
	// (reduceLatestProblem en yeniyi seçer), ByID'de korunur.
	add(Problem{ID: "runtime:jvm-gc:odeme-api:pod-a", RuleID: "runtime:jvm-gc",
		Service: "odeme-api", Pod: "pod-a", StartedAt: 100})
	add(Problem{ID: "runtime:jvm-gc:odeme-api:pod-b", RuleID: "runtime:jvm-gc",
		Service: "odeme-api", Pod: "pod-b", StartedAt: 200})
	// Servissiz, deterministik ID'li (paylaşılan patlama).
	add(Problem{ID: "shared-exc:java.sql.SQLRecoverableException:1754",
		RuleID: "exception:shared-dependency", Service: "", StartedAt: 300})
	return o
}

func TestOpenProblemsByIDFindsPerPodRows(t *testing.T) {
	o := testSnapshot()

	// ASIL BUG: bu arama eskiden HİÇ isabet etmiyordu.
	for _, id := range []string{
		"runtime:jvm-gc:odeme-api:pod-a",
		"runtime:jvm-gc:odeme-api:pod-b",
		"shared-exc:java.sql.SQLRecoverableException:1754",
	} {
		if got := o.ByID(id); got == nil {
			t.Errorf("ByID(%q) nil — dedektör 'açık problem yok' sanır ve HER TİK "+
				"yeniden açar: dakikada bir bildirim, sıfırlanan StartedAt, "+
				"çalışmayan histerezis, hiç kapanmayan problem", id)
		}
	}
}

func TestOpenProblemsByKeyKeepsRuleServiceLookup(t *testing.T) {
	o := testSnapshot()
	got := o.ByKey("runtime:jvm-gc", "odeme-api")
	if got == nil {
		t.Fatal("ByKey isabet etmedi — kural-bazlı denetimler (evaluateOne) bozulur")
	}
	// reduceLatestProblem sözleşmesi: en YENİ started_at kazanır.
	if got.StartedAt != 200 {
		t.Errorf("ByKey en yeniyi seçmedi: StartedAt=%d, beklenen 200", got.StartedAt)
	}
}

// İki indeksin AYRI sorulara cevap verdiğini sabitler. Birini diğerinin
// yerine kullanmak, düzeltilen hatanın tersini üretir: ByKey per-pod
// granülerliği taşıyamaz.
func TestOpenProblemsIndexesAnswerDifferentQuestions(t *testing.T) {
	o := testSnapshot()
	if o.Len() != 3 {
		t.Fatalf("toplam %d, beklenen 3", o.Len())
	}
	// ByKey iki pod'u TEK girdiye çöktürür — bu yüzden ID indeksi şart.
	if len(o.byKey) != 2 {
		t.Errorf("byKey %d girdi, beklenen 2 (iki pod tek anahtara çöker + servissiz olan)",
			len(o.byKey))
	}
	if len(o.byID) != 3 {
		t.Errorf("byID %d girdi, beklenen 3 (her satır ayrı)", len(o.byID))
	}
}

// All() her problemi TAM BİR KEZ vermeli. Tek map'e iki anahtar koymak
// dolaşan kodda (anomali resolve geçişi, emekli heap tahliyesi) çift
// sayım üretirdi — bir hatayı düzeltirken başka bir hata.
func TestOpenProblemsAllHasNoDuplicates(t *testing.T) {
	o := testSnapshot()
	seen := map[string]int{}
	for _, p := range o.All() {
		seen[p.ID]++
	}
	if len(seen) != 3 {
		t.Errorf("All() %d ayrı problem verdi, beklenen 3", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("All() %q problemini %d kez verdi — dolaşan kod çift sayar", id, n)
		}
	}
}

// nil alıcı güvenli: snapshot hatasında çağıranlar nil geçiyor ve
// "açık problem yok" davranışı korunmalı (panic değil).
func TestOpenProblemsNilSafe(t *testing.T) {
	var o *OpenProblems
	if o.ByID("x") != nil || o.ByKey("r", "s") != nil || o.All() != nil || o.Len() != 0 {
		t.Error("nil snapshot güvenli değil")
	}
}
