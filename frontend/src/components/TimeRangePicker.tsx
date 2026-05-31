import { useEffect, useRef, useState } from 'react';
import type { TimeRange } from '@/lib/types';
import { PRESET_LABELS, PRESET_SECONDS, timeRangeLabel } from '@/lib/utils';
import { IconClock } from './icons';

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
  const fromInputRef = useRef<HTMLInputElement>(null);
  // Refs to every preset button so we can auto-focus the active
  // one on open and walk through them with ArrowUp/ArrowDown.
  const presetRefs = useRef<(HTMLButtonElement | null)[]>([]);

  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false);
    };
    document.addEventListener('mousedown', onDoc);
    document.addEventListener('keydown', onKey);
    // Auto-focus on open. Land on the currently-active preset so
    // ArrowUp/Down + Enter is the fast path; if the operator is
    // already on a custom range or no preset matches, fall back
    // to the "From" input so typing `now-2h` works without
    // clicking. Deferred to next tick — the panel mounts after
    // openPanel() flips `open` true.
    const t = setTimeout(() => {
      const activeIdx = PRESETS.indexOf(value.preset);
      const target = activeIdx >= 0
        ? presetRefs.current[activeIdx]
        : (presetRefs.current[0] ?? fromInputRef.current);
      target?.focus();
    }, 0);
    return () => {
      document.removeEventListener('mousedown', onDoc);
      document.removeEventListener('keydown', onKey);
      clearTimeout(t);
    };
  }, [open, value.preset]);

  // Arrow-key navigation between preset buttons. Up/Down step
  // through the list (wraps at the ends); Home/End jump to the
  // bounds. Native button Enter/Space still applies the preset
  // via onClick — no extra wiring needed there.
  const onPresetKey = (i: number) => (e: React.KeyboardEvent<HTMLButtonElement>) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      const next = (i + 1) % PRESETS.length;
      presetRefs.current[next]?.focus();
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      const prev = (i - 1 + PRESETS.length) % PRESETS.length;
      presetRefs.current[prev]?.focus();
    } else if (e.key === 'Home') {
      e.preventDefault();
      presetRefs.current[0]?.focus();
    } else if (e.key === 'End') {
      e.preventDefault();
      presetRefs.current[PRESETS.length - 1]?.focus();
    }
  };

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
      <button className="trp-btn sec" onClick={() => (open ? setOpen(false) : openPanel())}
              style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
        <IconClock />
        <span>{timeRangeLabel(value)}</span>
        <span style={{ marginLeft: 2, color: 'var(--text2)' }}>▾</span>
      </button>
      {open && (
        <div className="trp-panel" role="dialog">
          <div className="trp-presets">
            <div className="trp-section-title">Quick ranges</div>
            {PRESETS.map((p, i) => (
              <button key={p}
                ref={el => { presetRefs.current[i] = el; }}
                className={'trp-preset' + (value.preset === p ? ' active' : '')}
                onClick={() => applyPreset(p)}
                onKeyDown={onPresetKey(i)}>
                {PRESET_LABELS[p]}
              </button>
            ))}
          </div>
          <div className="trp-custom">
            <div className="trp-section-title">Absolute range</div>

            {/* v0.7.45 — Operator-requested (Grafana-style): pick a single day
                → From/To snap to that day's 00:00:00 → 23:59:59.999 and apply
                immediately. Native datetime-local can't expose a day
                double-click, so this dedicated day picker delivers the intent
                in one click; the From/To calendars below stay for precise
                sub-day ranges. */}
            <label>
              Whole day
              <input type="date" className="trp-cal"
                title="Pick a day — sets the range to that day's start → end"
                onChange={e => {
                  const day = e.target.value; // YYYY-MM-DD (local)
                  if (!day) return;
                  const [y, mo, d] = day.split('-').map(Number);
                  onChange({
                    preset: 'custom',
                    fromMs: new Date(y, mo - 1, d, 0, 0, 0, 0).getTime(),
                    toMs:   new Date(y, mo - 1, d, 23, 59, 59, 999).getTime(),
                  });
                  setOpen(false);
                }} />
            </label>

            {/* From — text expression input + calendar picker.
                Native datetime-local gives the user the Grafana-
                style calendar + hour/minute spinner without
                pulling in a date library. Picking a date writes
                an absolute YYYY-MM-DD HH:MM:SS string into the
                text input; users who prefer typing `now-1h` keep
                that path untouched. */}
            <label>
              From
              <div style={{ display: 'flex', gap: 4 }}>
                <input ref={fromInputRef} type="text" value={fromInput} spellCheck={false}
                  onChange={e => { setFromInput(e.target.value); setError(''); }}
                  onKeyDown={e => e.key === 'Enter' && applyCustom()}
                  placeholder="now-1h  or  2026-05-02 12:00"
                  style={{ flex: 1, minWidth: 0 }} />
                <input type="datetime-local"
                  value={toLocalDatetime(fromInput)}
                  onChange={e => {
                    const v = fromLocalDatetime(e.target.value);
                    if (v) { setFromInput(v); setError(''); }
                  }}
                  className="trp-cal"
                  title="Pick from calendar" />
              </div>
              <div className="trp-quick">
                {['now-15m', 'now-1h', 'now-6h', 'now-1d', 'now-7d'].map(q => (
                  <button key={q} className="trp-chip" onClick={() => setQuick('from', q)}>{q}</button>
                ))}
              </div>
              <span className="trp-preview">{previewLabel(fromInput)}</span>
            </label>

            <label>
              To
              <div style={{ display: 'flex', gap: 4 }}>
                <input type="text" value={toInput} spellCheck={false}
                  onChange={e => { setToInput(e.target.value); setError(''); }}
                  onKeyDown={e => e.key === 'Enter' && applyCustom()}
                  placeholder="now  or  2026-05-02 13:00"
                  style={{ flex: 1, minWidth: 0 }} />
                <input type="datetime-local"
                  value={toLocalDatetime(toInput)}
                  onChange={e => {
                    const v = fromLocalDatetime(e.target.value);
                    if (v) { setToInput(v); setError(''); }
                  }}
                  className="trp-cal"
                  title="Pick from calendar" />
              </div>
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

// `<input type="datetime-local">` expects "YYYY-MM-DDTHH:MM" (no
// timezone, no seconds). toLocalDatetime resolves the current text
// expression — whether it's `now-1h`, an epoch number, or an
// absolute ISO/SQL string — into that format. fromLocalDatetime
// reverses: takes the picker's "YYYY-MM-DDTHH:MM" and returns the
// absolute "YYYY-MM-DD HH:MM:SS" form our text parser already
// accepts. Returning '' from toLocalDatetime makes the picker
// render empty (browser shows the placeholder).
function toLocalDatetime(textExpr: string): string {
  const ms = parseTimeExpr(textExpr);
  if (ms === null) return '';
  const d = new Date(ms);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}
function fromLocalDatetime(value: string): string | null {
  if (!value) return null;
  // value is local time, no TZ; reuse parseTimeExpr which understands
  // "YYYY-MM-DDTHH:MM" via Date.parse.
  const ms = Date.parse(value);
  if (isNaN(ms)) return null;
  const d = new Date(ms);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}
