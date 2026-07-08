package api

// v0.8.378 — Stage-2 slice D2: /api/databases/statements/detail cache-key
// pins. The key must hash ALL inputs (the v0.5.187 hard constraint) and
// the (system, db) fold must be boundary-safe: free-text fields joined
// without a separator can alias ("a"+"b|c" == "a|b"+"c").

import (
	"testing"
	"time"
)

func TestDBStmtDetailKey(t *testing.T) {
	from := time.Date(2026, 7, 8, 11, 0, 0, 0, time.UTC)
	to := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	base := dbStmtDetailKey(42, "postgresql", "orders", false, from, to)

	t.Run("every input changes the key", func(t *testing.T) {
		variants := map[string]string{
			"hash":    dbStmtDetailKey(43, "postgresql", "orders", false, from, to),
			"system":  dbStmtDetailKey(42, "mysql", "orders", false, from, to),
			"db":      dbStmtDetailKey(42, "postgresql", "billing", false, from, to),
			"compare": dbStmtDetailKey(42, "postgresql", "orders", true, from, to),
			"from":    dbStmtDetailKey(42, "postgresql", "orders", false, from.Add(2*time.Minute), to),
			"to":      dbStmtDetailKey(42, "postgresql", "orders", false, from, to.Add(2*time.Minute)),
		}
		for input, key := range variants {
			if key == base {
				t.Errorf("changing %s did not change the cache key: %s", input, key)
			}
		}
	})

	t.Run("system/db field boundary cannot be forged", func(t *testing.T) {
		// Without the NUL separator these two tuples would fold to the
		// same digest — the v0.5.187 ambiguity class on strings.
		a := dbStmtDetailKey(42, "post", "gresqlorders", false, from, to)
		if a == base {
			t.Errorf("shifted (system, db) boundary aliased the key: %s", a)
		}
	})

	t.Run("same minute shares one slot", func(t *testing.T) {
		// Windows are minute-bucketed (pivotMinuteBucket) so concurrent
		// drawer opens within the same minute hit one upstream read.
		k := dbStmtDetailKey(42, "postgresql", "orders", false,
			from.Add(20*time.Second), to.Add(45*time.Second))
		if k != base {
			t.Errorf("sub-minute window jitter split the cache slot:\n a=%s\n b=%s", base, k)
		}
	})
}
