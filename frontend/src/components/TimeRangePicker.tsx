'use client';
import { useEffect, useRef, useState } from 'react';
import type { TimeRange } from '@/lib/types';
import { PRESET_LABELS, PRESET_SECONDS, timeRangeLabel } from '@/lib/utils';

const PRESETS = Object.keys(PRESET_SECONDS);

export function TimeRangePicker({ value, onChange }: {
  value: TimeRange;
  onChange: (r: TimeRange) => void;
}) {
  const [open, setOpen] = useState(false);
  const [fromInput, setFromInput] = useState('');
  const [toInput, setToInput] = useState('');
  const [error, setError] = useState('');
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onDoc);
    return () => document.removeEventListener('mousedown', onDoc);
  }, [open]);

  const openPanel = () => {
    setError('');
    if (value.preset === 'custom' && value.fromMs && value.toMs) {
      setFromInput(formatAbsolute(value.fromMs));
      setToInput(formatAbsolute(value.toMs));
    } else {
      const secs = PRESET_SECONDS[value.preset] ?? 86400;
      setFromInput(`now-${shortDur(secs)}`);
      setToInput('now');
    }
    setOpen(true);
  };

  const applyPreset = (p: string) => { onChange({ preset: p }); setOpen(false); };

  const applyCustom = () => {
    const fromMs = parseTimeExpr(fromInput);
    const toMs   = parseTimeExpr(toInput);
    if (fromMs === null)            { setError('Invalid "From" — try `now-1h` or `2024-05-02 12:00`'); return; }
    if (toMs === null)              { setError('Invalid "To" — try `now`'); return; }
    if (toMs <= fromMs)             { setError('"To" must be after "From"'); return; }
    if (toMs - fromMs > 365*86400_000) { setError('Range too large (max 1 year)'); return; }
    setError('');
    onChange({ preset: 'custom', fromMs, toMs });
    setOpen(false);
  };

  const setQuick = (which: 'from' | 'to', expr: string) => {
    if (which === 'from') setFromInput(expr); else setToInput(expr);
    setError('');
  };

  return (
    <div ref={ref} className="trp">
      <button className="trp-btn sec" onClick={() => (open ? setOpen(false) : openPanel())}>
        <span style={{ marginRight: 6 }}>🕒</span>
        {timeRangeLabel(value)}
        <span style={{ marginLeft: 6, color: 'var(--text2)' }}>▾</span>
      </button>
      {open && (
        <div className="trp-panel" role="dialog">
          <div className="trp-presets">
            <div className="trp-section-title">Quick ranges</div>
            {PRESETS.map(p => (
              <button key={p}
                className={'trp-preset' + (value.preset === p ? ' active' : '')}
                onClick={() => applyPreset(p)}>
                {PRESET_LABELS[p]}
              </button>
            ))}
          </div>
          <div className="trp-custom">
            <div className="trp-section-title">Absolute range</div>

            <label>
              From
              <input type="text" value={fromInput} spellCheck={false}
                onChange={e => { setFromInput(e.target.value); setError(''); }}
                onKeyDown={e => e.key === 'Enter' && applyCustom()}
                placeholder="now-1h  or  2026-05-02 12:00" />
              <div className="trp-quick">
                {['now-15m', 'now-1h', 'now-6h', 'now-1d', 'now-7d'].map(q => (
                  <button key={q} className="trp-chip" onClick={() => setQuick('from', q)}>{q}</button>
                ))}
              </div>
              <span className="trp-preview">{previewLabel(fromInput)}</span>
            </label>

            <label>
              To
              <input type="text" value={toInput} spellCheck={false}
                onChange={e => { setToInput(e.target.value); setError(''); }}
                onKeyDown={e => e.key === 'Enter' && applyCustom()}
                placeholder="now  or  2026-05-02 13:00" />
              <div className="trp-quick">
                {['now', 'now-15m', 'now-1h'].map(q => (
                  <button key={q} className="trp-chip" onClick={() => setQuick('to', q)}>{q}</button>
                ))}
              </div>
              <span className="trp-preview">{previewLabel(toInput)}</span>
            </label>

            {error && <div className="trp-error">{error}</div>}

            <div style={{ display: 'flex', gap: 6, marginTop: 4 }}>
              <button onClick={applyCustom}>Apply</button>
              <button className="sec" onClick={() => setOpen(false)}>Cancel</button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

// ── Time expression parser ────────────────────────────────────────────────────
//
// Accepted forms:
//   "now"                                          → Date.now()
//   "now-1h", "now+30m", "now-15m", "now-1d"       → relative offset
//   "2026-05-02 12:00", "2026-05-02T12:00:00Z"     → absolute (Date.parse)
//   epoch seconds (10-digit) or epoch millis       → numeric
const UNIT_MS: Record<string, number> = {
  s: 1000, m: 60_000, h: 3_600_000, d: 86_400_000, w: 7 * 86_400_000,
};

function parseTimeExpr(raw: string): number | null {
  const s = raw.trim();
  if (!s) return null;
  if (s === 'now') return Date.now();

  const rel = s.match(/^now\s*([-+])\s*(\d+)\s*([smhdw])$/i);
  if (rel) {
    const sign = rel[1] === '-' ? -1 : 1;
    const n    = parseInt(rel[2], 10);
    const u    = UNIT_MS[rel[3].toLowerCase()];
    return Date.now() + sign * n * u;
  }

  // ISO / SQL-ish absolute. Allow "YYYY-MM-DD HH:mm" by inserting T.
  const norm = /^\d{4}-\d{2}-\d{2}\s\d{2}:\d{2}/.test(s) ? s.replace(' ', 'T') : s;
  const ms = Date.parse(norm);
  if (!isNaN(ms)) return ms;

  const num = Number(s);
  if (!isNaN(num) && num > 0) return num > 1e12 ? num : num * 1000;

  return null;
}

function previewLabel(s: string): string {
  const ms = parseTimeExpr(s);
  if (ms === null) return ' ';
  return formatAbsolute(ms);
}

function formatAbsolute(ms: number): string {
  const d = new Date(ms);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

function shortDur(secs: number): string {
  if (secs % 86400 === 0) return `${secs/86400}d`;
  if (secs % 3600 === 0)  return `${secs/3600}h`;
  if (secs % 60 === 0)    return `${secs/60}m`;
  return `${secs}s`;
}
