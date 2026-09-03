package api

import (
	"os"
	"strings"
	"testing"
)

// v0.10.331 — create/update hedef doğrulamasından geçer; Upsert hatası 409
// eşlemesinden geçer (kaynak pini; saf kural chstore'da test edilir).
func TestAlertRuleHandlersAcceptTarget(t *testing.T) {
	b, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if strings.Count(src, "if !s.acceptRuleTarget(w, rule) {") != 2 {
		t.Error("create ve update hedef doğrulamasından geçmeli")
	}
	if strings.Count(src, "s.writeRuleErr(w, err)") != 2 {
		t.Error("create ve update Upsert hatasını writeRuleErr ile yazmalı")
	}
}
