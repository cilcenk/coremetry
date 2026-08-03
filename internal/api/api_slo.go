package api

// SLO read + CRUD handlers. Split out of api.go for code
// organisation (behaviour-preserving). The auto-create flow lives
// separately in slo_autocreate.go; shared helpers (writeJSON,
// writeErr, parseInt, parseDuration, newID, s.serveCached) stay in
// api.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// sloStatusConcurrency — /api/slos fan-out'unun eşzamanlılığı.
//
// 6: sıralıya göre belirgin kazanç, ama ClickHouse'a aynı anda
// düzinelerce ağır sorgu göndermeyecek kadar dar. Fan-out zaten 30s
// cache arkasında (pencere başına tek çalışma).
const sloStatusConcurrency = 6

func (s *Server) listSLOs(w http.ResponseWriter, r *http.Request) {
	// v0.8.200 (scale-audit) — cache the list: each SLO's status is its own CH
	// query (now MV-backed for availability) and this fans out over ALL of them.
	// Uncached, every /api/slos load (and every operator viewing it) re-ran N
	// status queries. A 30s TTL collapses that to one fan-out per window.
	s.serveCached(w, r, "slos:status", 30*time.Second, func(ctx context.Context) (any, error) {
		out, err := s.store.ListSLOs(ctx)
		if err != nil {
			return nil, err
		}
		// Pre-compute status alongside each SLO so the UI shows health badges
		// without N round-trips.
		type row struct {
			chstore.SLO
			Status *chstore.SLOStatus `json:"status,omitempty"`
		}
		// v0.9.578 (operator-reported, prod) — SINIRLI PARALEL + TEK LOG.
		//
		// Öncesi SIRALIYDI: SLO başına bir CH sorgusu, arka arkaya. Filo
		// büyüdükçe toplam süre istek deadline'ını aşıyordu ve o an
		// KALAN HER yineleme aynı "context deadline exceeded" hatasını
		// logluyordu — prod log'unda tek kök sebep için onlarca satır,
		// ardından "[cache] set slos:status: context deadline exceeded".
		//
		// İki ayrı kusur, iki ayrı düzeltme:
		//
		//  (a) Sel: ctx ölünce döngü DURUYOR ve TEK satır yazılıyor.
		//      N satır, N sorun olduğunu düşündürüyordu; oysa tek
		//      sorun vardı ve N kez tekrarlanıyordu.
		//  (b) Sebep: fan-out sınırlı paralel. Sorgular birbirini
		//      beklemiyor; eşzamanlılık ClickHouse'u boğmayacak kadar
		//      dar tutuldu (rootcause.go'nun alt-okuma deseni).
		rows := make([]row, len(out))
		var wg sync.WaitGroup
		sem := make(chan struct{}, sloStatusConcurrency)
		var errOnce sync.Once
		for i, o := range out {
			rows[i] = row{SLO: o}
			if ctx.Err() != nil {
				break
			}
			wg.Add(1)
			go func(i int, o chstore.SLO) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if ctx.Err() != nil {
					return
				}
				st, err := s.store.ComputeSLOStatus(ctx, o)
				if err != nil {
					// TEK satır: aynı kök sebep N kez yazılmaz.
					errOnce.Do(func() {
						log.Printf("[slo] durum hesabı başarısız (ilk hata, %d SLO'luk fan-out): %s: %v",
							len(out), o.ID, err)
					})
					return
				}
				rows[i].Status = st
			}(i, o)
		}
		wg.Wait()
		return rows, nil
	})
}

func (s *Server) getSLO(w http.ResponseWriter, r *http.Request) {
	o, err := s.store.GetSLO(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if o == nil {
		http.Error(w, `{"error":"slo not found"}`, http.StatusNotFound)
		return
	}
	writeJSON(w, o)
}

func (s *Server) sloStatus(w http.ResponseWriter, r *http.Request) {
	o, err := s.store.GetSLO(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if o == nil {
		http.Error(w, `{"error":"slo not found"}`, http.StatusNotFound)
		return
	}
	st, err := s.store.ComputeSLOStatus(r.Context(), *o)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, st)
}

// sloForecast (v0.6.30) — burn-down projection. Cached 60s on
// (id, burnWindow) so /slos polling doesn't fan out per-row
// CH reads on every tab refresh. ComputeSLOForecast itself runs
// TWO short queries (status + short-window burn rate) — the
// cache wrapper collapses them across consecutive operator
// page-loads in a session.
func (s *Server) sloForecast(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	burnWindow := parseDuration(r.URL.Query().Get("window"), time.Hour)
	// Cap the burn window so a hallucinated ?window=720h doesn't
	// blow the budget on a backfill-shaped query.
	if burnWindow > 24*time.Hour {
		burnWindow = 24 * time.Hour
	}
	key := fmt.Sprintf("slo-forecast:%s:%s", id, burnWindow)
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		o, err := s.store.GetSLO(ctx, id)
		if err != nil {
			return nil, err
		}
		if o == nil {
			return nil, fmt.Errorf("slo not found")
		}
		return s.store.ComputeSLOForecast(ctx, *o, burnWindow)
	})
}

// sloBurnSeries serves the per-day burn-rate timeseries that
// drives the /slos sparkline (v0.5.150). Cached 60s on (id, days)
// — sparkline doesn't need real-time accuracy and the GROUP BY
// over a 7d service-slice is cheap but not free.
func (s *Server) sloBurnSeries(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	days := parseInt(r.URL.Query().Get("days"), 7)
	key := fmt.Sprintf("slo-burn-series:%s:%d", id, days)
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		o, err := s.store.GetSLO(ctx, id)
		if err != nil {
			return nil, err
		}
		if o == nil {
			return nil, fmt.Errorf("slo not found")
		}
		series, err := s.store.ComputeSLOBurnSeries(ctx, *o, days)
		if err != nil {
			return nil, err
		}
		if series == nil {
			series = []chstore.BurnPoint{}
		}
		return map[string]any{
			"series": series,
			"days":   days,
		}, nil
	})
}

func (s *Server) createSLO(w http.ResponseWriter, r *http.Request) {
	var o chstore.SLO
	if err := json.NewDecoder(r.Body).Decode(&o); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if o.Name == "" || o.Service == "" || o.SLIType == "" {
		http.Error(w, `{"error":"name, service and sliType required"}`, http.StatusBadRequest)
		return
	}
	if o.Target <= 0 || o.Target >= 1 {
		http.Error(w, `{"error":"target must be a fraction between 0 and 1 (e.g. 0.99)"}`, http.StatusBadRequest)
		return
	}
	if o.SLIType == chstore.SLITypeLatency && o.ThresholdMs <= 0 {
		http.Error(w, `{"error":"thresholdMs required for latency SLIs"}`, http.StatusBadRequest)
		return
	}
	o.ID = newID(8)
	if err := s.store.UpsertSLO(r.Context(), o); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "slo.create", "slo", o.ID, fmt.Sprintf(`{"name":%q,"service":%q}`, o.Name, o.Service))
	writeJSON(w, o)
}

func (s *Server) deleteSLO(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteSLO(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "slo.delete", "slo", id, "")
	writeJSON(w, map[string]string{"status": "ok"})
}
