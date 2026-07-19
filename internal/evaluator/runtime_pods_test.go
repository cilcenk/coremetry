package evaluator

import "testing"

// v0.9.90 — JVM pod runtime detector'ının saf eşik çekirdekleri. Bozuk
// evaluator herkesi page'ler; eşik mantığı EXACT kalmalı (CLAUDE.md #11).

func TestJVMHeapDecision(t *testing.T) {
	const eps = 1e-9
	tests := []struct {
		name     string
		usage    float64
		limit    float64
		wasOpen  bool
		wantOpen bool
		wantSev  string
		wantPct  float64
	}{
		{"boş limit → asla", 100, 0, false, false, "", 0},
		{"düşük kullanım → kapalı", 40, 100, false, false, "", 40},
		{"warn eşiği tam 85 → warning", 85, 100, false, true, "warning", 85},
		{"warn altı 84.9 → kapalı", 84.9, 100, false, false, "", 84.9},
		{"crit eşiği tam 90 → critical", 90, 100, false, true, "critical", 90},
		{"crit üstü → critical", 96, 100, false, true, "critical", 96},
		{"histerezis: açık + 83.5 → hâlâ warning", 83.5, 100, true, true, "warning", 83.5},
		{"histerezis: açık + 82.9 (band altı) → kapan", 82.9, 100, true, false, "", 82.9},
		{"histerezis: KAPALI + 83.5 → açma (band yalnız açığa)", 83.5, 100, false, false, "", 83.5},
		{"gerçekçi: 3.7/4.0 GB → critical", 3.7e9, 4.0e9, false, true, "critical", 92.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			open, sev, pct := jvmHeapDecision(tt.usage, tt.limit, tt.wasOpen)
			if open != tt.wantOpen || sev != tt.wantSev {
				t.Errorf("jvmHeapDecision(%v,%v,%v) = (%v,%q); want (%v,%q)",
					tt.usage, tt.limit, tt.wasOpen, open, sev, tt.wantOpen, tt.wantSev)
			}
			if diff := pct - tt.wantPct; diff > eps || diff < -eps {
				t.Errorf("pct = %v; want %v", pct, tt.wantPct)
			}
		})
	}
}

func TestJVMGCPauseDecision(t *testing.T) {
	tests := []struct {
		name     string
		avgMs    float64
		wasOpen  bool
		wantOpen bool
		wantSev  string
	}{
		{"düşük pause → kapalı", 120, false, false, ""},
		{"warn eşiği tam 500 → warning", 500, false, true, "warning"},
		{"warn altı 499 → kapalı", 499, false, false, ""},
		{"crit eşiği tam 1000 → critical", 1000, false, true, "critical"},
		{"crit üstü → critical", 2400, false, true, "critical"},
		{"histerezis: açık + 460 → hâlâ warning", 460, true, true, "warning"},
		{"histerezis: açık + 449 (band altı) → kapan", 449, true, false, ""},
		{"histerezis: KAPALI + 460 → açma", 460, false, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			open, sev := jvmGCPauseDecision(tt.avgMs, tt.wasOpen)
			if open != tt.wantOpen || sev != tt.wantSev {
				t.Errorf("jvmGCPauseDecision(%v,%v) = (%v,%q); want (%v,%q)",
					tt.avgMs, tt.wasOpen, open, sev, tt.wantOpen, tt.wantSev)
			}
		})
	}
}

func TestRuntimeServiceAndID(t *testing.T) {
	if got := runtimeService("auth-service", "pod-x2v"); got != "auth-service·pod-x2v" {
		t.Errorf("runtimeService with pod = %q", got)
	}
	if got := runtimeService("auth-service", ""); got != "auth-service" {
		t.Errorf("runtimeService no pod = %q", got)
	}
	if got := runtimeProblemID("jvm-heap", "auth-service", "pod-x2v"); got != "runtime:jvm-heap:auth-service:pod-x2v" {
		t.Errorf("problemID with pod = %q", got)
	}
	if got := runtimeProblemID("jvm-gc", "auth-service", ""); got != "runtime:jvm-gc:auth-service" {
		t.Errorf("problemID no pod = %q", got)
	}
}
