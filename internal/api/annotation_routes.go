package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// annotation_routes.go — annotation şeridinin birleşik olay ucu (v0.9.394,
// Faz C-2 Ş1; mockup 52b05851 operatör onaylı). api.go büyütülmez kısıtı:
// route'lar burada, api.go tek satır registerAnnotationRoutes çağırır.
//
// GET /api/annotations?service=&from=&to=
// → {items: [{ts, kind, title, service, targetType, targetId, link}],
//    truncated}
// kind: deploy | rollout | alert_fired | alert_resolved | anomaly | event
//
// Bugüne dek her chart kendi marker fetch'ini yapıyordu (EventMarkers tile
// başına /api/operator-events!); şerit sayfa başına TEK bounded çağrı ister.
// Beş kaynak paralel goroutine'de, her biri BEST-EFFORT (bundle sözleşmesi:
// tek kaynak hatası şeridi düşürmez, eksik kaynak items'a girmez).

// AnnotationItem — şeridin tek olay modeli (beş marker mekanizmasının
// hedef birleşimi; Ş3 emeklilikleri bunun üstüne).
type AnnotationItem struct {
	TS         int64  `json:"ts"` // unix ns
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Service    string `json:"service,omitempty"`
	TargetType string `json:"targetType,omitempty"` // problem | anomaly | event | rollout
	TargetID   string `json:"targetId,omitempty"`
	Link       string `json:"link,omitempty"`
}

const annotationCap = 500

func registerAnnotationRoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("GET /api/annotations", s.getAnnotations)
}

func (s *Server) getAnnotations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	service := strings.TrimSpace(q.Get("service"))
	from := parseTime(q.Get("from"))
	to := parseTime(q.Get("to"))
	if from.IsZero() || to.IsZero() {
		to = time.Now()
		from = to.Add(-1 * time.Hour)
	}

	key := fmt.Sprintf("annotations:v1:svc=%s:w=%s", service, cacheBucket(from, to))
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		type src struct {
			items []AnnotationItem
		}
		results := make([]src, 4)
		done := make(chan int, 4)

		// (a) deploys + rollouts — mevcut okumalar.
		go func() {
			var its []AnnotationItem
			for _, d := range s.serviceDeploysNonBlocking(ctx, service, from, to) {
				its = append(its, AnnotationItem{
					TS: d.TimeUnixNs, Kind: "deploy", Service: service,
					Title: "deploy " + d.Version, TargetType: "rollout",
				})
			}
			if res, err := s.store.GetServiceRollouts(ctx, service, from, to); err == nil && res != nil {
				for _, ro := range res.Rollouts {
					title := fmt.Sprintf("rollout ↻ %d pod", ro.PodsRemoved)
					if ro.VersionAfter != "" {
						title += " · " + ro.VersionAfter
					}
					its = append(its, AnnotationItem{
						TS: ro.TimeUnixNs, Kind: "rollout", Service: service,
						Title: title, TargetType: "rollout",
					})
				}
			}
			results[0] = src{its}
			done <- 0
		}()

		// (b) alarm tetik/çözülme — yeni pencere okuması (çözülmüşler dahil).
		go func() {
			var its []AnnotationItem
			if probs, err := s.store.ListProblemWindowEvents(ctx, service, from, to); err == nil {
				for _, p := range probs {
					title := p.RuleName
					if title == "" {
						title = p.ID
					}
					if p.StartedAt >= from.UnixNano() && p.StartedAt < to.UnixNano() {
						its = append(its, AnnotationItem{
							TS: p.StartedAt, Kind: "alert_fired", Service: p.Service,
							Title: "🔥 " + p.Severity + " · " + title,
							TargetType: "problem", TargetID: p.ID,
						})
					}
					if p.ResolvedAt != nil && *p.ResolvedAt >= from.UnixNano() && *p.ResolvedAt < to.UnixNano() {
						its = append(its, AnnotationItem{
							TS: *p.ResolvedAt, Kind: "alert_resolved", Service: p.Service,
							Title: "çözüldü · " + title,
							TargetType: "problem", TargetID: p.ID,
						})
					}
				}
			}
			results[1] = src{its}
			done <- 1
		}()

		// (c) anomaliler — yeni started_at pencere sorgusu.
		go func() {
			var its []AnnotationItem
			f := chstore.ListAnomalyEventsFilter{
				FromNs: from.UnixNano(), ToNs: to.UnixNano(),
				SinceNs: 1, // last_seen alt sınırını fiilen kapat (pencere started_at'te)
				Limit:   annotationCap,
			}
			if service != "" {
				f.Services = []string{service}
			}
			if evs, err := s.store.ListAnomalyEvents(ctx, f); err == nil {
				for _, e := range evs {
					its = append(its, AnnotationItem{
						TS: e.StartedAt, Kind: "anomaly", Service: e.Service,
						Title: "◈ " + e.Kind + ": " + e.Pattern,
						TargetType: "anomaly", TargetID: e.ID,
					})
				}
			}
			results[2] = src{its}
			done <- 2
		}()

		// (d) operatör olayları — service kapsamlı + global ("" service'li)
		// birlikte; ListEvents Service filtresi exact olduğundan Go süzer.
		go func() {
			var its []AnnotationItem
			if evs, err := s.store.ListEvents(ctx, chstore.EventFilter{
				From: from, To: to, Limit: annotationCap,
			}); err == nil {
				for _, e := range evs {
					if service != "" && e.Service != "" && e.Service != service {
						continue
					}
					its = append(its, AnnotationItem{
						TS: e.Time, Kind: "event", Service: e.Service,
						Title: "◇ " + e.Label, TargetType: "event",
						TargetID: e.ID, Link: e.Link,
					})
				}
			}
			results[3] = src{its}
			done <- 3
		}()

		for i := 0; i < 4; i++ {
			<-done
		}
		items := []AnnotationItem{}
		for _, r := range results {
			items = append(items, r.items...)
		}
		sort.Slice(items, func(i, j int) bool { return items[i].TS < items[j].TS })
		truncated := false
		if len(items) > annotationCap {
			items = items[:annotationCap]
			truncated = true
		}
		return map[string]any{"items": items, "truncated": truncated}, nil
	})
}
