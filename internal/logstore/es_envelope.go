package logstore

import "fmt"

// The honesty envelope (v0.9.288) — the partial-result signal every ES
// search response carries, and that this package used to decode nowhere.
//
// Each log query is sent with a SOFT timeout (esTimeoutFromEnv, 10s by
// default). A soft timeout does not fail the request: ES returns
// whatever it finished computing and sets `timed_out: true`. Shard
// failures behave the same way — `_shards.failed` counts them and the
// results simply omit those shards. Neither field was read, so a
// partial top-N and partial buckets reached the operator indistinguishable
// from a complete answer.
//
// Embed esSearchEnvelope in every ES search decode struct. It is
// deliberately a named, shared type rather than four hand-copied field
// sets: the risk called out when this was designed is that one decode
// site gets forgotten and quietly keeps telling the old lie.
// TestEverySearchDecodeCarriesTheEnvelope enforces that.
type esSearchEnvelope struct {
	TimedOut bool `json:"timed_out"`
	Shards   struct {
		Total      int `json:"total"`
		Successful int `json:"successful"`
		Failed     int `json:"failed"`
		Skipped    int `json:"skipped"`
	} `json:"_shards"`
}

// partial reports whether the answer is a subset of the true result.
//
// Two independent causes, either one is enough: the soft timeout fired,
// or shards did not answer. `skipped` is NOT a cause — a skipped shard
// is one the can_match phase proved cannot hold matching documents, so
// omitting it costs nothing. Counting skips as partial would mark every
// well-pruned query partial, i.e. exactly the queries that worked best.
func (e esSearchEnvelope) partial() bool {
	return e.TimedOut || e.Shards.Failed > 0
}

// describe renders the envelope for a pod log line. Returns "" when the
// answer is complete, so callers can `if s := env.describe(); s != ""`.
func (e esSearchEnvelope) describe() string {
	if !e.partial() {
		return ""
	}
	switch {
	case e.TimedOut && e.Shards.Failed > 0:
		return fmt.Sprintf("soft timeout AND %d/%d shards failed", e.Shards.Failed, e.Shards.Total)
	case e.TimedOut:
		return "soft timeout — ES returned what it had computed so far"
	default:
		return fmt.Sprintf("%d/%d shards failed", e.Shards.Failed, e.Shards.Total)
	}
}

// esTotal is the `hits.total` object. The Relation half is what makes
// Total honest: ES is asked for track_total_hits: 10000, so once the
// match count reaches that cap it answers relation "gte" and stops
// counting. Decoding only Value turned "at least 10,000" into "exactly
// 10,000" — and the very same number from the ClickHouse backend IS an
// exact count().
type esTotal struct {
	Value    int    `json:"value"`
	Relation string `json:"relation"`
}

// isLowerBound reports whether Value is a floor rather than the answer.
// ES sends "eq" or "gte"; an ABSENT relation (older clusters, or a
// response shape that predates track_total_hits) is treated as exact,
// which is the pre-v0.9.288 behaviour — the envelope must not invent
// uncertainty it has no evidence for.
func (t esTotal) isLowerBound() bool {
	return t.Relation == "gte"
}
