package templater

import (
	"context"
	"log"
	"time"

	"github.com/cilcenk/coremetry/internal/cache"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/logstore"
)

// Puller is the background goroutine that drives the Drain
// templater. Each tick (default 5 min) it samples N=1000 recent
// logs from whichever backend is wired, feeds them to Drain,
// then flushes the resulting clusters into chstore.LogTemplate.
//
// Sample-based on purpose — at billion-log/day a full pass per
// tick would dominate the cluster's load. 1000 samples / 5 min
// is enough to surface every template emitting at ≥10/min
// without significant under-sampling (Drain merges on
// similarity so a slightly truncated input still produces the
// same template).
//
// Lock-gated for HA: only one replica per tick writes; the
// others skip cleanly. The lease TTL is generous (2× tick) so
// a slow tick doesn't get a second writer fighting it.
type Puller struct {
	store    *chstore.Store
	logs     logstore.Store
	interval time.Duration
	sample   int // docs per tick
	lock     cache.Lock
	drain    *Drain
}

const pullerLockKey = "coremetry:lock:templater-puller"

// New returns a puller with sane defaults. interval defaults
// to 5min; sample defaults to 1000 docs.
func New(store *chstore.Store, logs logstore.Store, interval time.Duration, sample int, lock cache.Lock) *Puller {
	if interval == 0 {
		interval = 5 * time.Minute
	}
	if sample <= 0 {
		sample = 1000
	}
	return &Puller{
		store:    store,
		logs:     logs,
		interval: interval,
		sample:   sample,
		lock:     lock,
		drain:    NewDrain(),
	}
}

// Start runs until ctx is cancelled. Each tick:
//  1. Try the HA lock; skip if another replica got it.
//  2. Pull ~sample docs from the last `interval` window.
//  3. Feed every doc to the Drain templater.
//  4. Snapshot the resulting clusters; upsert each into CH.
//  5. Reset Drain — next tick starts cold against the next
//     window. Persistent state lives in log_templates; the
//     in-memory tree is just a per-batch scratchpad.
//
// The pre-cancel select gives ctx a fair shake on shutdown so
// a long-running pull doesn't block ProcessExit.
func (p *Puller) Start(ctx context.Context) {
	if p.logs == nil || p.store == nil {
		log.Printf("[templater] no logs/store wired, puller disabled")
		return
	}
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	p.tick(ctx) // immediate first run so a fresh deploy sees results within 5min, not 10
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.tick(ctx)
		}
	}
}

func (p *Puller) tick(ctx context.Context) {
	got, err := p.lock.TryAcquire(ctx, pullerLockKey, 2*p.interval)
	if err != nil || !got {
		return
	}
	defer p.lock.Release(ctx, pullerLockKey)

	now := time.Now()
	page, err := p.logs.Search(ctx, logstore.Filter{
		From:  now.Add(-p.interval),
		To:    now,
		Limit: p.sample,
	})
	if err != nil {
		log.Printf("[templater] pull: %v", err)
		return
	}
	if len(page.Logs) == 0 {
		return
	}

	for _, r := range page.Logs {
		body := r.Body
		if body == "" {
			continue
		}
		p.drain.Add(body, r.ServiceName, r.Timestamp)
	}

	clusters := p.drain.Snapshot()
	saved := 0
	for _, c := range clusters {
		t := chstore.LogTemplate{
			ID:         c.ID,
			Template:   c.TemplateString(),
			FirstSeen:  c.FirstSeen,
			LastSeen:   c.LastSeen,
			TotalCount: c.Count,
			Services:   c.Services,
			Sample:     c.Sample,
		}
		if err := p.store.UpsertLogTemplate(ctx, t); err != nil {
			log.Printf("[templater] upsert template %s: %v", c.ID, err)
			continue
		}
		saved++
	}
	log.Printf("[templater] tick: sampled=%d clusters=%d saved=%d",
		len(page.Logs), len(clusters), saved)

	// Drop in-memory tree so the next tick processes the next
	// window cold. The persistent ledger (log_templates) carries
	// continuity — first_seen is sticky on upsert.
	p.drain.Reset()
}
