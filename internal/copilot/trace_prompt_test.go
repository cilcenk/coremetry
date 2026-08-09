package copilot

import (
	"strings"
	"testing"
)

// trace_prompt_test.go — v0.9.842. Pins the DEPTH contract of the
// trace-explain system prompt.
//
// Operator-reported: one-click "Explain trace" came back shallow while
// typing "detaylı incele" by hand in the same drawer produced the
// structured analysis the operator wanted. The evidence package was
// already rich (100 spans + 15 correlated logs with exception.type and
// stacktrace); the prompt was the narrow part — it ordered "4-8 short
// bullet points… no preamble, no headers", actively discarding the
// depth the evidence had paid for.
//
// A prompt is a behavioural contract with no compiler behind it, so
// these are the invariants worth a test: the shortness orders must
// stay gone, the three section headers must stay present, and the two
// compositions (plain / code-context) must both carry them with the
// language directive still last.

func TestSystemTraceAsksForStructuredDepth(t *testing.T) {
	// The three sections the operator's own hand-typed prompt produced,
	// promoted into the one-click path.
	for _, header := range []string{
		"**İşlem Akışı ve Veri Özeti**",
		"**Stacktrace Detayı**",
		"**Kök Neden ve Sonraki Adım**",
	} {
		if !strings.Contains(systemTraceBody, header) {
			t.Errorf("systemTraceBody lost the %q section", header)
		}
	}
	// The regression itself: any of these phrases coming back means the
	// prompt is capping the answer again.
	for _, banned := range []string{
		"4-8 short bullet",
		"no headers",
		"No headers",
		"Be terse",
	} {
		if strings.Contains(systemTraceBody, banned) {
			t.Errorf("systemTraceBody re-introduced the shortness order %q", banned)
		}
	}
	// Depth without grounding is worse than shallow: the prompt must
	// keep forbidding invention, since the sections ask for exact codes
	// and class names.
	for _, must := range []string{"ONLY facts present in the evidence", "never invent"} {
		if !strings.Contains(systemTraceBody, must) {
			t.Errorf("systemTraceBody lost its grounding clause %q", must)
		}
	}
	// Sections are skippable — the MCP renderer (mcptools/prompts.go)
	// feeds spans with NO logs, so a mandatory stacktrace section would
	// invite a fabricated one there.
	// Matched on the unwrapped text: the constant is hard-wrapped, so a
	// literal single-line needle would fail on formatting alone.
	flat := strings.Join(strings.Fields(systemTraceBody), " ")
	if !strings.Contains(flat, "skipping a section entirely when its evidence is absent") {
		t.Error("systemTraceBody lost the skip-when-absent clause — the spans-only MCP path relies on it")
	}
}

// v0.9.831's body/addendum split must survive: both compositions carry
// the body, and the language directive stays the LAST thing in each.
func TestSystemTraceCompositions(t *testing.T) {
	for name, p := range map[string]string{
		"systemTrace":     systemTrace,
		"systemTraceCode": systemTraceCode,
	} {
		if !strings.Contains(p, systemTraceBody) {
			t.Errorf("%s no longer contains systemTraceBody verbatim", name)
		}
		if !strings.HasSuffix(p, AnswerInTurkish) {
			t.Errorf("%s does not end with the language directive", name)
		}
	}
	if !strings.Contains(systemTraceCode, systemCodeAddendum) {
		t.Error("systemTraceCode lost its code addendum")
	}
	// The addendum sits BETWEEN body and directive, not after it.
	if strings.Index(systemTraceCode, systemCodeAddendum) < strings.Index(systemTraceCode, systemTraceBody) {
		t.Error("systemTraceCode puts the addendum before the body")
	}
}
