// Package elasticml polls Elastic ML anomaly-detection jobs and
// ingests significant records into Coremetry's anomaly_events table.
// Read-only against Elastic — every job stays under Elastic's
// management; Coremetry just surfaces records as anomaly rows so
// operators get one triage surface across native + Elastic
// detections.
package elasticml

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/elastic/go-elasticsearch/v8"

	"github.com/cilcenk/coremetry/internal/cache"
	"github.com/cilcenk/coremetry/internal/chstore"
)

const (
	lockKey            = "elasticml-poller-leader"
	defaultInterval    = 5 * time.Minute
	defaultMinScore    = 75.0
	defaultStartupWait = 60 * time.Second
)

// Config holds the connection bits + tunables. Addresses /
// Username / Password / APIKey mirror the logstore ES config —
// the operator points us at the same cluster they're already
// querying for logs.
type Config struct {
	Addresses          []string
	Username, Password string
	APIKey             string
	InsecureSkipVerify bool
	Interval           time.Duration
	MinScore           float64
}

// Poller is the long-running background worker. tick() pulls the
// current ML job list and, for each open job, fetches recent
// high-score records and upserts them into anomaly_events.
// Lock-elect makes HA-safe; the lock TTL is 2× interval so a slow
// run doesn't kill liveness.
type Poller struct {
	cli      *elasticsearch.Client
	store    *chstore.Store
	interval time.Duration
	minScore float64
	lock     cache.Lock
	leader   *cache.LeaderHolder // v0.5.429
}

func New(cfg Config, store *chstore.Store, lock cache.Lock) (*Poller, error) {
	if cfg.Interval <= 0 {
		cfg.Interval = defaultInterval
	}
	if cfg.MinScore <= 0 {
		cfg.MinScore = defaultMinScore
	}
	transport := &http.Transport{}
	if cfg.InsecureSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	cli, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: cfg.Addresses,
		Username:  cfg.Username,
		Password:  cfg.Password,
		APIKey:    cfg.APIKey,
		Transport: transport,
	})
	if err != nil {
		return nil, fmt.Errorf("elasticml: client: %w", err)
	}
	if lock == nil {
		_, lock = cache.NewNoop()
	}
	return &Poller{
		cli: cli, store: store,
		interval: cfg.Interval, minScore: cfg.MinScore,
		lock:   lock,
		leader: cache.NewLeaderHolder(lock, lockKey, cache.LeaderTTL(cfg.Interval)),
	}, nil
}

func (p *Poller) Start(ctx context.Context) {
	p.leader.Start(ctx)
	go func() {
		// Initial warmup so we don't slam ES while it's also
		// warming on a cold rolling restart, and so we give the
		// existing ML jobs at least one bucket span past coremetry
		// startup before reading.
		select {
		case <-ctx.Done():
			return
		case <-time.After(defaultStartupWait):
		}
		p.tick(ctx)
		t := time.NewTicker(p.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				p.tick(ctx)
			}
		}
	}()
}

func (p *Poller) tick(ctx context.Context) {
	if !p.leader.IsLeader() {
		return
	}

	jobs, err := p.listJobs(ctx)
	if err != nil {
		log.Printf("[elastic-ml] list jobs: %v", err)
		return
	}
	if len(jobs) == 0 {
		return
	}
	for _, jobID := range jobs {
		if err := p.pollJob(ctx, jobID); err != nil {
			log.Printf("[elastic-ml] poll job %s: %v", jobID, err)
		}
	}
}

// listJobs returns the IDs of open (running) ML anomaly detectors.
// Open jobs are the only ones writing recent results — closed jobs
// have no fresh records to ingest. /_ml/anomaly_detectors returns
// the catalog; we filter on stats from /_ml/anomaly_detectors/_stats.
func (p *Poller) listJobs(ctx context.Context) ([]string, error) {
	type statsResp struct {
		Jobs []struct {
			JobID string `json:"job_id"`
			State string `json:"state"`
		} `json:"jobs"`
	}
	res, err := p.cli.Transport.Perform(mustReq(ctx, "GET",
		"/_ml/anomaly_detectors/_stats", nil))
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("ml stats: %s", res.Status)
	}
	var body statsResp
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(body.Jobs))
	for _, j := range body.Jobs {
		if j.State == "opened" {
			out = append(out, j.JobID)
		}
	}
	return out, nil
}

// pollJob fetches the last 2 polling intervals worth of records
// for one job and upserts every record with score >= minScore.
// Overlapping the window by one interval guards against races
// where a record lands a few seconds after our last poll.
func (p *Poller) pollJob(ctx context.Context, jobID string) error {
	startMs := time.Now().Add(-2 * p.interval).UnixMilli()
	body := map[string]any{
		"page":         map[string]any{"size": 100},
		"record_score": p.minScore,
		"start":        startMs,
		"desc":         true,
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return err
	}
	req := mustReq(ctx, "POST",
		"/_ml/anomaly_detectors/"+jobID+"/results/records",
		&buf)
	req.Header.Set("Content-Type", "application/json")
	res, err := p.cli.Transport.Perform(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return fmt.Errorf("get records: %s", res.Status)
	}
	type record struct {
		RecordScore         float64   `json:"record_score"`
		Timestamp           int64     `json:"timestamp"`
		BucketSpan          int       `json:"bucket_span"`
		JobID               string    `json:"job_id"`
		PartitionFieldValue string    `json:"partition_field_value"`
		ByFieldValue        string    `json:"by_field_value"`
		FunctionDescription string    `json:"function_description"`
		Typical             []float64 `json:"typical"`
		Actual              []float64 `json:"actual"`
	}
	var resp struct {
		Records []record `json:"records"`
	}
	if err := json.NewDecoder(res.Body).Decode(&resp); err != nil {
		return err
	}
	for _, r := range resp.Records {
		service := r.PartitionFieldValue
		if service == "" {
			service = r.ByFieldValue
		}
		pattern := jobID
		if r.FunctionDescription != "" {
			pattern = jobID + ":" + r.FunctionDescription
		}
		startNs := r.Timestamp * int64(time.Millisecond)
		bucketNs := int64(r.BucketSpan) * int64(time.Second)
		endNs := startNs + bucketNs
		sample := ""
		if len(r.Actual) > 0 && len(r.Typical) > 0 {
			sample = fmt.Sprintf("actual=%.2f typical=%.2f score=%.0f",
				r.Actual[0], r.Typical[0], r.RecordScore)
		} else {
			sample = fmt.Sprintf("score=%.0f", r.RecordScore)
		}
		ev := chstore.AnomalyEvent{
			ID:           chstore.FingerprintAnomaly("elastic_ml", pattern, service),
			Kind:         "elastic_ml",
			Pattern:      pattern,
			Service:      service,
			StartedAt:    startNs,
			LastSeen:     endNs,
			PeakRatio:    r.RecordScore / 100.0,
			CurrentRatio: r.RecordScore / 100.0,
			CurrentCount: 1,
			Sample:       sample,
		}
		if err := p.store.UpsertAnomalyEvent(ctx, ev); err != nil {
			log.Printf("[elastic-ml] upsert %s: %v", ev.ID, err)
		}
	}
	return nil
}

func mustReq(ctx context.Context, method, path string, body *bytes.Buffer) *http.Request {
	var b *bytes.Buffer
	if body != nil {
		b = body
	}
	var r *http.Request
	if b != nil {
		r, _ = http.NewRequestWithContext(ctx, method, path, b)
	} else {
		r, _ = http.NewRequestWithContext(ctx, method, path, nil)
	}
	return r
}
