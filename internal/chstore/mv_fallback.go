package chstore

import "strings"

// mvFallbackEligible answers one question: is this MV-read failure the kind the
// RAW path can actually rescue?
//
// v0.9.276. GetTraces used to fall through unconditionally, and that is what
// produced the operator's HTTP 500 on a wide window. The two failure classes
// are opposites:
//
//   - STRUCTURAL ("table doesn't exist", "unknown identifier"): the MV or one
//     of its columns is missing — an install where the migration could not run,
//     or a rolling deploy mid-flight. The raw path reads different tables and
//     genuinely rescues it. Fall through.
//
//   - RESOURCE (timeout 159, memory 241, cancelled 394): the MV read ran out of
//     budget. The raw path is then asked to do THE SAME shape of work over
//     billions of raw spans, so it cannot succeed either — it just burns the
//     client's remaining ReadTimeout and returns a transport error instead of
//     the ClickHouse error that explains what happened. Surface the original.
//
// Matching is by code where ClickHouse gives one and by message otherwise;
// clickhouse-go surfaces both in the error string. Unknown errors default to
// ELIGIBLE, preserving the historical behaviour for anything not classified —
// the point is to stop retrying the failures we KNOW are hopeless, not to
// invent a new gate for everything else.
// resourceExhaustionMarkers — the ways ClickHouse (or the transport)
// says "this query wanted more than it was allowed", as opposed to
// "this query is wrong". Shared so the two callers that care cannot
// drift apart: mvFallbackEligible (don't retry on raw, it's worse) and
// isResourceExhaustion (do retry, on a smaller window).
var resourceExhaustionMarkers = []string{
	"code: 159", // TIMEOUT_EXCEEDED / max_execution_time
	"code: 241", // MEMORY_LIMIT_EXCEEDED
	"code: 394", // QUERY_WAS_CANCELLED
	"code: 202", // TOO_MANY_SIMULTANEOUS_QUERIES
	"timeout_exceeded",
	"memory limit exceeded",
	"query was cancelled",
	// The transport giving up mid-stream.
	"i/o timeout",
	"context deadline exceeded",
}

// isResourceExhaustion reports whether an error means the query asked
// for more resources than it was granted. Such a query can succeed
// unchanged over a SMALLER window; a malformed one cannot, which is
// why the distinction has to be made before retrying anything.
func isResourceExhaustion(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, m := range resourceExhaustionMarkers {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

func mvFallbackEligible(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())

	// Resource exhaustion — retrying on raw makes it strictly worse.
	for _, hopeless := range resourceExhaustionMarkers {
		if strings.Contains(msg, hopeless) {
			return false
		}
	}
	return true
}
