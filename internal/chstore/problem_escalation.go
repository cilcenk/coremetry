package chstore

import (
	"context"
	"encoding/json"
)

// ProblemEscalationConfig drives the evaluator's age-based
// escalation sweep (escalateStaleProblems). An open Problem that
// nobody acks climbs the severity ladder on its own:
// info → warning → critical.
//
// Hard-coded at 15 min / 30 min until v0.9.248. Operator-reported:
// tightening anomaly promotion to get FEWER pages barely helped,
// because anything that did get through still escalated itself to
// critical half an hour later and re-fired the notify channel on the
// way (escalateStaleProblems calls SendProblemAlert on every bump).
// The knob that was supposed to make things quieter was fighting a
// constant nobody could see or change.
//
// Zero-value config is the pre-v0.9.248 behaviour, so an install that
// never opens the settings page keeps escalating exactly as before.
type ProblemEscalationConfig struct {
	// Enabled — master switch. False means severity stays wherever
	// the rule that opened the Problem put it. Useful for fleets
	// where "open for 30 minutes" is normal (batch windows,
	// long-running incidents already being worked) and the
	// automatic climb is pure pager noise.
	Enabled bool `json:"enabled"`
	// InfoToWarningSec — age at which an `info` Problem becomes
	// `warning`. Default 900 (15 min).
	InfoToWarningSec int `json:"infoToWarningSec"`
	// WarningToCriticalSec — age at which a `warning` Problem
	// becomes `critical` (and pages anyone subscribed to critical
	// only). Default 1800 (30 min). Must be >= InfoToWarningSec,
	// otherwise an info Problem would reach critical before it ever
	// reached warning; enforced at the API boundary and clamped on
	// read.
	WarningToCriticalSec int `json:"warningToCriticalSec"`
}

// DefaultProblemEscalation mirrors the constants this replaced
// (escalateInfoToWarningAfter / escalateWarningToCriticalAfter) so
// upgrading changes nothing until the operator says so.
func DefaultProblemEscalation() ProblemEscalationConfig {
	return ProblemEscalationConfig{
		Enabled:              true,
		InfoToWarningSec:     15 * 60,
		WarningToCriticalSec: 30 * 60,
	}
}

const problemEscalationKey = "problem_escalation"

// GetProblemEscalation returns the persisted config, or the defaults
// when nothing is saved. Soft-fails to defaults on CH error so a
// transient blip can't silently change escalation behaviour in a
// long-running evaluator — same posture as GetAnomalyPromotion.
//
// NOTE on the Enabled field: unlike the numeric knobs, `false` is a
// meaningful saved value, so it is never patched back to the default.
// The whole struct is replaced only when the row is absent or
// unparseable; Normalize below touches the numbers only.
func (s *Store) GetProblemEscalation(ctx context.Context) ProblemEscalationConfig {
	raw, err := s.GetSetting(ctx, problemEscalationKey)
	if err != nil || len(raw) == 0 {
		return DefaultProblemEscalation()
	}
	var c ProblemEscalationConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return DefaultProblemEscalation()
	}
	return NormalizeProblemEscalation(c)
}

// NormalizeProblemEscalation patches absent / nonsensical numeric
// values back into a usable shape. Split out so the API and the
// tests exercise the same rules the read path uses.
func NormalizeProblemEscalation(c ProblemEscalationConfig) ProblemEscalationConfig {
	d := DefaultProblemEscalation()
	// A partial PUT (or a hand-edited settings row) can omit these.
	// An `int` can't distinguish "absent" from "explicit 0", and 0
	// would mean "escalate instantly" — the opposite of a safe
	// fallback — so 0 means default.
	if c.InfoToWarningSec <= 0 {
		c.InfoToWarningSec = d.InfoToWarningSec
	}
	if c.WarningToCriticalSec <= 0 {
		c.WarningToCriticalSec = d.WarningToCriticalSec
	}
	// critical must never come before warning.
	if c.WarningToCriticalSec < c.InfoToWarningSec {
		c.WarningToCriticalSec = c.InfoToWarningSec
	}
	return c
}

// SaveProblemEscalation persists the config under system_settings —
// same key/value table every other operator setting uses, so it
// survives restart without new schema.
func (s *Store) SaveProblemEscalation(ctx context.Context, c ProblemEscalationConfig) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.PutSetting(ctx, problemEscalationKey, raw)
}
