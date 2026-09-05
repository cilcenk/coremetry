package api

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/logstore"
)

// halvesStore — Ascending yarıyı "after" sayan gecikmeli sahte mağaza.
type halvesStore struct {
	logstore.Store
	delay               time.Duration
	beforeErr, afterErr error
	mu                  sync.Mutex
	spans               map[bool][2]time.Time // Ascending → (start, end)
}

func (h *halvesStore) Search(ctx context.Context, f logstore.Filter) (*logstore.Page, error) {
	start := time.Now()
	select {
	case <-time.After(h.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	h.mu.Lock()
	if h.spans == nil {
		h.spans = map[bool][2]time.Time{}
	}
	h.spans[f.Ascending] = [2]time.Time{start, time.Now()}
	h.mu.Unlock()
	// v0.10.420 — iki yarı AYIRT EDİLİR (Total 1 / 2): before/after yer
	// değiştirse test kızarır (inceleme bulgusu).
	if f.Ascending {
		if h.afterErr != nil {
			return nil, h.afterErr
		}
		return &logstore.Page{Total: 2}, nil
	}
	if h.beforeErr != nil {
		return nil, h.beforeErr
	}
	return &logstore.Page{Total: 1}, nil
}

// overlap — iki çağrının zaman aralıkları kesişiyor mu (paralellik kanıtı;
// duvar saati tavanı yük altında kırılgandı — v0.10.420).
func (h *halvesStore) overlap() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	a, b := h.spans[false], h.spans[true]
	return a[0].Before(b[1]) && b[0].Before(a[1])
}
func (h *halvesStore) Backend() string { return "test" }

// v0.10.414 — A7: iki yarı gerçekten paralel; toplam süre iki gecikmenin
// toplamı değil, en uzunu.
func TestSearchContextHalves_Parallel(t *testing.T) {
	st := &halvesStore{delay: 100 * time.Millisecond}
	b, a, err := searchContextHalves(context.Background(), st, logstore.Filter{}, logstore.Filter{Ascending: true}, time.Second)
	if err != nil || b == nil || a == nil {
		t.Fatalf("err=%v b=%v a=%v", err, b, a)
	}
	if b.Total != 1 || a.Total != 2 {
		t.Fatalf("yarılar yer değiştirmiş: before.Total=%d after.Total=%d", b.Total, a.Total)
	}
	if !st.overlap() {
		t.Fatal("ardışık koşmuş: iki aramanın zaman aralıkları kesişmiyor")
	}
}

// Hata önceliği: yavaş backend (ErrBackendSlow) her zaman kazanır — modal
// 200 {degraded} kalır; yalnız gerçek sorgu hatası 5xx'e gider.
func TestSearchContextHalves_SlowWins(t *testing.T) {
	bad := errors.New("bad query")
	cases := map[string]struct {
		beforeErr, afterErr error
		wantSlow            bool
		wantErr             error
	}{
		"after yavaş":              {nil, dialRefused(), true, nil},
		"önce hatalı, sonra yavaş": {bad, dialRefused(), true, nil},
		"yalnız gerçek hata":       {bad, nil, false, bad},
		"temiz":                    {nil, nil, false, nil},
	}
	for name, c := range cases {
		st := &halvesStore{beforeErr: c.beforeErr, afterErr: c.afterErr}
		_, _, err := searchContextHalves(context.Background(), st, logstore.Filter{}, logstore.Filter{Ascending: true}, time.Second)
		if c.wantSlow != errors.Is(err, logstore.ErrBackendSlow) {
			t.Errorf("%s: slow=%v, err=%v", name, c.wantSlow, err)
		}
		if !c.wantSlow && !errors.Is(err, c.wantErr) {
			t.Errorf("%s: err=%v, want %v", name, err, c.wantErr)
		}
	}
}

// Kaynak pini: handler iki yarıyı da SkipTotal ile ve paralel yardımcıyla çağırır.
func TestGetLogsContext_UsesHalvesAndSkipTotal(t *testing.T) {
	b, err := os.ReadFile("api_logs.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "func (s *Server) getLogsContext(")
	j := strings.Index(src, "func (s *Server) getLogsTimeseries(")
	if i < 0 || j < i {
		t.Fatal("handler sınırları bulunamadı")
	}
	body := src[i:j]
	if n := strings.Count(body, "SkipTotal: true"); n != 2 {
		t.Fatalf("iki yarı da SkipTotal taşımalı, %d", n)
	}
	if !strings.Contains(body, "searchContextHalves(ctx, s.logs, beforeF, afterF, logsContextBudget)") {
		t.Fatal("handler paralel yardımcıyı kullanmıyor")
	}
	if strings.Contains(body, "afterPage, err := logstore.SearchWithTimeout") {
		t.Fatal("ardışık ikinci arama geri gelmiş")
	}
}
