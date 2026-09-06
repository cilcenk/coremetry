package entity

// derived_workload.go — v0.10.471 (CoSRE Telemetry Agent Faz 2, F2-4; audit
// G14): Thanos/KSM'nin BİLMEDİĞİ (yalnız span'den görülen) pod için workload
// adını pod ADINDAN türet — Deployment pod'u "<ad>-<rs hash>-<5>",
// StatefulSet pod'u "<ad>-<n>"; yalnız "<ad>-<5>" (DaemonSet ya da Job)
// belirsiz → kind "Derived" (yalan söylemek yerine). KSM sahibi varsa bu
// kural HİÇ çalışmaz (yapı Thanos'un). Türetilmiş workload source=span ve
// labels{kind, derived:"pod-name"} taşır; tool/kart "~" ile işaretler.

import "regexp"

var (
	derivedDeployRe = regexp.MustCompile(`^(.+)-[a-f0-9]{5,10}-[a-z0-9]{5}$`)
	derivedStsRe    = regexp.MustCompile(`^(.+)-\d{1,4}$`)
	derivedShortRe  = regexp.MustCompile(`^(.+)-[a-z0-9]{5}$`)
)

// DerivedKind — belirsiz kısa sonek (DaemonSet ya da Job) için tür adı.
const DerivedKind = "Derived"

// DerivedWorkload — pod adından (kind, name). ok=false → sonek yok (tekil pod).
func DerivedWorkload(pod string) (kind, name string, ok bool) {
	if m := derivedDeployRe.FindStringSubmatch(pod); m != nil {
		return "Deployment", m[1], true
	}
	if m := derivedStsRe.FindStringSubmatch(pod); m != nil {
		return "StatefulSet", m[1], true
	}
	if m := derivedShortRe.FindStringSubmatch(pod); m != nil {
		return DerivedKind, m[1], true
	}
	return "", "", false
}
