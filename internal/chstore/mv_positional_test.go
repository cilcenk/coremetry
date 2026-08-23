package chstore

// mv_positional_test.go — v0.9.1319 regression guard.
//
// Symptom (audit-found, never reached production because the trigger is
// narrow): the v0.5.349 peer.service-fallback upgrade loop resolved the
// MV it was about to recreate as mvs[3] / mvs[4]. Those constants were
// written when the catalogue was short. Two later insertions moved the
// targets: index 3 is spanmetrics_1m and 4 is spanmetrics_10s today,
// while db_summary_5m and db_caller_summary_5m sit further down. On a
// genuine pre-v0.5.349 upgrade the loop therefore dropped
// db_summary_5m and ran a CREATE ... IF NOT EXISTS for spanmetrics_1m,
// which no-ops — leaving the database aggregate MV missing for the
// whole process lifetime with no error logged anywhere.
//
// v0.8.52 already introduced mvDDLByName to kill this class and
// converted five call sites; this loop was the one it missed, and the
// comment above findMV claimed (falsely) that every migration below it
// used name lookup. The guards here therefore pin the MECHANISM, not
// the integers:
//
//	1. store.go contains no index expression on the mvs slice at all.
//	2. every name in the real catalogue resolves to its OWN CREATE —
//	   the pre-existing mvlookup_test.go only proved that against a
//	   synthetic eight-element slice.
//	3. the historical constants 3 and 4 do NOT point at the db MVs, so
//	   a reader who "just fixes the integers" sees why that is futile.
//	4. execDDL rejects an empty DDL, which is what mvDDLByName returns
//	   for a renamed or removed MV — the drop-then-create-nothing
//	   sequence now fails the boot loudly instead of silently.

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// mvCreateRe pulls the MV name out of a CREATE MATERIALIZED VIEW body.
var mvCreateRe = regexp.MustCompile(`CREATE MATERIALIZED VIEW IF NOT EXISTS (\w+)`)

// parseStoreGo parses internal/chstore/store.go into an AST. Parsing beats
// text scanning here: comments and string literals are separated for free,
// so the guard can never be fooled by the word "mvs[3]" appearing in prose
// (it did appear, in a stale comment, until v0.9.1319).
func parseStoreGo(t *testing.T) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "store.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse store.go: %v", err)
	}
	return fset, f
}

// mvCatalogue returns the ordered MV names of the `mvs := []string{...}`
// literal in store.go, mirroring the slice the upgrade migrations read.
// Elements are either a raw string literal or fmt.Sprintf(<literal>, ...).
func mvCatalogue(t *testing.T, f *ast.File) (names []string, ddls []string) {
	t.Helper()
	var lit *ast.CompositeLit
	ast.Inspect(f, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return true
		}
		id, ok := as.Lhs[0].(*ast.Ident)
		if !ok || id.Name != "mvs" {
			return true
		}
		if cl, ok := as.Rhs[0].(*ast.CompositeLit); ok {
			if lit != nil {
				t.Fatalf("store.go has more than one `mvs := []string{...}` literal; the guard would measure the wrong one")
			}
			lit = cl
		}
		return true
	})
	if lit == nil {
		t.Fatal("could not find the `mvs := []string{...}` catalogue in store.go")
	}
	for i, el := range lit.Elts {
		body := elementString(el)
		if body == "" {
			t.Fatalf("mvs[%d]: could not extract the DDL text (unexpected element shape %T)", i, el)
		}
		m := mvCreateRe.FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("mvs[%d]: no CREATE MATERIALIZED VIEW IF NOT EXISTS <name> in the element", i)
		}
		names = append(names, m[1])
		ddls = append(ddls, body)
	}
	return names, ddls
}

// elementString unwraps a slice element to its literal DDL text.
func elementString(el ast.Expr) string {
	switch e := el.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return ""
		}
		s, err := strconv.Unquote(e.Value)
		if err != nil {
			return ""
		}
		return s
	case *ast.CallExpr:
		if len(e.Args) == 0 {
			return ""
		}
		return elementString(e.Args[0])
	case *ast.BinaryExpr:
		// Some entries splice a shared expression constant into the DDL
		// with `+` (service_version_5m does). Keep the literal halves and
		// drop the identifier — the CREATE header, which is all the name
		// lookup reads, always lives in the first literal.
		if e.Op != token.ADD {
			return ""
		}
		return elementString(e.X) + elementString(e.Y)
	}
	return ""
}

// TestMVUpgradesNeverIndexMVsByPosition is the primary guard: no upgrade
// path may reach into the catalogue by position, whatever it calls the
// index variable. Scanning for the SHAPE (an index expression whose
// operand is `mvs`) rather than for the string "mvIdx" means a second
// spelling — mvs[7], mvs[i], mvs[dbIdx] — is caught the same way.
func TestMVUpgradesNeverIndexMVsByPosition(t *testing.T) {
	fset, f := parseStoreGo(t)
	ast.Inspect(f, func(n ast.Node) bool {
		ix, ok := n.(*ast.IndexExpr)
		if !ok {
			return true
		}
		id, ok := ix.X.(*ast.Ident)
		if !ok || id.Name != "mvs" {
			return true
		}
		t.Errorf("%s: positional lookup into the mvs catalogue (v0.8.52 / v0.9.1319 index-shift class) — use findMV(<name>) / mvDDLByName instead",
			fset.Position(ix.Pos()))
		return true
	})
}

// TestMVCatalogueResolvesEveryNameToItsOwnDDL runs mvDDLByName against the
// REAL catalogue. The pre-existing mvlookup_test.go proved the prefix rule
// on a synthetic slice; the live one carries genuinely collision-prone
// families (spanmetrics_1m / _1s / _10s / _calls_5m, db_summary_5m vs
// db_caller_summary_5m, messaging_summary_5m vs
// messaging_caller_summary_5m) that only this test exercises.
func TestMVCatalogueResolvesEveryNameToItsOwnDDL(t *testing.T) {
	_, f := parseStoreGo(t)
	names, ddls := mvCatalogue(t, f)
	if len(names) < 10 {
		t.Fatalf("only %d MVs extracted — the catalogue parser is broken, not the catalogue", len(names))
	}
	seen := map[string]bool{}
	for i, name := range names {
		if seen[name] {
			t.Errorf("MV %q declared twice in the catalogue (index %d)", name, i)
		}
		seen[name] = true
		got := mvDDLByName(ddls, name)
		if got == "" {
			t.Errorf("mvDDLByName(%q) returned empty against the real catalogue", name)
			continue
		}
		if got != ddls[i] {
			other := mvCreateRe.FindStringSubmatch(got)
			t.Errorf("mvDDLByName(%q) resolved to the DDL of %v — name collision", name, other)
		}
	}
	// Every name the upgrade migrations ask for must exist, or findMV
	// hands execDDL an empty string and the drop above it has no partner.
	for _, want := range []string{
		"service_summary_5m", "db_summary_5m", "db_caller_summary_5m",
		"db_statement_summary_5m", "trace_summary_5m",
	} {
		if !seen[want] {
			t.Errorf("upgrade migrations call findMV(%q) but the catalogue has no such MV — execDDL would receive \"\"", want)
		}
	}
}

// TestHistoricalMVIndicesPointAtTheWrongMVs pins WHY correcting the
// integers was never the fix. mvs[3] / mvs[4] were the db MVs when those
// constants were written; the catalogue has been inserted into twice
// since. If a future reorder happens to line them up again the guard
// stops meaning anything — delete it then, do NOT reintroduce indexing.
func TestHistoricalMVIndicesPointAtTheWrongMVs(t *testing.T) {
	_, f := parseStoreGo(t)
	names, _ := mvCatalogue(t, f)
	if len(names) <= 4 {
		t.Fatalf("catalogue too short (%d) to exercise the historical indices", len(names))
	}
	t.Logf("catalogue order today: %s", strings.Join(names, ", "))
	for idx, mv := range map[int]string{3: "db_summary_5m", 4: "db_caller_summary_5m"} {
		if names[idx] == mv {
			t.Errorf("mvs[%d] is %q again — the v0.9.1319 story no longer holds; drop this assertion rather than restoring positional lookup", idx, mv)
		}
	}
	// And the real positions, wherever they are, must be reachable by name.
	for _, mv := range []string{"db_summary_5m", "db_caller_summary_5m"} {
		found := -1
		for i, n := range names {
			if n == mv {
				found = i
				break
			}
		}
		if found < 0 {
			t.Fatalf("%s missing from the catalogue", mv)
		}
		t.Logf("%s lives at index %d (was assumed 3/4)", mv, found)
	}
}

// TestExecDDLRejectsEmptyDDL is the loud-failure half. mvDDLByName returns
// "" for an unknown name; every upgrade path is shaped
// "drop the old table, then execDDL(findMV(name))", so an empty DDL means
// the table is gone and nothing replaces it. The guard sits BEFORE the
// deferral check on purpose: in deferred-DDL mode an empty statement was
// appended to the queue and execDDL returned nil — the quietest possible
// branch.
func TestExecDDLRejectsEmptyDDL(t *testing.T) {
	cases := []struct {
		name   string
		defer_ bool
		sql    string
	}{
		{"empty immediate", false, ""},
		{"empty deferred", true, ""},
		{"whitespace immediate", false, "   \n\t "},
		{"whitespace deferred", true, "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Store{deferDDL: tc.defer_}
			err := s.execDDL(context.Background(), tc.sql)
			if err == nil {
				t.Fatal("execDDL accepted an empty DDL — a dropped MV would never be recreated and the boot would report success")
			}
			if !strings.Contains(err.Error(), "boş DDL") {
				t.Errorf("unexpected error %q", err)
			}
			if len(s.deferredDDL) != 0 {
				t.Errorf("empty DDL was queued for deferred execution: %q", s.deferredDDL)
			}
		})
	}
}
