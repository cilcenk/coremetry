import { Empty } from './Spinner';
import { Button } from './ui/Button';

// QueryError — v0.9.858 (UX denetimi K6). The one error state for a failed
// read, so "the query failed" can never again be rendered as "there is no
// data".
//
// The bug class this closes was the audit's widest: ~12 surfaces caught the
// error and collapsed it into the EMPTY branch. The three most expensive:
//
//   • /services printed "No services yet — point your OTLP exporter at the
//     collector". A backend outage was presented as an INSTRUMENTATION
//     problem, sending the operator to the collector for hours.
//   • /traces aggregate rendered nothing at all — no spinner, no message.
//   • /problems exception list rendered nothing at all, which reads as
//     "no exceptions, we're clean". Signal loss, in the one place that
//     exists to show signal.
//
// Plus: /alerts offered "No alert rules" + a "+ New rule" CTA, /slos "No SLOs
// defined", /monitors "No monitors yet", Settings→Channels "No channels yet",
// the metric catalogue "No metrics match", and the Service page silently
// shedding sections.
//
// The tri-state contract (`undefined` = loading, `null` = failed, value =
// data) was already documented and honoured on ~30 surfaces; these had simply
// never been converted. The template is Traces' list leg (v0.9.6xx): say it
// failed, show what the server said, offer a retry.
//
// `onRetry` is optional but strongly preferred — an error state with no way
// forward is only marginally better than a wrong empty state.
export function QueryError({ title, message, onRetry, children }: {
  /** Defaults to the Traces wording; override when a page has a better noun. */
  title?: string;
  /** Server/exception text. Rendered verbatim — operators paste these. */
  message?: string | null;
  onRetry?: () => void;
  children?: React.ReactNode;
}) {
  return (
    <Empty icon="⚠" title={title ?? 'Query failed'}
      action={onRetry ? <Button variant="secondary" size="sm" onClick={onRetry}>↻ Retry</Button> : undefined}>
      <span>
        {children ?? 'The query errored or timed out. Try a narrower time range, then retry.'}
      </span>
      {message && (
        <span className="mono" style={{
          display: 'block', fontSize: 12, color: 'var(--text2)',
          wordBreak: 'break-word', marginTop: 8,
        }}>{message}</span>
      )}
    </Empty>
  );
}

// QueryErrorInline — the same honesty in a card body / chart slot, where a
// full Empty block would blow the layout out. Used where the failing read
// backs one panel rather than the page (Service RED charts, problem-detail
// occurrence chart, sample panels).
export function QueryErrorInline({ text, onRetry }: { text?: string; onRetry?: () => void }) {
  return (
    <div style={{ color: 'var(--warn)', fontSize: 12, display: 'flex', alignItems: 'center', gap: 8 }}>
      <span>⚠ {text ?? 'Query failed — this is an error, not an empty result.'}</span>
      {onRetry && <Button variant="ghost" size="sm" onClick={onRetry}>↻ Retry</Button>}
    </div>
  );
}
