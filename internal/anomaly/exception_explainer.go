package anomaly

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/cache"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/copilot"
	"github.com/cilcenk/coremetry/internal/logstore"
	"github.com/cilcenk/coremetry/internal/rca"
)

// ExceptionExplainer (v0.9.415, operatör istegi #1) — ProblemExplainer'ın
// exception ikizi: inbox'a P1 olarak düşen ("anonslu") exception grupları
// için kök-sebep özetini PROAKTİF doldurur. Operatör gruba tıkladığında
// "Explain root cause"a basmadan hazır bir CoSRE özeti görür; buton yine
// durur (taze/derin soruşturma için).
//
// Tasarım birebir ProblemExplainer'dan (v0.5.254 + v0.9.200):
//   - Leader-lock'lu tek işçi; 60s tik.
//   - Copilot Active() + kota devre-kesici kapıları.
//   - Tik başına küçük batch (4): exception girdisi trace + log
//     prefetch'li — problem özetinden pahalı.
//   - Surface "exception-auto-explain" → /ai sayfasında ayrı satır.
//
// Aday ölçütü inbox'ın P1 formülüyle AYNI (internal/api/inbox.go
// exceptionPriority): last_seen ≤5dk VE occurrences ≥500 — yani tam da
// "anons edilen" gruplar. Regressed gruplar da listelenir: regresyonda
// UpsertExceptionGroup bayat özeti SIFIRLAR (yanıltıcıdır — v0.9.415),
// taze+yoğun bir regresyon burada yeniden doldurulur.
const exceptionExplainerLockKey = "exception-explainer:lock"
const exceptionExplainerBatch = 4

type ExceptionExplainer struct {
	store    *chstore.Store
	logs     logstore.Store
	copilot  *copilot.Service
	leader   *cache.LeaderHolder
	interval time.Duration
	batch    int
}

func NewExceptionExplainer(store *chstore.Store, logs logstore.Store, cop *copilot.Service, lock cache.Lock) *ExceptionExplainer {
	interval := 60 * time.Second
	return &ExceptionExplainer{
		store:    store,
		logs:     logs,
		copilot:  cop,
		leader:   cache.NewLeaderHolder(lock, exceptionExplainerLockKey, cache.LeaderTTL(interval)),
		interval: interval,
		batch:    exceptionExplainerBatch,
	}
}

func (e *ExceptionExplainer) Start(ctx context.Context) {
	if e == nil || e.copilot == nil {
		return
	}
	e.leader.Start(ctx)
	t := time.NewTicker(e.interval)
	defer t.Stop()
	e.tickIfLeader(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.tickIfLeader(ctx)
		}
	}
}

func (e *ExceptionExplainer) tickIfLeader(ctx context.Context) {
	// v0.9.1138 — otomatik açıklama vidası (Settings→AI). Kapalıyken
	// yalnız BU worker susar; tıklamalı ✨ yüzeyleri etkilenmez.
	if !e.copilot.AutoExplainEnabled() {
		return
	}
	if !e.copilot.Active() {
		return
	}
	if !e.leader.IsLeader() {
		return
	}
	if e.copilot.QuotaBackoffActive() {
		return
	}
	e.run(ctx)
}

func (e *ExceptionExplainer) run(ctx context.Context) {
	// new + regressed listelenir; P1 süzgeci saf fonksiyonda. MinOccurrences
	// 500 ön-süzgeci listeyi CH tarafında küçültür (P1 şartının parçası).
	now := time.Now()
	var candidates []chstore.ExceptionGroup
	for _, state := range []string{chstore.ExStateNew, chstore.ExStateRegressed} {
		groups, err := e.store.ListExceptionGroups(ctx, chstore.ExceptionGroupFilter{
			State: state, Limit: 100, MinOccurrences: 500,
		})
		if err != nil {
			log.Printf("[exception-explainer] list %s: %v", state, err)
			continue
		}
		for _, g := range groups {
			if len(candidates) >= e.batch {
				break
			}
			if isExceptionExplainCandidate(g, now) {
				candidates = append(candidates, g)
			}
		}
	}
	filled := 0
	for i := range candidates {
		g := candidates[i]
		in := BuildExceptionExplainInput(ctx, e.store, e.logs, &g)
		cctx := copilot.WithMeta(ctx, copilot.CallMeta{
			Surface: "exception-auto-explain", UserID: "system",
			Shield: func(prompt, answer string) uint8 {
				return rca.CountUnknownEntities(rca.LowerKnownSet(), prompt, answer)
			}, // v0.10.421 (E6)
		})
		summary, err := e.copilot.Explain(cctx, copilot.SystemPromptException(), in.User)
		if err != nil {
			log.Printf("[exception-explainer] %s: %v", g.Fingerprint, err)
			continue
		}
		if strings.TrimSpace(summary) == "" {
			continue
		}
		if err := e.store.UpsertExceptionGroupAISummary(ctx, g.Fingerprint, summary); err != nil {
			log.Printf("[exception-explainer] write %s: %v", g.Fingerprint, err)
			continue
		}
		filled++
	}
	if filled > 0 {
		log.Printf("[exception-explainer] filled %d summary/ies", filled)
	}
}

// isExceptionExplainCandidate — inbox P1 formülü (exceptionPriority,
// internal/api/inbox.go) + özet-boşluğu. Saf — tablo-testli.
func isExceptionExplainCandidate(g chstore.ExceptionGroup, now time.Time) bool {
	if strings.TrimSpace(g.AISummary) != "" {
		return false
	}
	age := now.UnixNano() - g.LastSeen
	return time.Duration(age) <= 5*time.Minute && g.Occurrences >= 500
}
