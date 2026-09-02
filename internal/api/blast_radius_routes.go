package api

// blast_radius_routes.go — v0.10.260 (perf profili §7 madde 4, F3 ⭐).
//
// Inbox açık problem satırı başına bir `/api/services/{svc}/blast-radius`
// atıyordu (200'e kadar istek, her biri kendi cache anahtarı + MV sorgusu).
// Toplu uç: bir istek, bir cache anahtarı (sıralı küme FNV — v0.5.187
// uzunluk-özeti tuzağı yok), bir MV sorgusu (LIMIT 25 BY service).
//
//	GET /api/blast-radius?services=a,b,c&since=1h → {"items": {svc: BlastRadius}}
//
// Rol kapısı yok (salt-okur drill-down; tekil ucun duruşu). Kayıt
// route_registry.go defterinden (api.go büyümez). ≤200 servis; fazlası 400.

import (
	"context"
	"fmt"
	"hash/fnv"
	"net/http"
	"sort"
	"strings"
	"time"
)

func init() { registerRoutesExtra("blast-radius-batch", (*Server).registerBlastRadiusBatchRoutes) }

func (s *Server) registerBlastRadiusBatchRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/blast-radius", s.getBlastRadiusBatch)
}

const blastRadiusBatchMax = 200

// blastRadiusServices — virgüllü listeyi tekil + sıralı yapar. SAF; testli.
func blastRadiusServices(raw string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// blastRadiusSetDigest — sıralı küme FNV-64a (cache anahtarı; uzunluk
// değil içerik). SAF; testli.
func blastRadiusSetDigest(sorted []string) string {
	h := fnv.New64a()
	for _, s := range sorted {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%x", h.Sum64())
}

func (s *Server) getBlastRadiusBatch(w http.ResponseWriter, r *http.Request) {
	svcs := blastRadiusServices(r.URL.Query().Get("services"))
	if len(svcs) == 0 {
		writeJSONError(w, http.StatusBadRequest, "services parametresi zorunlu (virgüllü)")
		return
	}
	if len(svcs) > blastRadiusBatchMax {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("en çok %d servis", blastRadiusBatchMax))
		return
	}
	since := parseDuration(r.URL.Query().Get("since"), time.Hour)
	if since > 24*time.Hour {
		since = 24 * time.Hour
	}
	// Anahtar = pencere + sıralı küme ÖZETİ (uzunluk değil — v0.5.187).
	key := fmt.Sprintf("blast-radius-batch:since=%s:svcs=%s", since, blastRadiusSetDigest(svcs))
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		now := time.Now()
		items, err := s.store.GetBlastRadiusBatch(ctx, svcs, now.Add(-since), now)
		if err != nil {
			return nil, err
		}
		return map[string]any{"items": items, "since": since.String()}, nil
	})
}
