package chstore

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
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
	CreatedAt      int64  `json:"createdAt"`            // unix ns
	Surface        string `json:"surface"`              // explain-span, explain-slo, …
	ExchangeID     string `json:"exchangeId,omitempty"` // v0.8.399 — feedback correlation key ('' pre-v0.8.399 / non-chat)
	Provider       string `json:"provider"`             // openai | anthropic | github
	Model          string `json:"model"`
	BaseURL        string `json:"baseUrl,omitempty"`
	DurationMs     uint32 `json:"durationMs"`
	InputTokens    uint32 `json:"inputTokens"`
	OutputTokens   uint32 `json:"outputTokens"`
	Status         string `json:"status"` // ok | error
	ErrorMsg       string `json:"errorMsg,omitempty"`
	PromptChars    uint32 `json:"promptChars"`
	ResponseChars  uint32 `json:"responseChars"`
	UserID         string `json:"userId,omitempty"`
	UserEmail      string `json:"userEmail,omitempty"`
	PromptSample   string `json:"promptSample,omitempty"`
	ResponseSample string `json:"responseSample,omitempty"`
	// v0.10.409 (CoSRE denetimi E2/O3/O4) — prompt sürümü (prompts.go
	// sabitlerinin FNV'si), kullanılan profil, hata SINIFI (timeout /
	// quota / refusal / context_overflow / http_4xx / http_5xx /
	// unavailable / other), ilk token gecikmesi, akış→buffered düşüşü ve
	// (E6 için hazır) kalkan isabeti. Kolonlar boot ALTER'ıyla gelir;
	// INSERT probe görene dek eski kolon listesini kullanır (iki-boot).
	PromptVersion  string `json:"promptVersion,omitempty"`
	ProfileID      string `json:"profileId,omitempty"`
	ErrorClass     string `json:"errorClass,omitempty"`
	TTFTMs         uint32 `json:"ttftMs,omitempty"`
	StreamFallback bool   `json:"streamFallback,omitempty"`
	ShieldHits     uint8  `json:"shieldHits,omitempty"`
}

// aiCallsExtended — v0.10.409 kolonları bu kurulumda var mı (boot probe;
// küme kipinde DDL ertelenir, ilk boot false okur → eski INSERT).
var aiCallsExtended atomic.Bool

// AICallsExtended — /admin/stats + stats sorguları için.
func AICallsExtended() bool { return aiCallsExtended.Load() }

// probeAICallsColumns — INSERT hedefi olan ai_calls'ta (küme kipinde
// Distributed) error_class var mı. Yalnız INSERT hedefine bakılır: yerel
// parçada olup Distributed'da henüz olmayan kolon INSERT'i kırar.
func (s *Store) probeAICallsColumns(ctx context.Context) bool {
	var n uint64
	err := s.conn.QueryRow(ctx,
		`SELECT count() FROM system.columns
		 WHERE database = currentDatabase() AND table = 'ai_calls' AND name = 'error_class'`).Scan(&n)
	ok := err == nil && n > 0
	aiCallsExtended.Store(ok)
	return ok
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
// aiCallsBaseCols — v0.10.409 öncesi kolon listesi. aiCallsExtCols yalnız
// boot probe'u kolonları GÖRDÜYSE eklenir (iki-boot sözleşmesi): küme
// kipinde ertelenen ALTER inene dek INSERT bu altı adı ANMAZ — Distributed
// tablo tanımadığı kolonu reddeder ve her AI çağrısı kaydı kaybolurdu.
const aiCallsBaseCols = `id, created_at, surface, exchange_id, provider, model, base_url,
		 duration_ms, input_tokens, output_tokens, status,
		 error_msg, prompt_chars, response_chars,
		 user_id, user_email, prompt_sample, response_sample`

const aiCallsExtCols = `prompt_version, profile_id, error_class, ttft_ms, stream_fallback, shield_hits`

// aiCallsInsertSQL — saf: kolon listesi bayrağa göre (ai_calls_insert_test).
func aiCallsInsertSQL(extended bool) string {
	cols := aiCallsBaseCols
	if extended {
		cols += ",\n\t\t " + aiCallsExtCols
	}
	return "INSERT INTO ai_calls (" + cols + ")"
}

// aiCallsInsertArgs — saf: aiCallsInsertSQL kolon sırasıyla birebir.
func aiCallsInsertArgs(c AICall, created time.Time, extended bool) []any {
	args := []any{c.ID, created, c.Surface, c.ExchangeID, c.Provider, c.Model, c.BaseURL,
		c.DurationMs, c.InputTokens, c.OutputTokens, c.Status,
		c.ErrorMsg, c.PromptChars, c.ResponseChars,
		c.UserID, c.UserEmail, c.PromptSample, c.ResponseSample}
	if extended {
		args = append(args, c.PromptVersion, c.ProfileID, c.ErrorClass, c.TTFTMs, boolU8(c.StreamFallback), c.ShieldHits)
	}
	return args
}

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
	// v0.10.409 — tek dal, bayrak yalnız kolon listesini seçer.
	ext := aiCallsExtended.Load()
	batch, err := s.conn.PrepareBatch(ctx, aiCallsInsertSQL(ext))
	if err != nil {
		return err
	}
	if err := batch.Append(aiCallsInsertArgs(c, created, ext)...); err != nil {
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
		wc.add("created_at >= toDateTime64(?, 9, 'UTC')", chDateTime64Arg(p.From))
	}
	wc.add("created_at < toDateTime64(?, 9, 'UTC')", chDateTime64Arg(p.To))
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
		SELECT id, toUnixTimestamp64Nano(created_at), surface, exchange_id, provider, model, base_url,
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
			&c.ID, &c.CreatedAt, &c.Surface, &c.ExchangeID, &c.Provider, &c.Model, &c.BaseURL,
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
		SELECT id, toUnixTimestamp64Nano(created_at), surface, exchange_id, provider, model, base_url,
		       duration_ms, input_tokens, output_tokens, status, error_msg,
		       prompt_chars, response_chars, user_id, user_email,
		       prompt_sample, response_sample
		FROM ai_calls
		WHERE id = ? LIMIT 1`, id)
	var c AICall
	if err := row.Scan(
		&c.ID, &c.CreatedAt, &c.Surface, &c.ExchangeID, &c.Provider, &c.Model, &c.BaseURL,
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
	TotalCalls    uint64           `json:"totalCalls"`
	OkCalls       uint64           `json:"okCalls"`
	ErrorCalls    uint64           `json:"errorCalls"`
	ErrorRate     float64          `json:"errorRate"` // 0..1
	AvgDurationMs float64          `json:"avgDurationMs"`
	P50DurationMs float64          `json:"p50DurationMs"`
	P99DurationMs float64          `json:"p99DurationMs"`
	InputTokens   uint64           `json:"inputTokens"`
	OutputTokens  uint64           `json:"outputTokens"`
	DistinctUsers uint64           `json:"distinctUsers"`
	BySurface     []AISurfaceStat  `json:"bySurface"`
	ByProvider    []AIProviderStat `json:"byProvider"`
	// v0.10.409 — hata sınıfı kırılımı ve ortalama ilk-token gecikmesi;
	// yalnız genişletilmiş kolonlar varken dolar (AICallsExtended).
	ByErrorClass []AIErrorClassStat `json:"byErrorClass,omitempty"`
	AvgTTFTMs    float64            `json:"avgTtftMs,omitempty"`
	// v0.10.421 (E6) — kalkan isabeti: en az bir uydurma ad taşıyan çağrı
	// sayısı ve toplam ad sayısı (yalnız genişletilmiş kolonlar varken).
	ShieldHitCalls uint64 `json:"shieldHitCalls,omitempty"`
	ShieldHits     uint64 `json:"shieldHits,omitempty"`
	Extended       bool   `json:"extended"`
}

type AISurfaceStat struct {
	Surface   string  `json:"surface"`
	Calls     uint64  `json:"calls"`
	ErrorRate float64 `json:"errorRate"`
	AvgMs     float64 `json:"avgMs"`
	// v0.8.399 — operator thumbs up/down quality signal, merged in
	// from ai_feedback (latest verdict per exchange wins). Zero
	// FeedbackCount = no ratings in the window; ThumbsUpRate is only
	// meaningful when FeedbackCount > 0 (omitempty on both keeps the
	// old payload shape for unrated surfaces).
	FeedbackCount uint64  `json:"feedbackCount,omitempty"`
	ThumbsUpRate  float64 `json:"thumbsUpRate,omitempty"` // 0..1 over rated exchanges
}

// AIErrorClassStat — v0.10.409: hata sınıfı kırılımı (kolon varken).
type AIErrorClassStat struct {
	Class string `json:"class"`
	Calls uint64 `json:"calls"`
}

type AIProviderStat struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Calls        uint64 `json:"calls"`
	InputTokens  uint64 `json:"inputTokens"`
	OutputTokens uint64 `json:"outputTokens"`
	// v0.10.400 (CoSRE denetimi O5/E3) — model başına kalite: iki profil
	// (yerel + bulut) yan yana koşarken "hangisi yavaş, hangisi hata
	// veriyor" sorusu bu üç alandan okunur.
	Errors uint64  `json:"errors"`
	AvgMs  float64 `json:"avgMs"`
	P95Ms  float64 `json:"p95Ms"`
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
		chDateTime64Arg(from), chDateTime64Arg(to))
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
		chDateTime64Arg(from), chDateTime64Arg(to))
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

	// v0.8.399 — thumbs up/down quality per surface, JOIN-free: one
	// small second read over the tiny ai_feedback state table (FINAL
	// so the latest verdict per exchange wins), merged into the
	// surface rows in Go. Surfaces with feedback but no calls in the
	// window are deliberately dropped — the /ai table is call-driven.
	fb, err := s.aiFeedbackBySurface(ctx, from, to)
	if err != nil {
		return nil, err
	}
	for i := range st.BySurface {
		if agg, ok := fb[st.BySurface[i].Surface]; ok && agg.Total > 0 {
			st.BySurface[i].FeedbackCount = agg.Total
			st.BySurface[i].ThumbsUpRate = float64(agg.Up) / float64(agg.Total)
		}
	}

	pRows, err := s.conn.Query(ctx, `
		SELECT provider, model,
		       toUInt64(count()),
		       toUInt64(sum(input_tokens)),
		       toUInt64(sum(output_tokens)),
		       toUInt64(countIf(status = 'error')),
		       toFloat64(avg(duration_ms)),
		       toFloat64(quantile(0.95)(duration_ms))
		FROM ai_calls
		WHERE created_at >= toDateTime64(?, 9, 'UTC')
		  AND created_at <  toDateTime64(?, 9, 'UTC')
		GROUP BY provider, model
		ORDER BY count() DESC
		LIMIT 20`,
		chDateTime64Arg(from), chDateTime64Arg(to))
	if err != nil {
		return nil, err
	}
	for pRows.Next() {
		var row AIProviderStat
		if err := pRows.Scan(&row.Provider, &row.Model, &row.Calls,
			&row.InputTokens, &row.OutputTokens, &row.Errors, &row.AvgMs, &row.P95Ms); err != nil {
			pRows.Close()
			return nil, err
		}
		st.ByProvider = append(st.ByProvider, row)
	}
	pRows.Close()
	// v0.10.409 — hata sınıfı + TTFT (kolonlar varsa).
	if aiCallsExtended.Load() {
		st.Extended = true
		eRows, err := s.conn.Query(ctx, `
			SELECT error_class, toUInt64(count())
			FROM ai_calls
			WHERE created_at >= toDateTime64(?, 9, 'UTC')
			  AND created_at <  toDateTime64(?, 9, 'UTC')
			  AND status = 'error'
			GROUP BY error_class ORDER BY count() DESC LIMIT 12`,
			chDateTime64Arg(from), chDateTime64Arg(to))
		if err != nil {
			log.Printf("[ai_calls] hata sınıfı kırılımı okunamadı: %v", err)
		} else {
			for eRows.Next() {
				var row AIErrorClassStat
				if err := eRows.Scan(&row.Class, &row.Calls); err == nil {
					if row.Class == "" {
						row.Class = "unclassified"
					}
					st.ByErrorClass = append(st.ByErrorClass, row)
				}
			}
			eRows.Close()
		}
		var ttft float64
		if err := s.conn.QueryRow(ctx, `
			SELECT ifNotFinite(toFloat64(avgIf(ttft_ms, ttft_ms > 0)), 0)
			FROM ai_calls
			WHERE created_at >= toDateTime64(?, 9, 'UTC')
			  AND created_at <  toDateTime64(?, 9, 'UTC')`,
			chDateTime64Arg(from), chDateTime64Arg(to)).Scan(&ttft); err == nil {
			st.AvgTTFTMs = ttft
		}
		// v0.10.421 (E6) — kalkan isabeti.
		if err := s.conn.QueryRow(ctx, `
			SELECT toUInt64(countIf(shield_hits > 0)), toUInt64(sum(shield_hits))
			FROM ai_calls
			WHERE created_at >= toDateTime64(?, 9, 'UTC')
			  AND created_at <  toDateTime64(?, 9, 'UTC')`,
			chDateTime64Arg(from), chDateTime64Arg(to)).Scan(&st.ShieldHitCalls, &st.ShieldHits); err != nil {
			log.Printf("[ai_calls] kalkan isabeti okunamadı: %v", err)
		}
	}
	return &st, nil
}

// AICallsTimeseries is one bucket of the volume-by-time chart.
// Granularity is determined client-side based on window length;
// for v1 we default to 5-minute buckets and let the renderer
// re-bin if it wants coarser resolution.
type AICallsTimePoint struct {
	Time         int64   `json:"time"` // unix ns, bucket start
	Calls        uint64  `json:"calls"`
	Errors       uint64  `json:"errors"`
	AvgMs        float64 `json:"avgMs"`
	InputTokens  uint64  `json:"inputTokens"`
	OutputTokens uint64  `json:"outputTokens"`
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
		chDateTime64Arg(from), chDateTime64Arg(to))
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

func boolU8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

// AICallEvalRow — v0.10.423 (CoSRE denetimi E5): bir 👎 satırının evalset
// vakasına dönüşmesi için KAYNAKTAN okunan çağrı. Genişletilmiş kolonlar
// (prompt_version/profile_id/error_class) yalnız probe gördüyse okunur —
// iki-boot sözleşmesi (küme kipinde ilk boot'ta kolon yok).
type AICallEvalRow struct {
	Surface       string
	Provider      string
	Model         string
	CreatedAt     time.Time
	PromptChars   uint32
	ResponseChars uint32
	Prompt        string
	Response      string
	PromptVersion string
	ProfileID     string
	ErrorClass    string
}

// aiCallEvalSelect — saf: kolon listesi bayrağa göre (ai_calls_insert_test kardeşi).
func aiCallEvalSelect(extended bool) string {
	cols := "surface, provider, model, created_at, prompt_chars, response_chars, prompt_sample, response_sample"
	if extended {
		cols += ", prompt_version, profile_id, error_class"
	}
	return "SELECT " + cols + " FROM ai_calls WHERE exchange_id = ? ORDER BY created_at DESC LIMIT 1 SETTINGS max_execution_time = 5"
}

// AICallForEvalset — exchange kimliğiyle nokta okuma; yoksa nil, nil.
func (s *Store) AICallForEvalset(ctx context.Context, exchangeID string) (*AICallEvalRow, error) {
	if exchangeID == "" {
		return nil, nil
	}
	ext := aiCallsExtended.Load()
	rows, err := s.conn.Query(ctx, aiCallEvalSelect(ext), exchangeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var r AICallEvalRow
	dest := []any{&r.Surface, &r.Provider, &r.Model, &r.CreatedAt, &r.PromptChars, &r.ResponseChars, &r.Prompt, &r.Response}
	if ext {
		dest = append(dest, &r.PromptVersion, &r.ProfileID, &r.ErrorClass)
	}
	if err := rows.Scan(dest...); err != nil {
		return nil, err
	}
	return &r, nil
}
