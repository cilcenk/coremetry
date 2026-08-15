package chstore

import (
	"os"
	"strings"
	"testing"
)

// v0.9.1063 (Faz 2.2 / K3) regresyon pini — BubbleUp iki pencere alır ve
// İKİ taraf da kendi zaman predicate'iyle sayılır. Eski tek-pencere
// biçiminde base tarafı çıplak count() idi; pencereler ayrışınca çıplak
// count() birleşim penceresini sayar ve baseline'ı şişirirdi. Bu pin
// SQL gövdesinde (1) her iki tarafın countIf olduğunu, (2) çıplak
// count()'un geri dönmediğini, (3) selection-side WHERE'in temiz
// kurulduğunu (eski args-tohumlama tuhaflığı: wcSel.args'a wcBase.args
// önden ekleniyordu — placeholder/arg hizası ancak tesadüfen tutar)
// zorlar.
func TestBubbleUpWindowedSQLShape(t *testing.T) {
	src, err := os.ReadFile("bubbleup.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if !strings.Contains(s, "countIf(%s) AS sel_total") ||
		!strings.Contains(s, "countIf(%s) AS base_total") {
		t.Fatal("totals iki taraflı countIf değil — tek-pencere semantiği geri dönmüş")
	}
	if strings.Contains(s, "count() AS base") {
		t.Fatal("çıplak count() base tarafına geri dönmüş — ayrık pencerede birleşimi sayar")
	}
	if strings.Contains(s, "wcSel.args = append(wcSel.args, wcBase.args") {
		t.Fatal("wcSel args-tohumlama tuhaflığı geri dönmüş")
	}
	if !strings.Contains(s, "baseFrom, baseTo, selFrom, selTo time.Time") {
		t.Fatal("BubbleUp imzası iki-pencere değil")
	}
}
