package chstore

import (
	"errors"
	"testing"
)

// fakeRows records whether Close was called and can panic on Close to stand in
// for the half-initialised *rows clickhouse-go returns on a query error.
type fakeRows struct {
	closed       bool
	panicOnClose bool
}

func (f *fakeRows) Close() error {
	if f.panicOnClose {
		panic("Close() on a query-error rows — nil deref")
	}
	f.closed = true
	return nil
}

// v0.8.185 — operator-reported PRODUCTION crash-loop on the external distributed
// cluster: the hasClusterCol boot probe `SELECT cluster FROM spans` errors
// (the materialized column never reached spans_local), and clickhouse-go
// returns a NON-NIL but half-initialised *rows whose Close() nil-derefs. The
// old `if rows != nil { rows.Close() }` panicked the pod at boot. maybeCloseRows
// must NEVER touch the rows when the query failed, and must still close it on
// success.
func TestMaybeCloseRows_SkipsOnQueryError(t *testing.T) {
	// On a query error, the broken rows must NOT be closed (would panic).
	broken := &fakeRows{panicOnClose: true}
	maybeCloseRows(broken, errors.New("code: 47, message: Identifier '__table1.cluster' cannot be resolved"))
	if broken.closed {
		t.Fatal("maybeCloseRows must NOT close a query-error rows")
	}

	// On success, the rows must be closed (no resource leak).
	ok := &fakeRows{}
	maybeCloseRows(ok, nil)
	if !ok.closed {
		t.Fatal("maybeCloseRows must close a successful query's rows")
	}

	// A nil rows on success is a no-op (no panic).
	maybeCloseRows(nil, nil)
}
