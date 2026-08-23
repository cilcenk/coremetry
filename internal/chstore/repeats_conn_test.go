package chstore

import (
	"context"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// v0.9.1290 — repeats.go was the raw-spans reader that skipped the
// RoundRobin read pool and coordinated every call on the in-order main
// connection. The regression this test would catch is a silent one: put
// s.conn back and nothing breaks, no error surfaces, no row changes —
// the only symptom is that a heavy scan piles onto one node again. The
// allowlist test next door (TestTelemetryReadConnCallSurface) cannot
// catch it either, because that gate only fires on files that call
// telemetryReadConn WITHOUT being listed. It is a one-way gate: removing
// the call is invisible to it. So the pin has to be positive.
//
// It is pinned behaviourally rather than by source text: both pools are
// swapped for a recorder and the assertion is which one actually
// received the query.
//
// Case 2 is the positive control and doubles as the local-environment
// contract. With no read pool configured (the minikube install, and
// every Store{} built directly in a test) telemetryReadConn falls back
// to s.conn, so the query has to land on the main connection and the
// behaviour stays byte-identical to pre-v0.9.1290. Without this half, a
// dead accessor that always returned s.conn would still pass case 1.
type repeatsRecordingConn struct {
	driver.Conn // embedded on purpose: any method this test does not
	// implement panics on nil rather than passing quietly.
	label string
	used  *string
}

func (c repeatsRecordingConn) Query(ctx context.Context, query string, args ...any) (driver.Rows, error) {
	*c.used = c.label
	return emptyRepeatRows{}, nil
}

type emptyRepeatRows struct{ driver.Rows }

func (emptyRepeatRows) Next() bool   { return false }
func (emptyRepeatRows) Close() error { return nil }
func (emptyRepeatRows) Err() error   { return nil }

func TestRepeatsReadsThroughTelemetryPool(t *testing.T) {
	f := RepeatedSpanFilter{From: time.Now().Add(-time.Hour), To: time.Now()}

	t.Run("okuma havuzu varsa oraya gider", func(t *testing.T) {
		used := ""
		s := &Store{}
		s.conn = repeatsRecordingConn{label: "main", used: &used}
		s.read = repeatsRecordingConn{label: "read", used: &used}
		if _, err := s.QueryRepeatedSpans(context.Background(), f); err != nil {
			t.Fatalf("beklenmeyen hata: %v", err)
		}
		if used != "read" {
			t.Errorf("repeats sorgusu %q bağlantısına gitti, telemetryReadConn (okuma havuzu) beklenirdi — ham spans taraması in-order ana bağlantıya geri düşmüş (v0.9.1290 gerilemesi)", used)
		}
	})

	t.Run("okuma havuzu yoksa ana bağlantıya düşer", func(t *testing.T) {
		used := ""
		s := &Store{}
		s.conn = repeatsRecordingConn{label: "main", used: &used}
		if _, err := s.QueryRepeatedSpans(context.Background(), f); err != nil {
			t.Fatalf("beklenmeyen hata: %v", err)
		}
		if used != "main" {
			t.Errorf("okuma havuzu açılmamışken sorgu %q bağlantısına gitti, ana bağlantı beklenirdi — replica yapılandırılmayan kurulumda davranış değişmemeli", used)
		}
	})
}
