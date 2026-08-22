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
#
# Test-file exclusion (v0.5.450): every Go-source check filters
# out `*_test.go` files. Regression tests deliberately reference
# the anti-pattern they guard against ("does the WHERE contain
# `trace_id IN (SELECT ...`?") so scanning them like production
# code would self-trigger. The audit asks: does ACTUAL CODE use
# the bad pattern? Not: does any file mention it?

set -u
cd "$(dirname "$0")/.."

RED='\033[0;31m'
YEL='\033[0;33m'
GRN='\033[0;32m'
DIM='\033[2m'
NC='\033[0m'

CRITICAL=0
WARNINGS=0
SUPPRESSED=0

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

# ─── .auditignore — known false-positive suppression ────────
# Format: one `<file>:<line>` per line. Lines starting with `#`
# and blank lines ignored. Each entry suppresses any finding
# whose location matches as a substring (so file:line is the
# minimum specificity; file:line:check-id works too if you
# want to scope to a single check).
#
# Why a file, not inline annotations? Some FPs live inside
# template literals rendered to the operator (e.g. code-sample
# pages) — an inline `// audit-skip` comment would pollute the
# rendered sample. The file keeps suppression list-visible +
# under version control, and the operator periodically prunes
# entries that no longer apply.
IGNORE_FILE=".auditignore"
# Subshell-safe counter: filter_ignored runs inside `$(...)` so
# any variable assignment there is lost to the parent. Track
# suppression count by appending to a tempfile; read once at the
# summary stage.
SUPPRESS_LOG=$(mktemp -t audit_suppress.XXXXXX)
trap 'rm -f "$SUPPRESS_LOG"' EXIT
filter_ignored() {
    if [ ! -f "$IGNORE_FILE" ]; then cat; return; fi
    local patterns
    patterns=$(grep -v '^[[:space:]]*#' "$IGNORE_FILE" 2>/dev/null \
        | awk 'NF { print $1 }' \
        | grep -v '^[[:space:]]*$' || true)
    if [ -z "$patterns" ]; then cat; return; fi
    # grep -F treats patterns as fixed strings (no regex escaping
    # needed for paths with dots). -v inverts: keep lines NOT
    # matching any pattern. Substring match on file:line.
    local input
    input=$(cat)
    local kept
    kept=$(printf '%s\n' "$input" | grep -vF -f <(printf '%s\n' "$patterns") || true)
    local in_count out_count
    in_count=$(printf '%s' "$input"  | grep -c . || true)
    out_count=$(printf '%s' "$kept" | grep -c . || true)
    local delta=$((in_count - out_count))
    if [ "$delta" -gt 0 ]; then printf '%d\n' "$delta" >> "$SUPPRESS_LOG"; fi
    printf '%s' "$kept"
}

# ─── CHECK 1: cache-key length anti-pattern (v0.5.187) ──────
# `fmt.Sprintf("...n=%d", len(set))` collapses two distinct
# sets with same cardinality to the same key. Cross-set
# poisoning at scale. Use a sorted+FNV digest helper instead.
hr
echo "CHECK 1 — cache-key length anti-pattern (v0.5.187)"
hits=$(grep -rn 'fmt\.Sprintf.*len(' internal/api 2>/dev/null \
    | grep -E ':[^:]*key|cache' \
    | grep -v _test.go \
    | filter_ignored || true)
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
' $(find frontend/src -name '*.tsx' -o -name '*.ts' 2>/dev/null) 2>/dev/null \
    | filter_ignored || true)
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
hits=$(grep -rn '<Combobox options={api\.' frontend/src 2>/dev/null | filter_ignored || true)
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
    | grep -v _test.go \
    | grep -v '://' \
    | grep -vE ':[0-9]+:\s*//' \
    | filter_ignored || true)
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
    | grep -v 'GLOBAL IN (SELECT' \
    | grep -v _test.go \
    | filter_ignored || true)
if [ -n "$hits" ]; then
    while IFS= read -r line; do crit "$line"; done <<< "$hits"
else
    printf "${GRN}✓ clean${NC}\n"
fi

# ─── CHECK 5b: non-GLOBAL JOIN over Distributed (v0.9.1285) ──
# The JOIN twin of CHECK 5, and the gap that let Repeats ship
# dead: `LEFT JOIN spans s ON s.trace_id = d.trace_id` against a
# Distributed table is the same shard-local trap as a bare IN
# subquery — `spans` shards on rand(), so one trace's spans are
# scattered and each shard joins only its own slice. CHECK 5 only
# ever grepped `IN (SELECT`, so five sites (repeats, backtrace,
# three in topology) sat unflagged for months.
#
# Correct shape is BOTH halves: a GLOBAL prefix *and* the time
# bound inside the joined subquery's WHERE. A bare
# `GLOBAL JOIN <distributed table>` ships the whole table to the
# initiator, and a bound in the ON clause never prunes (measured
# v0.9.1285: 22.69M rows / 20.5s vs 75.65K rows / 0.056s). Because
# the correct form has the table name on its own line inside the
# subquery, matching `JOIN <telemetry table>` on ONE line catches
# exactly the wrong shape.
#
# Comment lines are skipped — the incident notes in repeats.go /
# repo.go quote the anti-pattern by name.
hr
echo "CHECK 5b — JOIN spans/metric_points/logs without GLOBAL prefix"
hits=$(grep -rnE '(LEFT|INNER|ANY|SEMI|ANTI|CROSS|FULL|OUTER|ASOF)?[[:space:]]*JOIN[[:space:]]+(spans|metric_points|logs)\b' \
        internal/chstore internal/api 2>/dev/null \
    | grep -v _test.go \
    | grep -vE '^[^:]+:[0-9]+:[[:space:]]*(//|--|\*)' \
    | grep -v 'GLOBAL' \
    | filter_ignored || true)
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
' $(find internal -name '*.go' ! -name '*_test.go' 2>/dev/null) 2>/dev/null \
    | filter_ignored || true)
if [ -n "$hits" ]; then
    while IFS= read -r line; do warn "$line"; done <<< "$hits"
else
    printf "${GRN}✓ clean${NC}\n"
fi

# ─── CHECK 7: duplicate route registration (v0.5.483) ───────
# Go's net/http ServeMux panics at boot when two handlers
# register the same "METHOD /path" pattern. Caught the hard
# way during v0.5.476 → v0.5.482: `mux.HandleFunc("GET /api/
# events", ...)` collided with the existing SSE stream's
# `mux.Handle("GET /api/events", ...)`. make audit's regex
# spotter would have caught it pre-tag.
#
# Pattern: extract `<METHOD> <PATH>` from both mux.Handle and
# mux.HandleFunc strings, normalise whitespace, sort + uniq -d.
hr
echo "CHECK 7 — duplicate net/http route registration"
hits=$(grep -hE 'mux\.Handle(Func)?\("[A-Z]+[[:space:]]+/' internal/api/*.go 2>/dev/null \
    | grep -oE '"[A-Z]+[[:space:]]+[^"]*"' \
    | sed -E 's/"//g; s/[[:space:]]+/ /g' \
    | sort \
    | uniq -d \
    | filter_ignored || true)
if [ -n "$hits" ]; then
    while IFS= read -r line; do crit "duplicate route: $line"; done <<< "$hits"
else
    printf "${GRN}✓ clean${NC}\n"
fi

# ─── CHECK 8: filter applied AFTER a LIMIT (v0.9.322→343) ───
# The defect family that produced 12 of the 40 releases in
# v0.9.301..340, and the one this script could not see.
#
# Shape: a store call carries a Limit, and the caller then
# picks rows out of the returned slice in Go. The LIMIT is
# spent on rows the caller is about to discard, so the ones
# it wanted may never have entered the window — and nothing
# errors. Real instances: the inbox badge said 29 open
# incidents while the list showed 2 (v0.9.322); GetIncident
# paged the newest 1000 and ~87% of incidents were
# unfetchable (v0.9.332); three copilot handlers answered
# "not found" for any row outside their page (v0.9.343).
#
# Narrow on purpose: only the id-lookup form (Limit, then a
# loop comparing an .ID and breaking out). Broad heuristics
# here drown in false positives; this one has been verified
# to flag every known instance of the family and nothing
# else. Warning, not critical — the correct fix is usually a
# by-id primitive, and a legitimate small-N scan exists.
hr
echo "CHECK 8 — filter/scan after a LIMIT (v0.9.322→343)"
hits=$(awk '
    FNR == 1 { lim = 0 }
    # A function boundary closes the window: a Limit in one function says
    # nothing about an id comparison in the next one.
    /^func / { lim = 0 }
    /Limit:[[:space:]]*[0-9a-zA-Z_]+/ { lim = FNR }
    # Compare against a VALUE, not an empty-string guard — `if i.ID == ""`
    # is a nil check, not a scan.
    /\.ID ==|\.Fingerprint ==/ && !/== ""/ {
        if (lim > 0 && FNR - lim <= 15) {
            printf "%s:%d — Limit at line %d, then a Go-side id scan\n", FILENAME, FNR, lim
            lim = 0
        }
    }
' internal/api/*.go internal/chstore/*.go 2>/dev/null | filter_ignored || true)
if [ -n "$hits" ]; then
    while IFS= read -r line; do warn "$line"; done <<< "$hits"
else
    printf "${GRN}✓ clean${NC}\n"
fi

# ─── CHECK 9: toUnixTimestamp64Nano over toStartOfInterval ───
# İKİ KEZ prod'a çıktı, ikisi de operatör raporuyla döndü.
#
# toStartOfInterval, saniye grenli bir INTERVAL ile DateTime64
# girdiden düz DateTime üretir; toUnixTimestamp64Nano yalnız
# DateTime64 kabul eder → code 43, sorgu HİÇ çalışmaz.
#
# v0.8.312 (exception occurrences grafiği) ve v0.9.572
# (paylaşılan bağımlılık dedektörü). İkincisi boş tabloda bile
# patlıyordu, yani özellik hiç çalışmadı — ve saf-fonksiyon
# testleri bunu göremedi çünkü SQL'e dokunmuyorlardı.
#
# Doğru desen: bucket'ı CH'nin doğal zaman tipinde döndür,
# unix-ns'e Go'da çevir (bucket.UnixNano()). İki şemada da
# (lokal monolitik + harici Distributed) tip-bağımsız.
hr
echo "CHECK 9 — toUnixTimestamp64Nano(toStartOfInterval(...))"
hits=$(grep -rn 'toUnixTimestamp64Nano(toStartOfInterval' \
    internal/ 2>/dev/null | filter_ignored || true)
if [ -n "$hits" ]; then
    while IFS= read -r line; do
        crit "$line — toStartOfInterval DateTime döndürür, toUnixTimestamp64Nano DateTime64 ister (code 43). Bucket'ı time.Time olarak tara, UnixNano()'yu Go'da çağır."
    done <<< "$hits"
else
    printf "${GRN}✓ clean${NC}\n"
fi

# ─── Summary ────────────────────────────────────────────────
hr
SUPPRESSED=$(awk '{ s += $1 } END { print s+0 }' "$SUPPRESS_LOG")
if [ "$SUPPRESSED" -gt 0 ]; then
    printf "${DIM}(%d finding(s) suppressed via .auditignore)${NC}\n" "$SUPPRESSED"
fi
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
