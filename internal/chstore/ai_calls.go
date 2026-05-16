package chstore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// AICall is one Copilot LLM round-trip. Recorded by copilot.Service
// on every Explain() call regardless of success — error rows show
// up in the /ai page so the operator can see "this Ollama endpoint
// is timing out half the time" instantly. Prompt/response samples
// are size-capped at the record path (see SamplePromptCap) so a
// runaway prompt can't blow up the row size.
type AICall struct {
	ID             string `json:"id"`
	CreatedAt      int64  `json:"createdAt"`      // unix ns
	Surface        string `json:"surface"`        // explain-span, explain-slo, …
	Provider       string `json:"provider"`       // openai | anthropic | github
	Model          string `json:"model"`
	BaseURL        string `json:"baseUrl,omitempty"`
	DurationMs     uint32 `json:"durationMs"`
	InputTokens    uint32 `json:"inputTokens"`
	OutputTokens   uint32 `json:"outputTokens"`
	Status         string `json:"status"`         // ok | error
	ErrorMsg       string `json:"errorMsg,omitempty"`
	PromptChars    uint32 `json:"promptChars"`
	ResponseChars  uint32 `json:"responseChars"`
	UserID         string `json:"userId,omitempty"`
	UserEmail      string `json:"userEmail,omitempty"`
	PromptSample   string `json:"promptSample,omitempty"`
	ResponseSample string `json:"responseSample,omitempty"`
}

// SamplePromptCap is the byte limit applied to prompt_sample /
// response_sample at insert time. 4KB is enough to see what the
// model was asked + how it answered without blowing up rows when
// an operator pastes a 30KB log into a custom Explain. CH compresses
// the column with ZSTD anyway so this is mostly a CH I/O guard.
const SamplePromptCap = 4 * 1024

// InsertAICall writes one row. Sync (single-row INSERT) — the
// recording happens on a goroutine in copilot.Service so the
// user-facing latency isn't impacted by CH ingest time.
func (s *Store) InsertAICall(ctx context.Context, c AICall) error {
	if c.ID == "" {
		c.ID = randHex(12)
	}
	created := time.Now().UTC()
	if c.CreatedAt > 0 {
		created = time.Unix(0, c.CreatedAt).UTC()
	}
	if len(c.PromptSample) > SamplePromptCap {
		c.PromptSample = c.PromptSample[:SamplePromptCap]
	}
	if len(c.ResponseSample) > SamplePromptCap {
		c.ResponseSample = c.ResponseSample[:SamplePromptCap]
	}
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO ai_calls
		(id, created_at, surface, provider, model, base_url,
		 duration_ms, input_tokens, output_tokens, status,
		 error_msg, prompt_chars, response_chars,
		 user_id, user_email, prompt_sample, response_sample)`)
	if err != nil {
		return err
	}
	if err := batch.Append(c.ID, created, c.Surface, c.Provider, c.Model, c.BaseURL,
		c.DurationMs, c.InputTokens, c.OutputTokens, c.Status,
		c.ErrorMsg, c.PromptChars, c.ResponseChars,
		c.UserID, c.UserEmail, c.PromptSample, c.ResponseSample); err != nil {
		return err
	}
	return batch.Send()
}

// ListAICalls returns recent calls filtered by surface/provider/
// status. Filters all optional — empty means "any". Caller-side
// pagination via since/limit; default 100 rows. Latest-first.
type ListAICallsParams struct {
	Surface  string
	Provider string
	Status   string
	From     time.Time // inclusive; zero = no lower bound
	To       time.Time // exclusive; zero = now()
	Limit    int
}

func (s *Store) ListAICalls(ctx context.Context, p ListAICallsParams) ([]AICall, error) {
	if p.Limit <= 0 || p.Limit > 1000 {
		p.Limit = 100
	}
	if p.To.IsZero() {
		p.To = time.Now().UTC()
	}
	var wc whereClause
	if !p.From.IsZero() {
		wc.add("created_at >= toDateTime64(?, 9, 'UTC')", p.From.Format(time.RFC3339Nano))
	}
	wc.add("created_at < toDateTime64(?, 9, 'UTC')", p.To.Format(time.RFC3339Nano))
	if p.Surface != "" {
		wc.add("surface = ?", p.Surface)
	}
	if p.Provider != "" {
		wc.add("provider = ?", p.Provider)
	}
	if p.Status != "" {
		wc.add("status = ?", p.Status)
	}
	q := `
		SELECT id, toUnixTimestamp64Nano(created_at), surface, provider, model, base_url,
		       duration_ms, input_tokens, output_tokens, status, error_msg,
		       prompt_chars, response_chars, user_id, user_email,
		       prompt_sample, response_sample
		FROM ai_calls ` + wc.sql() + `
		ORDER BY created_at DESC
		LIMIT ?`
	args := append(wc.args, p.Limit)
	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AICall, 0, p.Limit)
	for rows.Next() {
		var c AICall
		if err := rows.Scan(
			&c.ID, &c.CreatedAt, &c.Surface, &c.Provider, &c.Model, &c.BaseURL,
			&c.DurationMs, &c.InputTokens, &c.OutputTokens, &c.Status, &c.ErrorMsg,
			&c.PromptChars, &c.ResponseChars, &c.UserID, &c.UserEmail,
			&c.PromptSample, &c.ResponseSample,
		); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetAICall fetches one row by id — drives the drill-in panel
// where the operator inspects prompt/response in full.
func (s *Store) GetAICall(ctx context.Context, id string) (*AICall, error) {
	if id == "" {
		return nil, nil
	}
	row := s.conn.QueryRow(ctx, `
		SELECT id, toUnixTimestamp64Nano(created_at), surface, provider, model, base_url,
		       duration_ms, input_tokens, output_tokens, status, error_msg,
		       prompt_chars, response_chars, user_id, user_email,
		       prompt_sample, response_sample
		FROM ai_calls
		WHERE id = ? LIMIT 1`, id)
	var c AICall
	if err := row.Scan(
		&c.ID, &c.CreatedAt, &c.Surface, &c.Provider, &c.Model, &c.BaseURL,
		&c.DurationMs, &c.InputTokens, &c.OutputTokens, &c.Status, &c.ErrorMsg,
		&c.PromptChars, &c.ResponseChars, &c.UserID, &c.UserEmail,
		&c.PromptSample, &c.ResponseSample,
	); err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

// AIStats is the aggregate summary surfaced on the /ai overview
// cards. Computed in one ClickHouse query over the requested
// window so the page loads with KPIs visible before the table
// data streams in.
type AIStats struct {
	TotalCalls    uint64  `json:"totalCalls"`
	OkCalls       uint64  `json:"okCalls"`
	ErrorCalls    uint64  `json:"errorCalls"`
	ErrorRate     float64 `json:"errorRate"`     // 0..1
	AvgDurationMs float64 `json:"avgDurationMs"`
	P50DurationMs float64 `json:"p50DurationMs"`
	P99DurationMs float64 `json:"p99DurationMs"`
	InputTokens   uint64  `json:"inputTokens"`
	OutputTokens  uint64  `json:"outputTokens"`
	DistinctUsers uint64  `json:"distinctUsers"`
	BySurface     []AISurfaceStat `json:"bySurface"`
	ByProvider    []AIProviderStat `json:"byProvider"`
}

type AISurfaceStat struct {
	Surface   string `json:"surface"`
	Calls     uint64 `json:"calls"`
	ErrorRate float64 `json:"errorRate"`
	AvgMs     float64 `json:"avgMs"`
}

type AIProviderStat struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Calls        uint64 `json:"calls"`
	InputTokens  uint64 `json:"inputTokens"`
	OutputTokens uint64 `json:"outputTokens"`
}

// ComputeAIStats does the aggregate query for the overview cards.
// Window-bounded (from..to) so we never scan beyond the TTL.
func (s *Store) ComputeAIStats(ctx context.Context, from, to time.Time) (*AIStats, error) {
	if to.IsZero() {
		to = time.Now().UTC()
	}
	if from.IsZero() {
		from = to.Add(-24 * time.Hour)
	}
	row := s.conn.QueryRow(ctx, `
		SELECT
			toUInt64(count()),
			toUInt64(countIf(status = 'ok')),
			toUInt64(countIf(status = 'error')),
			coalesce(toFloat64(avg(duration_ms)), 0),
			coalesce(toFloat64(quantile(0.5)(toFloat64(duration_ms))), 0),
			coalesce(toFloat64(quantile(0.99)(toFloat64(duration_ms))), 0),
			toUInt64(sum(input_tokens)),
			toUInt64(sum(output_tokens)),
			toUInt64(uniqExact(user_id))
		FROM ai_calls
		WHERE created_at >= toDateTime64(?, 9, 'UTC')
		  AND created_at <  toDateTime64(?, 9, 'UTC')`,
		from.Format(time.RFC3339Nano), to.Format(time.RFC3339Nano))
	var st AIStats
	if err := row.Scan(&st.TotalCalls, &st.OkCalls, &st.ErrorCalls,
		&st.AvgDurationMs, &st.P50DurationMs, &st.P99DurationMs,
		&st.InputTokens, &st.OutputTokens, &st.DistinctUsers); err != nil {
		return nil, err
	}
	if st.TotalCalls > 0 {
		st.ErrorRate = float64(st.ErrorCalls) / float64(st.TotalCalls)
	}

	// Per-surface breakdown — operator wants "which Explain button
	// gets the most clicks / has the highest error rate".
	sRows, err := s.conn.Query(ctx, `
		SELECT surface,
		       toUInt64(count()),
		       coalesce(toFloat64(countIf(status = 'error')) / nullIf(count(), 0), 0),
		       coalesce(toFloat64(avg(duration_ms)), 0)
		FROM ai_calls
		WHERE created_at >= toDateTime64(?, 9, 'UTC')
		  AND created_at <  toDateTime64(?, 9, 'UTC')
		GROUP BY surface
		ORDER BY count() DESC
		LIMIT 20`,
		from.Format(time.RFC3339Nano), to.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	for sRows.Next() {
		var row AISurfaceStat
		if err := sRows.Scan(&row.Surface, &row.Calls, &row.ErrorRate, &row.AvgMs); err != nil {
			sRows.Close()
			return nil, err
		}
		st.BySurface = append(st.BySurface, row)
	}
	sRows.Close()

	pRows, err := s.conn.Query(ctx, `
		SELECT provider, model,
		       toUInt64(count()),
		       toUInt64(sum(input_tokens)),
		       toUInt64(sum(output_tokens))
		FROM ai_calls
		WHERE created_at >= toDateTime64(?, 9, 'UTC')
		  AND created_at <  toDateTime64(?, 9, 'UTC')
		GROUP BY provider, model
		ORDER BY count() DESC
		LIMIT 20`,
		from.Format(time.RFC3339Nano), to.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	for pRows.Next() {
		var row AIProviderStat
		if err := pRows.Scan(&row.Provider, &row.Model, &row.Calls,
			&row.InputTokens, &row.OutputTokens); err != nil {
			pRows.Close()
			return nil, err
		}
		st.ByProvider = append(st.ByProvider, row)
	}
	pRows.Close()
	return &st, nil
}

// AICallsTimeseries is one bucket of the volume-by-time chart.
// Granularity is determined client-side based on window length;
// for v1 we default to 5-minute buckets and let the renderer
// re-bin if it wants coarser resolution.
type AICallsTimePoint struct {
	Time        int64   `json:"time"`        // unix ns, bucket start
	Calls       uint64  `json:"calls"`
	Errors      uint64  `json:"errors"`
	AvgMs       float64 `json:"avgMs"`
	InputTokens uint64  `json:"inputTokens"`
	OutputTokens uint64 `json:"outputTokens"`
}

func (s *Store) AICallsTimeseries(ctx context.Context, from, to time.Time, bucketSec int) ([]AICallsTimePoint, error) {
	if to.IsZero() {
		to = time.Now().UTC()
	}
	if from.IsZero() {
		from = to.Add(-24 * time.Hour)
	}
	if bucketSec <= 0 {
		bucketSec = 300
	}
	q := fmt.Sprintf(`
		SELECT toStartOfInterval(created_at, INTERVAL %d second) AS bucket,
		       toUInt64(count()) AS calls,
		       toUInt64(countIf(status = 'error')) AS errors,
		       coalesce(toFloat64(avg(duration_ms)), 0) AS avg_ms,
		       toUInt64(sum(input_tokens)) AS in_tok,
		       toUInt64(sum(output_tokens)) AS out_tok
		FROM ai_calls
		WHERE created_at >= toDateTime64(?, 9, 'UTC')
		  AND created_at <  toDateTime64(?, 9, 'UTC')
		GROUP BY bucket
		ORDER BY bucket`, bucketSec)
	rows, err := s.conn.Query(ctx, q,
		from.Format(time.RFC3339Nano), to.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AICallsTimePoint
	for rows.Next() {
		var bucket time.Time
		var p AICallsTimePoint
		if err := rows.Scan(&bucket, &p.Calls, &p.Errors, &p.AvgMs,
			&p.InputTokens, &p.OutputTokens); err != nil {
			return nil, err
		}
		p.Time = bucket.UnixNano()
		out = append(out, p)
	}
	return out, rows.Err()
}
