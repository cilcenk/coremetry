package logstore

import (
	"context"
	"testing"
)

// v0.8.232 — pins the ESManager contract: apply-first save (an
// unconnectable/invalid config is rejected and NOTHING persists),
// clickhouse backend swaps the Switchable to the CH fallback, and
// LoadPersisted skips an unchanged blob (no ES client churn on the
// 30s refresh tick).

type fakeSettingsStore struct {
	raw  []byte
	puts int
}

func (f *fakeSettingsStore) GetLogstoreESSettingsRaw(context.Context) ([]byte, error) {
	return f.raw, nil
}
func (f *fakeSettingsStore) PutLogstoreESSettingsRaw(_ context.Context, raw []byte) error {
	f.raw = append([]byte(nil), raw...)
	f.puts++
	return nil
}

func TestESManagerSaveApplyFirst(t *testing.T) {
	ctx := context.Background()
	chFallback := &ESStore{} // stand-in Store; only identity matters
	sw := NewSwitchable(&ESStore{})
	m := NewESManager(sw, chFallback, nil, ESSettings{Backend: "clickhouse"})
	fs := &fakeSettingsStore{}

	// Invalid: ES backend without addresses → rejected, nothing persisted.
	err := m.SavePersisted(ctx, fs, ESSettings{Backend: "elasticsearch"})
	if err == nil {
		t.Fatal("ES backend without addresses must be rejected")
	}
	if fs.puts != 0 {
		t.Fatalf("failed apply must not persist (puts=%d)", fs.puts)
	}

	// Valid: clickhouse backend → swaps to the fallback + persists.
	if err := m.SavePersisted(ctx, fs, ESSettings{Backend: "clickhouse"}); err != nil {
		t.Fatalf("clickhouse save: %v", err)
	}
	if fs.puts != 1 {
		t.Fatalf("puts = %d, want 1", fs.puts)
	}
	if sw.Current() != Store(chFallback) {
		t.Fatal("Switchable must point at the CH fallback after a clickhouse save")
	}
	if snap := m.Snapshot(); snap.Source != "ui" || snap.Backend != "clickhouse" {
		t.Fatalf("snapshot after save = %+v", snap)
	}
}

func TestESManagerLoadPersistedSkipsUnchanged(t *testing.T) {
	ctx := context.Background()
	sw := NewSwitchable(&ESStore{})
	first := &ESStore{}
	m := NewESManager(sw, first, nil, ESSettings{Backend: "clickhouse"})
	fs := &fakeSettingsStore{}

	if err := m.SavePersisted(ctx, fs, ESSettings{Backend: "clickhouse"}); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	// Simulate a swap-behind (another apply) then an unchanged reload:
	// the blob equals lastRaw so LoadPersisted must NOT re-apply.
	other := &ESStore{}
	sw.Swap(other)
	if err := m.LoadPersisted(ctx, fs); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if sw.Current() != Store(other) {
		t.Fatal("unchanged blob must be a no-op (store was re-applied)")
	}
}

func TestESManagerNoBlobKeepsEnvConfig(t *testing.T) {
	sw := NewSwitchable(&ESStore{})
	m := NewESManager(sw, &ESStore{}, nil, ESSettings{Backend: "elasticsearch", Addresses: []string{"http://env:9200"}})
	if err := m.LoadPersisted(context.Background(), &fakeSettingsStore{}); err != nil {
		t.Fatalf("empty blob: %v", err)
	}
	snap := m.Snapshot()
	if snap.Source != "env" || len(snap.Addresses) != 1 {
		t.Fatalf("env seed must survive an empty blob: %+v", snap)
	}
}
