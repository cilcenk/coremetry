package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"golang.org/x/sync/errgroup"
)

// deploys_page.go — filo Deploys/Rollouts geçmişi (v0.9.435, operatör
// istegi: "image değişince ve rollout olunca pod isimleri değişince
// history'yi gösteren bir sayfa"). İki kanıtlı kaynak tek uçta:
//
//   - DEPLOYS: GetDeploysInWindow — service.version/image-tag'in pencere
//     içindeki ilk görülmeleri. PENCERE-sınırlı varyant bilinçli: eski
//     GetRecentDeploys [since, şimdi] tarayıp en-yeni-N tuttuğundan
//     geçmiş custom pencerede sayfa boş görünürdü (verify bulgusu).
//   - ROLLOUTS/RESTARTS: GetServiceRollouts — pod-churn (Kind
//     deploy/restart, v0.8.405). Filo-geneli churn taraması pahalı;
//     aday kümesi pencerede deploy görülen servisler (cap) + ?service=.
//
// DÜRÜSTLÜK SÖZLEŞMESİ (no-silent-caps): her kesme yanıtta bayraklı —
// deploysTruncated (LIMIT doldu), candidateCapped (aday kümesi sığmadı),
// rolloutWindowClamped (churn taraması son ~6.5 güne kırpıldı;
// GetServiceRollouts'un 2000×5dk bucket tavanı 7g+ pencerede sessizce
// baş tarafı analiz ederdi — verify bulgusu), rolloutScanErrors
// (soft-fail sayısı — "rollout yok"tan ayırt edilir).
// Read-only, audit yok; cache anahtarı TÜM girdileri taşır.

const deploysPageRolloutCap = 20
const deploysPageRolloutConcurrency = 8

// deploysPageRolloutMaxWindow — GetServiceRollouts LIMIT 2000 bucket
// (≈6.94g); marjlı kırpma.
const deploysPageRolloutMaxWindow = 156 * time.Hour // 6.5 gün

// FleetRollout — Rollout + hangi servise ait olduğu (filo satırı).
type FleetRollout struct {
	Service string `json:"service"`
	chstore.Rollout
}

func (s *Server) getDeploysHistory(w http.ResponseWriter, r *http.Request) {
	from, to := parseFromTo(r, 24*time.Hour)
	service := r.URL.Query().Get("service")
	key := fmt.Sprintf("deploys-history:v1:%s:svc=%s", cacheBucket(from, to), service)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		const deployLimit = 200
		deps, err := s.store.GetDeploysInWindow(ctx, from, to, deployLimit)
		if err != nil {
			return nil, err
		}
		inWin := deps
		if service != "" {
			filtered := inWin[:0]
			for _, d := range inWin {
				if d.Service == service {
					filtered = append(filtered, d)
				}
			}
			inWin = filtered
		}

		// Rollout aday kümesi: deploy görülen servisler + açık seçim.
		seen := map[string]bool{}
		candidates := make([]string, 0, deploysPageRolloutCap)
		if service != "" {
			seen[service] = true
			candidates = append(candidates, service)
		}
		capped := false
		for _, d := range inWin {
			if seen[d.Service] {
				continue
			}
			if len(candidates) >= deploysPageRolloutCap {
				capped = true
				break
			}
			seen[d.Service] = true
			candidates = append(candidates, d.Service)
		}

		// Churn taraması penceresi: bucket tavanı yüzünden son ~6.5 gün.
		rolloutFrom := from
		rolloutClamped := false
		if to.Sub(rolloutFrom) > deploysPageRolloutMaxWindow {
			rolloutFrom = to.Add(-deploysPageRolloutMaxWindow)
			rolloutClamped = true
		}

		var mu sync.Mutex
		var scanErrs atomic.Int64
		rollouts := make([]FleetRollout, 0, 32)
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(deploysPageRolloutConcurrency)
		for _, svc := range candidates {
			svc := svc
			g.Go(func() error {
				res, rerr := s.store.GetServiceRollouts(gctx, svc, rolloutFrom, to)
				if rerr != nil || res == nil {
					// Soft-fail ama SESSİZ değil: sayaç yanıtta döner.
					scanErrs.Add(1)
					return nil
				}
				mu.Lock()
				for _, ro := range res.Rollouts {
					// Bucket-taban zamanı pencere başından ~5dk önceye
					// düşebilir — pencere dışı satır basma (verify bulgusu).
					if ro.TimeUnixNs >= rolloutFrom.UnixNano() && ro.TimeUnixNs <= to.UnixNano() {
						rollouts = append(rollouts, FleetRollout{Service: svc, Rollout: ro})
					}
				}
				mu.Unlock()
				return nil
			})
		}
		_ = g.Wait()
		sort.SliceStable(rollouts, func(i, j int) bool {
			return rollouts[i].TimeUnixNs > rollouts[j].TimeUnixNs
		})

		return map[string]any{
			"deploys":  inWin,
			"rollouts": rollouts,
			// Dürüstlük bayrakları — UI şeridi bunlardan konuşur.
			"deploysTruncated":     len(deps) == deployLimit,
			"scannedServices":      len(candidates),
			"candidateCapped":      capped,
			"rolloutWindowClamped": rolloutClamped,
			"rolloutScanErrors":    scanErrs.Load(),
		}, nil
	})
}
