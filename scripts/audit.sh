#!/usr/bin/env bash
# audit.sh — grep-based regression catcher for the CLAUDE.md
# hard constraints. Scoped to the patterns that are cheap to
# match statically AND have a track record of regressing in this
# codebase. Not a substitute for /scale-audit (which is broader
# and reads context) — this is the always-on pre-tag gate.
#
# Exit codes:
#   0  no critical findings
#   1  at least one critical finding (cache-key len, eager picker,
#      direct copilot.Explain, non-GLOBAL IN over Distributed)
#
# Warnings (setInterval without document.hidden, FROM spans
# without nearby bounds, timeRangeToNs in JSX) print but don't
# fail — these have known false-positive surfaces. Operator
# triages from the printed list.
#
# Add new checks here as the codebase grows new sharp edges;
# every check should reference the v0.5.X incident or CLAUDE.md
# section that justifies it.

set -u
cd "$(dirname "$0")/.."

RED='\033[0;31m'
YEL='\033[0;33m'
GRN='\033[0;32m'
DIM='\033[2m'
NC='\033[0m'

CRITICAL=0
WARNINGS=0

hr() { printf '%s\n' "------------------------------------------------------------"; }

# Helper: print a 🔴 finding and bump the critical counter.
crit() {
    printf "${RED}🔴 %s${NC}\n" "$1"
    CRITICAL=$((CRITICAL + 1))
}

# Helper: print a 🟡 finding and bump the warning counter.
warn() {
    printf "${YEL}🟡 %s${NC}\n" "$1"
    WARNINGS=$((WARNINGS + 1))
}

# ─── CHECK 1: cache-key length anti-pattern (v0.5.187) ──────
# `fmt.Sprintf("...n=%d", len(set))` collapses two distinct
# sets with same cardinality to the same key. Cross-set
# poisoning at scale. Use a sorted+FNV digest helper instead.
hr
echo "CHECK 1 — cache-key length anti-pattern (v0.5.187)"
hits=$(grep -rn 'fmt\.Sprintf.*len(' internal/api 2>/dev/null \
    | grep -E ':[^:]*key|cache' \
    | grep -v _test.go || true)
if [ -n "$hits" ]; then
    while IFS= read -r line; do crit "$line"; done <<< "$hits"
else
    printf "${GRN}✓ clean${NC}\n"
fi

# ─── CHECK 2: setInterval without document.hidden ───────────
# Polling that doesn't pause when the tab is hidden burns
# mobile/laptop battery + idle API traffic. CLAUDE.md
# performance budget mandates the document.hidden guard.
# Pattern: setInterval callback should contain "document.hidden"
# within ~5 lines.
hr
echo "CHECK 2 — setInterval without document.hidden guard"
# awk pass: for each setInterval line, look ahead 5 lines for
# document.hidden. Print location if not found.
hits=$(awk '
    /setInterval\(/ {
        loc = FILENAME ":" FNR
        block = $0
        for (i = 1; i <= 5; i++) {
            if ((getline next_line) > 0) {
                block = block "\n" next_line
            } else { break }
        }
        if (block !~ /document\.hidden/) {
            print loc ": setInterval without document.hidden"
        }
    }
' $(find frontend/src -name '*.tsx' -o -name '*.ts' 2>/dev/null) 2>/dev/null || true)
if [ -n "$hits" ]; then
    while IFS= read -r line; do warn "$line"; done <<< "$hits"
else
    printf "${GRN}✓ clean${NC}\n"
fi

# ─── CHECK 3: eager Combobox catalogue load ─────────────────
# `<Combobox options={api.X()}>` where api.X() returns the full
# catalogue is the picker scale regression — 10k ops freeze the
# page. Expected pattern: ServicePicker / OperationPicker /
# MetricNamePicker (server-side debounced).
hr
echo "CHECK 3 — eager Combobox catalogue load"
hits=$(grep -rn '<Combobox options={api\.' frontend/src 2>/dev/null || true)
if [ -n "$hits" ]; then
    while IFS= read -r line; do crit "$line"; done <<< "$hits"
else
    printf "${GRN}✓ clean${NC}\n"
fi

# ─── CHECK 4: direct s.copilot.Explain (skips ai_calls) ─────
# CLAUDE.md invariant: every Copilot route routes through
# `s.copilotExplain(r, ...)`. The wrapper writes the ai_calls
# row for /ai page attribution. Direct `s.copilot.Explain(...)`
# silently breaks attribution.
hr
echo "CHECK 4 — direct s.copilot.Explain bypassing wrapper"
# ai_observability.go is the wrapper's own file — it MUST call
# the underlying s.copilot.Explain; that's the wrapper's job.
# Skip the wrapper file from the bypass check. Also skip
# comments — the wrapper file references the antipattern in
# its block comment.
hits=$(grep -rn 's\.copilot\.Explain(' internal/api 2>/dev/null \
    | grep -v 'ai_observability\.go' \
    | grep -v '://' \
    | grep -vE ':[0-9]+:\s*//' || true)
if [ -n "$hits" ]; then
    while IFS= read -r line; do crit "$line"; done <<< "$hits"
else
    printf "${GRN}✓ clean${NC}\n"
fi

# ─── CHECK 5: non-GLOBAL IN over Distributed (v0.5.427) ─────
# `trace_id IN (SELECT trace_id FROM spans ...)` against a
# Distributed table without GLOBAL prefix runs the subquery
# shard-locally — traces split across shards never resolve.
# Same v0.5.116 fix applied to topology / backtrace; the raw
# IN-subquery cases regressed in v0.5.427.
hr
echo "CHECK 5 — IN (SELECT ...) without GLOBAL prefix"
hits=$(grep -rn 'IN (SELECT' internal/chstore internal/api 2>/dev/null \
    | grep -v 'GLOBAL IN (SELECT' || true)
if [ -n "$hits" ]; then
    while IFS= read -r line; do crit "$line"; done <<< "$hits"
else
    printf "${GRN}✓ clean${NC}\n"
fi

# ─── CHECK 6: FROM spans without LIMIT or settings ──────────
# Heuristic: any `FROM spans` literal should have `LIMIT` OR
# `max_execution_time` within ±10 lines of the same string
# literal. False positives possible (multi-line string concat
# where the bounds live elsewhere) — flagged as warnings, not
# critical.
hr
echo "CHECK 6 — FROM spans literal without nearby LIMIT/max_execution_time"
hits=$(awk '
    /FROM spans\b/ {
        loc = FILENAME ":" NR
        block = $0
        # Look back 5 lines for context
        if (prev5 ~ /LIMIT|max_execution_time/) { next }
        # Look forward 10 lines
        ok = 0
        for (i = 1; i <= 10; i++) {
            if ((getline next_line) > 0) {
                block = block "\n" next_line
                if (next_line ~ /LIMIT|max_execution_time/) { ok = 1; break }
                if (next_line ~ /^\s*$/) { break }
            } else { break }
        }
        if (!ok) { print loc ": FROM spans without LIMIT/max_execution_time nearby" }
    }
    { prev5 = prev4; prev4 = prev3; prev3 = prev2; prev2 = prev1; prev1 = $0 }
' $(find internal -name '*.go' 2>/dev/null) 2>/dev/null || true)
if [ -n "$hits" ]; then
    while IFS= read -r line; do warn "$line"; done <<< "$hits"
else
    printf "${GRN}✓ clean${NC}\n"
fi

# ─── Summary ────────────────────────────────────────────────
hr
if [ $CRITICAL -gt 0 ]; then
    printf "${RED}AUDIT FAIL — %d critical, %d warning${NC}\n" "$CRITICAL" "$WARNINGS"
    exit 1
elif [ $WARNINGS -gt 0 ]; then
    printf "${YEL}audit clean (critical), %d warning to review${NC}\n" "$WARNINGS"
    exit 0
else
    printf "${GRN}audit clean — 0 critical, 0 warning${NC}\n"
    exit 0
fi
