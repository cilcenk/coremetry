// Package monitor implements the synthetic-monitoring runner — periodic
// HTTP probes + heartbeat liveness checks. State changes (up→down /
// down→up) get routed through the existing alert / Problem path so the
// notification stack delivers them with no monitor-specific plumbing.
package monitor

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/cache"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/notify"
)

const (
	// Tick interval for the runner — checks "what's due to probe right
	// now" against monitor.interval_sec. 5s gives sub-minute precision
	// without hammering the DB on idle deployments.
	tickInterval = 5 * time.Second
	// Distributed-lock key — single instance probes at a time across
	// HA replicas; otherwise each replica double-probes.
	lockKey = "coremetry:lock:monitor-runner"
	// keywordBodyLimit caps how much of a keyword monitor's response body
	// we read into memory before matching — a monitor pointed at a large
	// download must never balloon the worker.
	keywordBodyLimit = 512 * 1024 // 512 KB
	// maxProbeTimeout hard-caps any single probe so one wedged target
	// can't pin a worker goroutine indefinitely.
	maxProbeTimeout = 30 * time.Second
)

// probeTimeout resolves a monitor's per-probe timeout: the monitor's own
// TimeoutSec, defaulting to 5s when unset and clamped to maxProbeTimeout so
// a mis-configured value can't stall the loop.
func probeTimeout(sec uint16) time.Duration {
	d := time.Duration(sec) * time.Second
	if d <= 0 {
		d = 5 * time.Second
	}
	if d > maxProbeTimeout {
		d = maxProbeTimeout
	}
	return d
}

type Runner struct {
	store    *chstore.Store
	notifier *notify.Notifier
	lock     cache.Lock
	leader   *cache.LeaderHolder // v0.5.429
	cli      *http.Client

	// In-process state-change tracker — last status per monitor we
	// observed. Avoids a CH round-trip per tick to figure out "did
	// this monitor just flip?" — combined with the lockKey acquire
	// only one instance writes anyway.
	lastStatus map[string]string
}

func New(store *chstore.Store, notifier *notify.Notifier, lock cache.Lock) *Runner {
	return &Runner{
		store:    store,
		notifier: notifier,
		lock:     lock,
		leader:   cache.NewLeaderHolder(lock, lockKey, cache.LeaderTTL(tickInterval)),
		// Generous timeout — gets overridden per-monitor with the
		// monitor's TimeoutSec via http.Request context. Matches what
		// the prod tools (Pingdom, UptimeRobot, etc.) do.
		cli:        &http.Client{Timeout: 30 * time.Second},
		lastStatus: map[string]string{},
	}
}

// Start runs the runner loop until ctx is canceled. Per-tick: try to
// acquire the leader lock; if won, fetch all enabled monitors and
// probe whichever are due.
func (r *Runner) Start(ctx context.Context) {
	r.leader.Start(ctx)
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	log.Printf("[monitor] runner started (tick=%s)", tickInterval)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tick(ctx)
		}
	}
}

func (r *Runner) tick(ctx context.Context) {
	if !r.leader.IsLeader() {
		return
	}
	monitors, err := r.store.ListMonitors(ctx)
	if err != nil {
		log.Printf("[monitor] list: %v", err)
		return
	}
	last, err := r.store.LastMonitorStatus(ctx)
	if err != nil {
		log.Printf("[monitor] last status: %v", err)
		// Continue anyway with empty map — first probe of every
		// monitor will write a fresh result.
		last = map[string]chstore.MonitorResult{}
	}
	now := time.Now()
	for _, m := range monitors {
		if !m.Enabled {
			continue
		}
		prev, hasPrev := last[m.ID]
		if hasPrev {
			elapsed := now.Sub(time.Unix(0, prev.Time))
			if elapsed < time.Duration(m.IntervalSec)*time.Second {
				// Active probes (http/tcp/ssl-cert/keyword): too early to
				// probe again. Heartbeat monitors: we ALWAYS run the
				// staleness check below, since the absence of a beat is
				// what triggers the down state.
				if m.Type != "heartbeat" {
					continue
				}
			}
		}
		switch m.Type {
		case "http":
			r.probeHTTP(ctx, m, prev.Status)
		case "tcp":
			r.probeTCP(ctx, m, prev.Status)
		case "ssl-cert":
			r.probeSSLCert(ctx, m, prev.Status)
		case "keyword":
			r.probeKeyword(ctx, m, prev.Status)
		case "heartbeat":
			r.checkHeartbeat(ctx, m, prev)
		}
	}
}

func (r *Runner) probeHTTP(ctx context.Context, m chstore.Monitor, prevStatus string) {
	timeout := time.Duration(m.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	method := strings.ToUpper(m.Method)
	if method == "" {
		method = "GET"
	}
	req, err := http.NewRequestWithContext(pctx, method, m.URL, nil)
	if err != nil {
		r.record(ctx, m, "down", 0, 0, "build request: "+err.Error(), 0, prevStatus)
		return
	}
	req.Header.Set("User-Agent", "Coremetry-Monitor/1.0")
	start := time.Now()
	resp, err := r.cli.Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		r.record(ctx, m, "down", latency, 0, err.Error(), 0, prevStatus)
		return
	}
	defer resp.Body.Close()
	expected := uint16(m.ExpectedStatus)
	if expected == 0 {
		expected = 200
	}
	status := "up"
	msg := ""
	if uint16(resp.StatusCode) != expected {
		status = "down"
		msg = fmt.Sprintf("expected %d got %d", expected, resp.StatusCode)
	}
	r.record(ctx, m, status, latency, uint16(resp.StatusCode), msg, 0, prevStatus)
}

// probeTCP dials target (host:port) with the monitor's timeout. Up = the
// TCP handshake completes; latency is the connect time. A refused / timed-out
// dial is monitor-DOWN, never a worker error.
func (r *Runner) probeTCP(ctx context.Context, m chstore.Monitor, prevStatus string) {
	addr, err := NormalizeAddr(m.Target, "")
	if err != nil {
		r.record(ctx, m, "down", 0, 0, "bad target: "+err.Error(), 0, prevStatus)
		return
	}
	timeout := probeTimeout(m.TimeoutSec)
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	d := net.Dialer{Timeout: timeout}
	start := time.Now()
	conn, err := d.DialContext(pctx, "tcp", addr)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		r.record(ctx, m, "down", latency, 0, err.Error(), 0, prevStatus)
		return
	}
	_ = conn.Close()
	r.record(ctx, m, "up", latency, 0, "connected to "+addr, 0, prevStatus)
}

// probeSSLCert TLS-handshakes target (host[:443]) and inspects the leaf
// certificate's NotAfter. DOWN when expired OR days-remaining < cert_warn_days;
// records days-remaining in `detail` so the UI can show "37d left". We set
// InsecureSkipVerify: we're asserting expiry here, NOT trust-chain validity —
// a self-signed or mis-chained cert should still surface its expiry, and the
// handshake must not fail on trust grounds before we can read NotAfter.
func (r *Runner) probeSSLCert(ctx context.Context, m chstore.Monitor, prevStatus string) {
	addr, err := NormalizeAddr(m.Target, "443")
	if err != nil {
		r.record(ctx, m, "down", 0, 0, "bad target: "+err.Error(), 0, prevStatus)
		return
	}
	host, _, _ := net.SplitHostPort(addr) // addr is canonical, so this always parses
	warn := m.CertWarnDays
	if warn == 0 {
		warn = 14
	}
	timeout := probeTimeout(m.TimeoutSec)
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	td := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: timeout},
		Config: &tls.Config{
			InsecureSkipVerify: true, // expiry check, not trust-chain validation (intentional)
			ServerName:         host, // SNI so multi-cert endpoints hand back the right leaf
		},
	}
	start := time.Now()
	conn, err := td.DialContext(pctx, "tcp", addr)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		r.record(ctx, m, "down", latency, 0, "tls handshake failed: "+err.Error(), 0, prevStatus)
		return
	}
	defer conn.Close()
	// tls.Dialer always returns a *tls.Conn on success.
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		r.record(ctx, m, "down", latency, 0, "no peer certificate presented", 0, prevStatus)
		return
	}
	status, days, msg := evalCertExpiry(certs[0].NotAfter, time.Now(), warn)
	r.record(ctx, m, status, latency, 0, msg, days, prevStatus)
}

// probeKeyword GETs url (reusing the http prober's client / TLS posture) and
// asserts the response body contains keyword (or does NOT, when inverted).
// The body read is capped at keywordBodyLimit so a monitor pointed at a large
// download can never balloon the worker's memory.
func (r *Runner) probeKeyword(ctx context.Context, m chstore.Monitor, prevStatus string) {
	timeout := probeTimeout(m.TimeoutSec)
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(pctx, http.MethodGet, m.URL, nil)
	if err != nil {
		r.record(ctx, m, "down", 0, 0, "build request: "+err.Error(), 0, prevStatus)
		return
	}
	req.Header.Set("User-Agent", "Coremetry-Monitor/1.0")
	start := time.Now()
	resp, err := r.cli.Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		r.record(ctx, m, "down", latency, 0, err.Error(), 0, prevStatus)
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, keywordBodyLimit))
	if err != nil {
		r.record(ctx, m, "down", latency, uint16(resp.StatusCode), "read body: "+err.Error(), 0, prevStatus)
		return
	}
	up, msg := evalKeyword(string(body), m.Keyword, m.KeywordInvert)
	status := "down"
	if up {
		status = "up"
	}
	r.record(ctx, m, status, latency, uint16(resp.StatusCode), msg, 0, prevStatus)
}

func (r *Runner) checkHeartbeat(ctx context.Context, m chstore.Monitor, prev chstore.MonitorResult) {
	// Heartbeat-driven: the actual beat comes via /api/heartbeats/{token}
	// which writes an "up" result to monitor_results. The runner's job
	// here is to detect ABSENCE: if no beat in the last interval_sec,
	// flip to down.
	if prev.Time == 0 {
		// No beats ever received — keep status as "down" with a hint
		// so the operator knows the monitor exists but hasn't seen
		// its first beat yet.
		r.record(ctx, m, "down", 0, 0, "no heartbeat received yet", 0, "")
		return
	}
	elapsed := time.Since(time.Unix(0, prev.Time))
	grace := time.Duration(m.IntervalSec) * time.Second
	if elapsed > grace {
		// Only emit a fresh "down" row when state is changing — keeps
		// monitor_results compact for noisy missed-beat scenarios.
		if prev.Status != "down" {
			r.record(ctx, m, "down", 0, 0,
				fmt.Sprintf("no heartbeat for %s (grace %s)", elapsed.Round(time.Second), grace),
				0, prev.Status)
		}
	}
}

// record persists a probe result and, when the status flipped, opens
// or resolves a Problem so the notification stack fires. `detail` is a
// type-specific number (ssl-cert: days remaining) surfaced by the UI;
// pass 0 for types that don't use it.
func (r *Runner) record(ctx context.Context, m chstore.Monitor, status string,
	latencyMs int64, code uint16, msg string, detail int64, prevStatus string) {

	if err := r.store.InsertMonitorResult(ctx, chstore.MonitorResult{
		MonitorID: m.ID, Status: status, LatencyMs: latencyMs,
		HTTPCode: code, Message: msg, Detail: detail,
	}); err != nil {
		log.Printf("[monitor] record %s: %v", m.Name, err)
	}

	// Use the in-process tracker as a cheap "did this just change"
	// gate; falls back to prevStatus on cold start.
	cached, ok := r.lastStatus[m.ID]
	if ok {
		prevStatus = cached
	}
	r.lastStatus[m.ID] = status
	if status == prevStatus {
		return
	}
	r.handleStateChange(ctx, m, status, msg)
}

func (r *Runner) handleStateChange(ctx context.Context, m chstore.Monitor, status, msg string) {
	const ruleIDPrefix = "monitor:" // synthetic rule id — keyed by monitor for FindOpenProblem
	ruleID := ruleIDPrefix + m.ID

	switch status {
	case "down":
		// Open a fresh problem (the existing pattern stamps it with a
		// random ID; FindOpenProblem will skip if one's already open
		// for the same rule + service combo).
		existing, err := r.store.FindOpenProblem(ctx, ruleID, m.Name)
		if err == nil && existing != nil {
			return
		}
		desc := fmt.Sprintf("Synthetic %s monitor %q is DOWN.", m.Type, m.Name)
		if msg != "" {
			desc += " Reason: " + msg
		}
		switch m.Type {
		case "http", "keyword":
			desc += "\nURL: " + m.URL
		case "tcp", "ssl-cert":
			desc += "\nTarget: " + m.Target
		}
		p := chstore.Problem{
			ID:          newProblemID(),
			RuleID:      ruleID,
			RuleName:    "Monitor: " + m.Name,
			Severity:    "critical",
			Service:     m.Name,
			Metric:      "uptime",
			Value:       0,
			Threshold:   1,
			Status:      "open",
			Description: desc,
			StartedAt:   time.Now().UnixNano(),
		}
		if err := r.store.UpsertProblem(ctx, p); err != nil {
			log.Printf("[monitor] open problem: %v", err)
			return
		}
		log.Printf("[monitor] %s flipped to DOWN — opened problem", m.Name)
		if _, err := r.store.AttachProblemToIncident(ctx, p); err != nil {
			log.Printf("[monitor] incident attach: %v", err)
		}
		if r.notifier != nil {
			go r.notifier.SendProblemAlert(context.Background(), p)
		}
	case "up":
		open, err := r.store.FindOpenProblem(ctx, ruleID, m.Name)
		if err != nil || open == nil {
			return
		}
		open.Status = "resolved"
		now := time.Now().UnixNano()
		open.ResolvedAt = &now
		if err := r.store.UpsertProblem(ctx, *open); err != nil {
			log.Printf("[monitor] resolve problem: %v", err)
			return
		}
		log.Printf("[monitor] %s recovered — resolved problem", m.Name)
	}
}

func newProblemID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
