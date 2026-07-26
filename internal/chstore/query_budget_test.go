package chstore

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// v0.9.280 — a server cap above the client's read budget is not a cap.
//
// store.go:376 sets ReadTimeout: 30s on the one application connection pool.
// clickhouse-go arms that deadline ONCE per read phase and never refreshes it
// on progress packets, so it is a hard wall-clock budget for the whole result
// stream. Any max_execution_time at or above 30 therefore cannot fire: the
// socket dies first and the operator gets a transport "i/o timeout" instead of
// ClickHouse code 159 — an error that names neither the query nor the reason.
//
// That is exactly what the operator saw on /traces before v0.9.276. A sweep
// then found SIXTY more queries in the same state, including a topology read
// budgeted at 180 seconds that has never once been allowed to use them.
//
// The rule this pins: a UI-facing read must cap BELOW the client budget, so the
// guard that fires is the one that can explain itself.
func TestServerCapsAreReachable(t *testing.T) {
	// Kept deliberately in sync with store.go's ReadTimeout. If that moves,
	// this fails and someone re-reads both numbers together, which is the
	// point — they only make sense as a pair.
	const clientReadTimeoutSec = 30

	// Background work legitimately wants a longer budget than the UI. It
	// cannot have one today — same pool, same 30s — so these are recorded as
	// KNOWN-UNREACHABLE rather than quietly lowered, because lowering them
	// would silently shorten a deliberate budget instead of fixing the
	// transport that denies it. The real fix is a separate worker pool with
	// its own ReadTimeout; until then they fail at 30s regardless of what the
	// number says.
	knownUnreachable := map[string]bool{
		"topology.go":        true, // correlator, worker role (main.go:86)
		"backtrace.go":       true,
		"exception_inbox.go": true,
	}

	re := regexp.MustCompile(`max_execution_time = (\d+)`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || knownUnreachable[f] {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			sec, _ := strconv.Atoi(m[1])
			checked++
			if sec >= clientReadTimeoutSec {
				t.Errorf("%s: max_execution_time = %d is unreachable — the client ReadTimeout is %ds\n"+
					"  (store.go:376) and clickhouse-go arms it once per read phase without refreshing.\n"+
					"  The socket dies first, so this guard never fires and the operator gets a\n"+
					"  transport i/o timeout instead of ClickHouse code 159. Cap below %d.",
					f, sec, clientReadTimeoutSec, clientReadTimeoutSec)
			}
		}
	}
	if checked < 40 {
		t.Fatalf("only %d caps inspected — the SETTINGS shape moved and this gate is no "+
			"longer aimed at anything", checked)
	}
}
