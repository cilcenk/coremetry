package templater

import (
	"crypto/sha1"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// Drain implements a faithful subset of the Drain-3 online log
// template extractor (He, Pinjia et al., "Drain: An Online Log
// Parsing Approach with Fixed Depth Tree"). Each log line walks
// a fixed-depth tree keyed by:
//
//   • Layer 1 — token count (an 11-token "Failed to connect"
//     line never collides with a 4-token "GET /foo 200" line)
//   • Layer 2 — first token (literal or "<*>")
//   • Layer 3..Depth — extended literal tokens (only the
//     non-masked positions discriminate)
//
// The leaf node holds a list of clusters; each cluster's
// template is a token list with "<*>" at variable positions.
// A new line either:
//   - hits an existing cluster (similarity ≥ SimThreshold) →
//     refine the template (mark differing positions as "<*>"),
//     bump count.
//   - hits no cluster → new cluster.
//
// Tradeoffs vs the canonical implementation:
//   • No LRU eviction of clusters — log_templates is the
//     persistent ledger so memory growth is bounded by the
//     periodic save+compact in the puller.
//   • MaxChildren is enforced at the tree, not via priority
//     eviction — once a non-leaf node has MaxChildren distinct
//     buckets we fall through to a single "<*>" child so a
//     pathologically variable layer doesn't explode the tree.
type Drain struct {
	Depth        int     // tree depth (default 4)
	MaxChildren  int     // max children per non-leaf (default 100)
	SimThreshold float64 // similarity threshold (default 0.4)

	mu   sync.Mutex
	root *node

	// v0.5.345 — pathology-watch counters. OverflowHits counts
	// every childOrCreate call that had to route through the
	// "*" wildcard because the parent had MaxChildren distinct
	// keys; ResetCount tracks how many times we've blown the
	// tree away due to runaway growth. Both surface via Stats()
	// so the puller can log per-tick health.
	overflowHits uint64
	resetCount   uint64
}

// DrainStats is the per-instance health snapshot the puller
// emits at the end of each tick. NodeCount approximates the
// in-memory tree size; OverflowHits is the cumulative count
// of childOrCreate calls that fell through to the wildcard
// child (each one is a hint that the layer's source domain
// has high cardinality the template tree can't represent
// well). High values suggest the input stream has more
// distinct shapes than MaxChildren can hold without
// over-collapsing.
type DrainStats struct {
	NodeCount    int
	ClusterCount int
	OverflowHits uint64
	ResetCount   uint64
}

// Stats returns the current health snapshot. Cheap — walks
// the tree once to count nodes + clusters.
func (d *Drain) Stats() DrainStats {
	d.mu.Lock()
	defer d.mu.Unlock()
	var nodes, clusters int
	walkCount(d.root, &nodes, &clusters)
	return DrainStats{
		NodeCount:    nodes,
		ClusterCount: clusters,
		OverflowHits: d.overflowHits,
		ResetCount:   d.resetCount,
	}
}

func walkCount(n *node, nodes, clusters *int) {
	if n == nil {
		return
	}
	*nodes++
	*clusters += len(n.clusters)
	for _, c := range n.children {
		walkCount(c, nodes, clusters)
	}
}

type node struct {
	// children keys are token literals OR the special "<*>"
	// wildcard when the actual token count exceeded MaxChildren.
	children map[string]*node
	clusters []*Cluster // only set on leaf nodes
}

// Cluster is one extracted log template plus running stats. ID
// is a stable sha1 over the template tokens — same shape +
// same order always produces the same id, so duplicate
// processing across pulls is idempotent.
type Cluster struct {
	ID         string
	Template   []string // tokens; "<*>" marks variable positions
	Count      uint64
	FirstSeen  int64 // unix ns
	LastSeen   int64 // unix ns
	Services   []string
	Sample     string // representative raw line (for UI hover)
}

// NewDrain builds a tree with the canonical Drain-3 defaults:
// depth 4, max-children 100, similarity 0.4. Tuned in the
// paper across multiple log datasets; we've kept these values
// because they're well-validated and tuning at billion-line
// scale isn't a quick test.
func NewDrain() *Drain {
	return &Drain{
		Depth:        4,
		MaxChildren:  100,
		SimThreshold: 0.4,
		root:         &node{children: map[string]*node{}},
	}
}

// Add processes one log line: tokenises + masks, walks/extends
// the tree, returns the matched (or created) cluster. Service
// + tsNs are recorded on the cluster for the periodic save
// step. Threadsafe.
func (d *Drain) Add(line, service string, tsNs int64) *Cluster {
	tokens := Tokenize(line)
	if len(tokens) == 0 {
		return nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	// Layer 1: token count
	cntKey := tokenCountKey(len(tokens))
	cur := d.childOrCreate(d.root, cntKey, d.MaxChildren)

	// Layer 2..Depth-1: token-by-token literal traversal. Last
	// layer holds the cluster list — depth=4 means 4 internal
	// levels then the cluster list at depth 4.
	for i := 0; i < d.Depth-1 && i < len(tokens); i++ {
		key := tokens[i]
		// "<*>" tokens collapse to a wildcard child so a line
		// like "<*> connection refused" + "<*> timeout exceeded"
		// fan out under one parent rather than per-mask.
		if key == "<*>" {
			key = "*"
		}
		cur = d.childOrCreate(cur, key, d.MaxChildren)
	}

	// At the leaf — find the best similarity match.
	if cur.clusters == nil {
		cur.clusters = []*Cluster{}
	}
	bestSim, bestIdx := 0.0, -1
	for i, c := range cur.clusters {
		sim := similarity(c.Template, tokens)
		if sim > bestSim {
			bestSim, bestIdx = sim, i
		}
	}
	if bestSim >= d.SimThreshold && bestIdx >= 0 {
		c := cur.clusters[bestIdx]
		// Refine: any position where new ≠ template ⇒ "<*>".
		for i := range c.Template {
			if i >= len(tokens) {
				break
			}
			if c.Template[i] != tokens[i] {
				c.Template[i] = "<*>"
			}
		}
		// Recompute ID after refinement so the stable hash
		// follows the (possibly broadened) template.
		c.ID = clusterID(c.Template)
		c.Count++
		c.LastSeen = tsNs
		addService(c, service)
		if c.Sample == "" {
			c.Sample = line
		}
		return c
	}

	// No good match → new cluster.
	template := make([]string, len(tokens))
	copy(template, tokens)
	c := &Cluster{
		ID:        clusterID(template),
		Template:  template,
		Count:     1,
		FirstSeen: tsNs,
		LastSeen:  tsNs,
		Services:  []string{},
		Sample:    line,
	}
	if service != "" {
		c.Services = append(c.Services, service)
	}
	cur.clusters = append(cur.clusters, c)
	return c
}

// Snapshot returns every cluster currently in the tree. Used by
// the puller to flush state into chstore after each batch. The
// caller treats the slice as read-only; the underlying
// Cluster pointers continue to mutate on subsequent Add() calls.
func (d *Drain) Snapshot() []*Cluster {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := []*Cluster{}
	walkClusters(d.root, &out)
	return out
}

// Reset clears the tree. Caller persists Snapshot() output
// first, then calls Reset() to bound in-process memory; the
// next pull warms a fresh tree against the recent window.
func (d *Drain) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.root = &node{children: map[string]*node{}}
	d.resetCount++
	// Don't zero overflowHits — operator wants cumulative
	// visibility on tree-cardinality pressure across the
	// run, not just the current tick.
}

func walkClusters(n *node, acc *[]*Cluster) {
	if n == nil {
		return
	}
	for _, c := range n.clusters {
		*acc = append(*acc, c)
	}
	for _, child := range n.children {
		walkClusters(child, acc)
	}
}

// childOrCreate walks one tree layer, creating a child when
// missing. MaxChildren overflow → reroute through the "*"
// wildcard so a pathological layer doesn't blow up the tree.
// The Drain pointer is threaded through (rather than a free
// function) so the overflow event can be counted on the
// instance.
func (d *Drain) childOrCreate(parent *node, key string, maxChildren int) *node {
	if c, ok := parent.children[key]; ok {
		return c
	}
	if len(parent.children) >= maxChildren {
		key = "*"
		d.overflowHits++
	}
	if c, ok := parent.children[key]; ok {
		return c
	}
	c := &node{children: map[string]*node{}}
	parent.children[key] = c
	return c
}

// similarity = (matching positions) / (total positions). Drain
// counts "<*>" template positions as matching automatically so
// a refined template doesn't penalise itself for variable
// fields. Token-count mismatch isn't possible here because the
// layer-1 split already groups by count.
func similarity(template, tokens []string) float64 {
	n := len(template)
	if n == 0 {
		return 0
	}
	if len(tokens) != n {
		return 0
	}
	matches := 0
	for i := 0; i < n; i++ {
		if template[i] == "<*>" || template[i] == tokens[i] {
			matches++
		}
	}
	return float64(matches) / float64(n)
}

// tokenCountKey strips the cardinality of the layer-1 key.
// Buckets >50 tokens into a single "50+" pool so very long
// lines (formatted JSON, multi-frame inline stack traces) don't
// fragment along count.
func tokenCountKey(n int) string {
	if n > 50 {
		return "50+"
	}
	return itoa(n)
}

func itoa(n int) string {
	// Small custom impl avoiding fmt.Sprintf for a per-line
	// hot path — masker.go already calls Sprintf for nothing
	// performance-critical, but this is the per-tokenise path.
	if n == 0 {
		return "0"
	}
	digits := "0123456789"
	out := ""
	for n > 0 {
		out = string(digits[n%10]) + out
		n /= 10
	}
	return out
}

func clusterID(template []string) string {
	h := sha1.New()
	for i, t := range template {
		if i > 0 {
			h.Write([]byte(" "))
		}
		h.Write([]byte(t))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func addService(c *Cluster, service string) {
	if service == "" {
		return
	}
	for _, s := range c.Services {
		if s == service {
			return
		}
	}
	// Cap at 5 services so the slice doesn't grow unbounded
	// on patterns that fire across the fleet.
	if len(c.Services) >= 5 {
		return
	}
	c.Services = append(c.Services, service)
}

// TemplateString joins the template tokens with single spaces —
// the canonical human-readable form for storage + display.
func (c *Cluster) TemplateString() string {
	return strings.Join(c.Template, " ")
}

// AsOfNow is a small helper for tests that need a deterministic
// timestamp; production code uses time.Now().UnixNano() inline.
var _ = time.Now
