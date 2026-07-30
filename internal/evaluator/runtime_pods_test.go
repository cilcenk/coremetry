package evaluator

import "testing"

// v0.9.90 — JVM pod runtime detector'ının saf eşik çekirdekleri. Bozuk
// evaluator herkesi page'ler; eşik mantığı EXACT kalmalı (CLAUDE.md #11).

// v0.9.426 (operator-reported, prod: "JVM hatası olmayan podlara alert")
// — iki sinyalli eşikler: GC-sonrası doluluk varsa 85/90 (gerçek baskı),
// yoksa anlık used/max 92/97 (testere-dişi payı). Gürültü düzeltmesinin
// asıl pini: eski warn bölgesi (85-92, anlık sinyal) artık KAPALI.
func TestJVMHeapDecision(t *testing.T) {
	const eps = 1e-9
	tests := []struct {
		name     string
		usage    float64
		postGC   float64
		limit    float64
		wasOpen  bool
		wantOpen bool
		wantSev  string
		wantPct  float64
		wantPost bool
	}{
		{"boş limit → asla", 100, 0, 0, false, false, "", 0, false},
		{"düşük kullanım → kapalı", 40, 0, 100, false, false, "", 40, false},

		// GC-SONRASI sinyal varken: 85/90 eşikleri, pct post-GC'den.
		{"postGC warn eşiği tam 85", 95, 85, 100, false, true, "warning", 85, true},
		{"postGC crit eşiği tam 90", 99, 90, 100, false, true, "critical", 90, true},
		{"postGC düşükse anlık YÜKSEK bile olsa kapalı (sağlıklı testere-dişi)", 95, 60, 100, false, false, "", 60, true},
		{"postGC histerezis: açık + 83.5 → hâlâ warning", 99, 83.5, 100, true, true, "warning", 83.5, true},

		// ANLIK sinyal (postGC=0): eşikler 92/97 — eski 85-92 bölgesi kapalı.
		{"GÜRÜLTÜ PİNİ: anlık 85 artık kapalı", 85, 0, 100, false, false, "", 85, false},
		{"GÜRÜLTÜ PİNİ: anlık 90 artık kapalı", 90, 0, 100, false, false, "", 90, false},
		{"anlık warn eşiği tam 92", 92, 0, 100, false, true, "warning", 92, false},
		{"anlık crit eşiği tam 97", 97, 0, 100, false, true, "critical", 97, false},
		{"anlık histerezis: açık + 90.5 → hâlâ warning", 90.5, 0, 100, true, true, "warning", 90.5, false},
		{"anlık histerezis: açık + 89.9 (band altı) → kapan", 89.9, 0, 100, true, false, "", 89.9, false},
		{"anlık histerezis: KAPALI + 90.5 → açma", 90.5, 0, 100, false, false, "", 90.5, false},
		{"gerçekçi: 3.7/4.0 GB anlık 92.5 → warning (eskiden critical'dı)", 3.7e9, 0, 4.0e9, false, true, "warning", 92.5, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			open, sev, pct, post := jvmHeapDecision(tt.usage, tt.postGC, tt.limit, tt.wasOpen)
			if open != tt.wantOpen || sev != tt.wantSev || post != tt.wantPost {
				t.Errorf("jvmHeapDecision(%v,%v,%v,%v) = (%v,%q,post=%v); want (%v,%q,post=%v)",
					tt.usage, tt.postGC, tt.limit, tt.wasOpen, open, sev, post, tt.wantOpen, tt.wantSev, tt.wantPost)
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

// v0.9.440 — GC zaman payı (operatör istegi: "çok uzun GC + GC sayısı
// yüksek"): uzun pause'lar da sık kısa pause'lar da payı şişirir; tek
// eşik iki şikâyeti kapsar.
func TestJVMGCShareDecision(t *testing.T) {
	tests := []struct {
		name     string
		sharePct float64
		wasOpen  bool
		wantOpen bool
		wantSev  string
	}{
		{"düşük pay → kapalı", 3, false, false, ""},
		{"warn eşiği tam 10 → warning", 10, false, true, "warning"},
		{"warn altı 9.9 → kapalı", 9.9, false, false, ""},
		{"crit eşiği tam 25 → critical", 25, false, true, "critical"},
		{"sık-kısa GC senaryosu: %14 → warning", 14, false, true, "warning"},
		{"histerezis: açık + 8.5 → hâlâ warning", 8.5, true, true, "warning"},
		{"histerezis: açık + 7.9 (band altı) → kapan", 7.9, true, false, ""},
		{"histerezis: KAPALI + 8.5 → açma", 8.5, false, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			open, sev := jvmGCShareDecision(tt.sharePct, tt.wasOpen)
			if open != tt.wantOpen || sev != tt.wantSev {
				t.Errorf("jvmGCShareDecision(%v,%v) = (%v,%q); want (%v,%q)",
					tt.sharePct, tt.wasOpen, open, sev, tt.wantOpen, tt.wantSev)
			}
		})
	}
}

func TestRuntimeServiceAndID(t *testing.T) {
	// v0.9.401 (operator-reported): service alanı artık YALNIZ gerçek
	// servis — "servis·pod" birleşimi P1 listesinde servis adını yutuyor
	// ve tıklamayı sahte servise götürüyordu. Pod, problemID + reason'da.
	if got := runtimeService("auth-service", "pod-x2v"); got != "auth-service" {
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
