import { useEffect, useRef, useState } from 'react';
import { api } from '@/lib/api';
import { fmtNum } from '@/lib/utils';

// LiveTicker — v0.5.280. Tiny "spans/sec · logs/sec · drops"
// ticker on the right edge of the Topbar. Visceral feedback
// that ingest is alive — the sort of thing operators expect
// from Datadog APM's top-of-screen activity bar.
//
// Polls /api/health every 5s, paused on document.hidden per
// CLAUDE.md. Per-sec delta is computed client-side from the
// cumulative Accepted() counters (added to the response in
// v0.5.280) so there's no need for a second server-side rate
// store. First sample renders "—" until the second sample
// arrives (delta unavailable).

interface Sample {
  at: number;        // performance.now() in ms
  spans: number;
  logs: number;
  metrics: number;
}

export function LiveTicker() {
  const [rates, setRates] = useState<{
    spans: number;
    logs: number;
    metrics: number;
    drops: number;
  } | null>(null);
  const prev = useRef<Sample | null>(null);

  useEffect(() => {
    let cancelled = false;
    const fetchOnce = async () => {
      try {
        const h = await api.health();
        if (cancelled) return;
        const now = performance.now();
        const sample: Sample = {
          at: now,
          spans: h.spans_accepted ?? 0,
          logs: h.logs_accepted ?? 0,
          metrics: h.metrics_accepted ?? 0,
        };
        if (prev.current) {
          const dt = (sample.at - prev.current.at) / 1000;
          if (dt > 0.5) {
            setRates({
              spans:   Math.max(0, (sample.spans   - prev.current.spans)   / dt),
              logs:    Math.max(0, (sample.logs    - prev.current.logs)    / dt),
              metrics: Math.max(0, (sample.metrics - prev.current.metrics) / dt),
              drops:   h.spans_dropped,
            });
          }
        }
        prev.current = sample;
      } catch {
        if (!cancelled) setRates(null);
      }
    };
    fetchOnce();
    const id = setInterval(() => {
      if (!document.hidden) fetchOnce();
    }, 5000);
    return () => { cancelled = true; clearInterval(id); };
  }, []);

  // Hide until we have a real delta — the first sample carries
  // no per-sec signal, and rendering "0 spans/s" on a busy
  // install would be misleading.
  if (!rates) return null;

  return (
    <div title="Live ingest rate (5s sample) · spans / logs / metrics per second + lifetime span drops"
      style={{
        display: 'inline-flex', alignItems: 'center', gap: 10,
        padding: '4px 10px', borderRadius: 12,
        background: 'var(--bg2)',
        border: '1px solid var(--border)',
        fontFamily: 'ui-monospace, SFMono-Regular, monospace',
        fontSize: 11, color: 'var(--text2)',
        whiteSpace: 'nowrap',
      }}>
      <span>
        <b style={{ color: 'var(--accent2)' }}>{fmtNum(Math.round(rates.spans))}</b>
        <span style={{ color: 'var(--text3)' }}> sp/s</span>
      </span>
      <span style={{ color: 'var(--border)' }}>·</span>
      <span>
        <b style={{ color: 'var(--text)' }}>{fmtNum(Math.round(rates.logs))}</b>
        <span style={{ color: 'var(--text3)' }}> lg/s</span>
      </span>
      <span style={{ color: 'var(--border)' }}>·</span>
      <span>
        <b style={{ color: 'var(--text)' }}>{fmtNum(Math.round(rates.metrics))}</b>
        <span style={{ color: 'var(--text3)' }}> mt/s</span>
      </span>
      {rates.drops > 0 && (
        <>
          <span style={{ color: 'var(--border)' }}>·</span>
          <span style={{ color: 'var(--err)' }} title="Lifetime dropped spans — back-pressure on the consumer">
            <b>{fmtNum(rates.drops)}</b> drops
          </span>
        </>
      )}
    </div>
  );
}
