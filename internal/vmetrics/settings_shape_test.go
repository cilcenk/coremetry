package vmetrics

// v0.9.1164 — the JSON shape of the two new knobs, on both sides of the wire.
//
// This is the failure these tests exist for, and it is invisible in review: a
// mistyped struct tag compiles, `go build` passes, `tsc` passes (TypeScript
// cannot see a Go tag), and the form then behaves as if the setting does not
// exist — the box shows empty on every load and the PUT writes a key the
// backend ignores. The operator sets a floor, saves, sees "Kaydedildi", and
// the charts never change. Nothing anywhere reports an error.
//
// So the KEY NAMES are asserted against the literal strings the frontend
// reads (frontend/src/lib/types.ts), and the PERSISTENCE round trip is
// asserted through the same marshal/unmarshal pair SavePersisted and
// LoadPersisted use.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	jsonKeyRateWindowFloor = "rateWindowFloorS"
	jsonKeyAllowUnfiltered = "allowUnfilteredPercentiles"
)

func TestSettingsJSONKeys(t *testing.T) {
	raw, err := json.Marshal(Settings{
		Enabled: true, BaseURL: "http://vm:8428",
		RateWindowFloorS: 60, AllowUnfilteredPercentiles: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if v, ok := got[jsonKeyRateWindowFloor]; !ok || v != float64(60) {
		t.Fatalf("%s missing or wrong in the persisted blob: %s", jsonKeyRateWindowFloor, raw)
	}
	if v, ok := got[jsonKeyAllowUnfiltered]; !ok || v != true {
		t.Fatalf("%s missing or wrong in the persisted blob: %s", jsonKeyAllowUnfiltered, raw)
	}
}

// The DEFAULT blob must omit both keys.
//
// Not cosmetic: `omitempty` is what makes an absent key and an explicit zero
// the same thing, which is the contract the form's string-state relies on
// (vmForm.ts — an EMPTY BOX is the only honest "unset"). If either key started
// serialising as an explicit 0/false, a blob written by an old release and one
// written by a new one would differ in a way nothing reads, and the "0 means
// follow the default" story would depend on which release last saved.
func TestDefaultSettingsOmitTheKnobs(t *testing.T) {
	raw, err := json.Marshal(Settings{Enabled: true, BaseURL: "http://vm:8428"})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{jsonKeyRateWindowFloor, jsonKeyAllowUnfiltered} {
		if strings.Contains(string(raw), k) {
			t.Fatalf("default blob carries %s — the unset sentinel must be ABSENCE: %s", k, raw)
		}
	}
}

// The frontend must read the SAME key names. Pinned against its source rather
// than a duplicated constant, since the drift this catches is a rename on
// either side.
func TestFrontendReadsTheSameSettingsKeys(t *testing.T) {
	for _, rel := range []string{
		filepath.Join("frontend", "src", "lib", "types.ts"),
		filepath.Join("frontend", "src", "pages", "settings", "MetricsBackendTab.tsx"),
	} {
		b, err := os.ReadFile(filepath.Join("..", "..", rel))
		if err != nil {
			// "I could not measure it" is not "it is fine".
			t.Fatalf("%s unreadable, cannot verify the wire contract: %v", rel, err)
		}
		for _, k := range []string{jsonKeyRateWindowFloor, jsonKeyAllowUnfiltered} {
			if !strings.Contains(string(b), k) {
				t.Errorf("%s does not mention %q — the form would silently never "+
					"read or write this setting", rel, k)
			}
		}
	}
}

// Both knobs survive the persistence round trip, and the Snapshot exposes them.
//
// The Snapshot half matters on its own: the form seeds itself from the GET, so
// a field that persists but is not snapshotted shows an empty box that
// re-submits 0 on the next unrelated save — the aiTuning failure class,
// arriving from the other direction.
func TestKnobsRoundTripThroughPersistence(t *testing.T) {
	store := &fakeVMSettingsStore{}
	s := New()
	cfg := Settings{
		Enabled: true, BaseURL: "http://vm:8428", AuthType: "bearer", Token: "t",
		RateWindowFloorS: 45, AllowUnfilteredPercentiles: true,
	}
	if err := s.SavePersisted(context.Background(), store, cfg); err != nil {
		t.Fatal(err)
	}

	// A FRESH service hydrating from the stored blob — the boot path.
	fresh := New()
	if err := fresh.LoadPersisted(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if got := fresh.CurrentSettings(); got != cfg {
		t.Fatalf("round trip lost fields:\n got  %+v\n want %+v", got, cfg)
	}
	snap := fresh.Snapshot()
	if snap.RateWindowFloorS != 45 {
		t.Fatalf("snapshot floor = %d, want 45 — the form would show an empty box",
			snap.RateWindowFloorS)
	}
	if !snap.AllowUnfilteredPercentiles {
		t.Fatal("snapshot lost the guard override — the checkbox would render unchecked " +
			"while the guard was actually off")
	}

	// And the options the translation sees come from that same config.
	if opts := promOptions(fresh.CurrentSettings()); opts.RateWindowFloorS != 45 ||
		!opts.AllowUnfilteredPercentiles {
		t.Fatalf("promOptions did not carry the persisted knobs: %+v", opts)
	}
}

// An OLD blob (written before v0.9.1164) must hydrate into the GUARDED,
// default-floor state. This is the upgrade path, and it is the direction that
// must be safe: a missing key can only mean "protection on".
func TestPreUpgradeBlobHydratesGuarded(t *testing.T) {
	store := &fakeVMSettingsStore{
		raw: []byte(`{"enabled":true,"baseUrl":"http://vm:8428","authType":"bearer","token":"t"}`),
	}
	s := New()
	if err := s.LoadPersisted(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	cfg := s.CurrentSettings()
	if cfg.AllowUnfilteredPercentiles {
		t.Fatal("an old blob lifted the guard — the absent key must mean PROTECTED")
	}
	if cfg.RateWindowFloorS != 0 {
		t.Fatalf("floor = %d, want the 0 sentinel", cfg.RateWindowFloorS)
	}
	if got := resolveRateWindowFloor(cfg.RateWindowFloorS); got != promLookbehindFloorSec {
		t.Fatalf("an old blob resolved to a %ds floor, want the shipped %ds", got, promLookbehindFloorSec)
	}
}

type fakeVMSettingsStore struct{ raw []byte }

func (f *fakeVMSettingsStore) GetVMetricsSettingsRaw(context.Context) ([]byte, error) {
	return f.raw, nil
}

func (f *fakeVMSettingsStore) PutVMetricsSettingsRaw(_ context.Context, raw []byte) error {
	f.raw = raw
	return nil
}
