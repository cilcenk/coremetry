package chstore

import (
	"context"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/cilcenk/coremetry/internal/selfobs"
)

// tracedConn wraps a driver.Conn with OTel child spans (v0.6.42).
// Used when selfobs is enabled — the request-level span (from
// otelhttp on the api mux) becomes the parent of every CH query
// span the handler issues, giving the operator a flame-graph view
// of "where the time goes" in /trace/{id} for any inbound API
// request to Coremetry itself.
//
// Design choices:
//
//   • Embeds driver.Conn so methods we don't override (Stats,
//     ServerVersion, etc.) are promoted unchanged — keeps the
//     surface forwards-compatible with future driver versions.
//
//   • Span name is the SQL operation verb only ("clickhouse.query",
//     "clickhouse.exec", "clickhouse.batch", "clickhouse.queryrow")
//     so trace aggregation groups them sensibly. The `db.statement`
//     attribute carries the SQL (truncated to 1KB to stay under
//     the OTel attribute-size limit and avoid blowing trace payload
//     sizes on bulk INSERTs).
//
//   • Errors are recorded on the span AND wrapped through; the
//     handler still gets the same error it would have without
//     tracing — instrumentation is purely additive.
//
//   • When selfobs is disabled (noop tracer), each Start call
//     allocates a noop span which is essentially free; we don't
//     branch on selfobs.Enabled() at the per-call level. The hot
//     path overhead is one map-free no-op span allocation +
//     attribute slice — well under 1µs at the rates we run.
type tracedConn struct{ driver.Conn }

// dbSystem is the OTel semconv 'db.system' value for ClickHouse.
// Hard-coded since chstore is single-backend.
const dbSystem = "clickhouse"

func newTracedConn(c driver.Conn) driver.Conn { return &tracedConn{Conn: c} }

// truncStmt caps db.statement to keep span payload bounded. The
// alternative — recording the full SQL for a 10K-batch INSERT —
// turns every span into a multi-MB payload and overwhelms the
// collector. 1024 bytes captures the SHAPE of the query
// (operation + table + first WHERE clause) which is what an
// operator needs to grep across traces.
const maxStmtBytes = 1024

func truncStmt(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxStmtBytes {
		return s
	}
	return s[:maxStmtBytes] + "…"
}

func (t *tracedConn) Query(ctx context.Context, q string, args ...any) (driver.Rows, error) {
	ctx, span := selfobs.Tracer().Start(ctx, "clickhouse.query")
	span.SetAttributes(
		attribute.String("db.system", dbSystem),
		attribute.String("db.statement", truncStmt(q)),
	)
	defer span.End()
	rows, err := t.Conn.Query(ctx, q, args...)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return rows, err
}

func (t *tracedConn) QueryRow(ctx context.Context, q string, args ...any) driver.Row {
	ctx, span := selfobs.Tracer().Start(ctx, "clickhouse.queryrow")
	span.SetAttributes(
		attribute.String("db.system", dbSystem),
		attribute.String("db.statement", truncStmt(q)),
	)
	// QueryRow returns a Row; the Scan() happens later. We end the
	// span immediately because the Row's lazy Scan can be invoked
	// outside our reach. The span captures the dispatch, not the
	// row fetch. Acceptable trade-off for QueryRow's typical
	// "1-row lookup" use.
	defer span.End()
	return t.Conn.QueryRow(ctx, q, args...)
}

func (t *tracedConn) Exec(ctx context.Context, q string, args ...any) error {
	ctx, span := selfobs.Tracer().Start(ctx, "clickhouse.exec")
	span.SetAttributes(
		attribute.String("db.system", dbSystem),
		attribute.String("db.statement", truncStmt(q)),
	)
	defer span.End()
	if err := t.Conn.Exec(ctx, q, args...); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	return nil
}

func (t *tracedConn) PrepareBatch(ctx context.Context, q string, opts ...driver.PrepareBatchOption) (driver.Batch, error) {
	ctx, span := selfobs.Tracer().Start(ctx, "clickhouse.batch")
	span.SetAttributes(
		attribute.String("db.system", dbSystem),
		attribute.String("db.statement", truncStmt(q)),
	)
	defer span.End()
	b, err := t.Conn.PrepareBatch(ctx, q, opts...)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return b, err
}

func (t *tracedConn) AsyncInsert(ctx context.Context, q string, wait bool, args ...any) error {
	ctx, span := selfobs.Tracer().Start(ctx, "clickhouse.async_insert")
	span.SetAttributes(
		attribute.String("db.system", dbSystem),
		attribute.String("db.statement", truncStmt(q)),
		attribute.Bool("clickhouse.async_wait", wait),
	)
	defer span.End()
	if err := t.Conn.AsyncInsert(ctx, q, wait, args...); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	return nil
}
