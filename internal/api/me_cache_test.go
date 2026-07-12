package api

import (
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// me_cache_test.go — v0.8.519 regression: TTL, miss, clear ve toptan
// temizlik sözleşmesi (her kullanıcı-yazması clear çağırır).
func TestMeCache(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	c := newMeCache(30 * time.Second)
	u := &chstore.User{ID: "u1", Email: "a@x"}

	if _, ok := c.get("u1", t0); ok {
		t.Fatal("boş cache hit vermemeli")
	}
	c.put("u1", u, t0)
	if got, ok := c.get("u1", t0.Add(29*time.Second)); !ok || got.Email != "a@x" {
		t.Fatal("TTL içinde hit beklenirdi")
	}
	if _, ok := c.get("u1", t0.Add(31*time.Second)); ok {
		t.Fatal("TTL sonrası miss beklenirdi")
	}
	c.put("u1", u, t0)
	c.clear()
	if _, ok := c.get("u1", t0); ok {
		t.Fatal("clear sonrası miss beklenirdi")
	}
}
