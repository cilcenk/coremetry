package notify

import (
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// exception_notifier_test.go — v0.9.437 pinleri. P1 formülü inbox.go
// exceptionPriority ile BİREBİR kalmalı (5dk + 500); sentetik Problem
// kanal şablonunun bastığı alanları taşımalı.

func TestIsP1ExceptionCandidate(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	mk := func(ageMin int64, occ uint64) chstore.ExceptionGroup {
		return chstore.ExceptionGroup{
			LastSeen:    now.Add(-time.Duration(ageMin) * time.Minute).UnixNano(),
			Occurrences: occ,
		}
	}
	cases := []struct {
		name string
		g    chstore.ExceptionGroup
		want bool
	}{
		{"P1: taze + yoğun", mk(2, 600), true},
		{"tam eşik: 5dk + 500", mk(5, 500), true},
		{"eşik altı occurrence", mk(2, 499), false},
		{"bayat", mk(6, 10000), false},
	}
	for _, c := range cases {
		if got := isP1ExceptionCandidate(c.g, now); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestExceptionAsProblem(t *testing.T) {
	g := chstore.ExceptionGroup{
		Fingerprint: "abcd1234", Type: "java.sql.SQLException",
		Message: "ORA-03113", Service: "bsa-cards", Occurrences: 950,
		FirstSeen: 123,
	}
	p := exceptionAsProblem(g)
	if p.ID != "exception:abcd1234" || p.Service != "bsa-cards" {
		t.Errorf("kimlik/servis: %+v", p)
	}
	if p.Severity != "warning" || p.Metric != "exception" {
		t.Errorf("severity/metric: %+v", p)
	}
	if !strings.Contains(p.RuleName, "java.sql.SQLException") ||
		!strings.Contains(p.Description, "950") ||
		!strings.Contains(p.Description, "ORA-03113") {
		t.Errorf("şablon alanları eksik: %+v", p)
	}
}
