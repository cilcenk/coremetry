import { useEffect, useMemo, useRef, useState } from 'react';
import type { TimeRange } from '@/lib/types';
import { PRESET_SECONDS } from '@/lib/utils';
import { decodeRange, encodeRange } from '@/lib/urlState';
import { getRaw, setRaw } from '@/lib/storage';
import { useLang, useT } from '@/lib/i18n';
import {
  DOW_SHORT, MONTHS_LONG, QUICK_PRESETS, RECENTS_KEY,
  absRangeLabel, calendarGrid, dayClickRange, formatDateTime, formatTimeOfDay,
  parseDateTime, parseRecents, pushRecent, resolveRangeMs, utcOffsetLabel,
  withTimeOfDay, zoomOutRange, type CalCell,
} from '@/lib/rangePicker';
import { Button } from './ui/Button';
import { IconClock, IconZoomOut } from './icons';

// Grafana-parity global time picker (2026-07-24 brief). One button shows the
// current window ("Son 30 dakika" / "24 Tem 08:00 → 24 Tem 12:30"); clicking
// opens the two-column Grafana panel:
//   LEFT  — absolute range: From/To datetime inputs + native time inputs, a
//           hand-rolled mini month calendar (day click → From, second click →
//           To), Apply, and the last-4 recently used ranges.
//   RIGHT — the 11 quick presets (5m…30d, all pre-existing in the TimeRange
//           contract — no codec/timeRangeToNs change was needed).
// A magnifier-minus button next to the picker widens the window 2× around a
// fixed center (preset drops to custom), and the panel footer shows the
// browser's UTC offset. Deliberately OUT OF SCOPE per the brief: the `now-6h`
// text grammar (the old free-text expression inputs were replaced by absolute
// datetime inputs) and a global refresh-interval dropdown.
//
// All writes go through the SAME onChange the presets always used — pages
// clear their zoom stacks on out-of-band range changes (v0.9.201), so the
// picker needs no zoom-stack awareness of its own.
export function TimeRangePicker({ value, onChange }: {
  value: TimeRange;
  onChange: (r: TimeRange) => void;
}) {
  const t = useT();
  const lang = useLang();
  const [open, setOpen] = useState(false);
  const [fromInput, setFromInput] = useState('');
  const [toInput, setToInput] = useState('');
  const [error, setError] = useState('');      // i18n key, '' = none
  const [recents, setRecents] = useState<string[]>([]);
  const [cal, setCal] = useState(() => {
    const d = new Date();
    return { y: d.getFullYear(), m: d.getMonth() };
  });
  // True between the first and second calendar day clicks — the two-click
  // From→To flow. Typing in either input breaks out of it.
  const [calPending, setCalPending] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const fromInputRef = useRef<HTMLInputElement>(null);
  // Refs to every preset button so we can auto-focus the active
  // one on open and walk through them with ArrowUp/ArrowDown.
  const presetRefs = useRef<(HTMLButtonElement | null)[]>([]);

  const fromMs = useMemo(() => parseDateTime(fromInput), [fromInput]);
  const toMs = useMemo(() => parseDateTime(toInput), [toInput]);
  const cells = useMemo(() => calendarGrid(cal.y, cal.m), [cal]);

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
    // ArrowUp/Down + Enter is the fast path; on a custom range fall
    // back to the "From" input so refining the window starts typing
    // immediately. Deferred to next tick — the panel mounts after
    // openPanel() flips `open` true.
    const timer = setTimeout(() => {
      const activeIdx = QUICK_PRESETS.indexOf(value.preset);
      const target = activeIdx >= 0 ? presetRefs.current[activeIdx] : fromInputRef.current;
      target?.focus();
    }, 0);
    return () => {
      document.removeEventListener('mousedown', onDoc);
      document.removeEventListener('keydown', onKey);
      clearTimeout(timer);
    };
  }, [open, value.preset]);

  // Arrow-key navigation between preset buttons. Up/Down step
  // through the list (wraps at the ends); Home/End jump to the
  // bounds. Native button Enter/Space still applies the preset
  // via onClick — no extra wiring needed there.
  const onPresetKey = (i: number) => (e: React.KeyboardEvent<HTMLButtonElement>) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      presetRefs.current[(i + 1) % QUICK_PRESETS.length]?.focus();
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      presetRefs.current[(i - 1 + QUICK_PRESETS.length) % QUICK_PRESETS.length]?.focus();
    } else if (e.key === 'Home') {
      e.preventDefault();
      presetRefs.current[0]?.focus();
    } else if (e.key === 'End') {
      e.preventDefault();
      presetRefs.current[QUICK_PRESETS.length - 1]?.focus();
    }
  };

  const openPanel = () => {
    setError('');
    setCalPending(false);
    const now = Date.now();
    const abs = resolveRangeMs(value, now);
    setFromInput(formatDateTime(abs.fromMs));
    setToInput(formatDateTime(abs.toMs));
    const d = new Date(abs.toMs);
    setCal({ y: d.getFullYear(), m: d.getMonth() });
    setRecents(parseRecents(getRaw(RECENTS_KEY)));
    setOpen(true);
  };

  // Every APPLY path (preset, custom, recent click) records the range in the
  // last-4 recents list. Zoom-out intentionally does not — repeated widening
  // would flood the list with throwaway windows.
  const apply = (r: TimeRange) => {
    setRaw(RECENTS_KEY, JSON.stringify(
      pushRecent(parseRecents(getRaw(RECENTS_KEY)), encodeRange(r)),
    ));
    onChange(r);
    setOpen(false);
  };

  const applyCustom = () => {
    if (fromMs === null) { setError('trp.errFrom'); return; }
    if (toMs === null) { setError('trp.errTo'); return; }
    if (toMs <= fromMs) { setError('trp.errOrder'); return; }
    if (toMs - fromMs > 365 * 86400_000) { setError('trp.errMax'); return; }
    setError('');
    apply({ preset: 'custom', fromMs, toMs });
  };

  const onDayClick = (c: CalCell) => {
    const start = new Date(c.y, c.m, c.d, 0, 0, 0, 0).getTime();
    const end = new Date(c.y, c.m, c.d, 23, 59, 59, 999).getTime();
    const next = dayClickRange({ fromMs: calPending ? fromMs : null, toMs: null }, start, end);
    setFromInput(formatDateTime(next.fromMs));
    // While the To click is pending, To tracks the clicked day's end — a
    // single click + Apply therefore selects the whole day, and the inputs
    // never show an inverted window mid-flow.
    setToInput(formatDateTime(next.toMs ?? end));
    setCalPending(next.toMs === null);
    setError('');
  };

  const shiftMonth = (delta: number) =>
    setCal(c => {
      const d = new Date(c.y, c.m + delta, 1);
      return { y: d.getFullYear(), m: d.getMonth() };
    });

  const onTimeChange = (which: 'from' | 'to') => (e: React.ChangeEvent<HTMLInputElement>) => {
    const [base, set] = which === 'from'
      ? [fromMs, setFromInput] : [toMs, setToInput];
    if (base === null || !e.target.value) return;
    const ms = withTimeOfDay(base, e.target.value);
    if (ms !== null) { set(formatDateTime(ms)); setError(''); }
  };

  const rangeLabel = (r: TimeRange): string => {
    if (r.preset === 'custom' && r.fromMs && r.toMs) {
      return absRangeLabel(r.fromMs, r.toMs, lang, Date.now());
    }
    return PRESET_SECONDS[r.preset] ? t('range.' + r.preset) : r.preset;
  };

  const today = new Date();
  const dayCellClass = (c: CalCell): string => {
    const cls = ['trp-cal-day'];
    if (!c.inMonth) cls.push('out');
    if (c.y === today.getFullYear() && c.m === today.getMonth() && c.d === today.getDate()) {
      cls.push('today');
    }
    const cs = new Date(c.y, c.m, c.d, 0, 0, 0, 0).getTime();
    const ce = new Date(c.y, c.m, c.d, 23, 59, 59, 999).getTime();
    const isFrom = fromMs !== null && fromMs >= cs && fromMs <= ce;
    const isTo = toMs !== null && toMs >= cs && toMs <= ce;
    if (isFrom || isTo) cls.push('sel');
    else if (fromMs !== null && toMs !== null && cs <= toMs && ce >= fromMs) cls.push('inrange');
    return cls.join(' ');
  };

  return (
    <div ref={ref} className="trp">
      <Button variant="secondary" className="trp-btn"
        onClick={() => (open ? setOpen(false) : openPanel())}
        leftIcon={<IconClock />}
        rightIcon={<span style={{ color: 'var(--text2)' }}>▾</span>}>
        {rangeLabel(value)}
      </Button>
      <Button variant="secondary" className="trp-zoom"
        title={t('trp.zoomOut')} aria-label={t('trp.zoomOut')}
        onClick={() => onChange(zoomOutRange(value, Date.now()))}>
        <IconZoomOut />
      </Button>
      {open && (
        <div className="trp-panel" role="dialog">
          <div className="trp-body">
            <div className="trp-custom">
              <div className="trp-section-title">{t('trp.absoluteRange')}</div>

              <label>
                {t('trp.from')}
                <div className="trp-row">
                  <input ref={fromInputRef} type="text" value={fromInput} spellCheck={false}
                    onChange={e => { setFromInput(e.target.value); setCalPending(false); setError(''); }}
                    onKeyDown={e => e.key === 'Enter' && applyCustom()}
                    placeholder="2026-07-24 08:00:00" />
                  <input type="time" step={1} className="trp-time"
                    value={fromMs !== null ? formatTimeOfDay(fromMs) : ''}
                    onChange={onTimeChange('from')} />
                </div>
              </label>

              <label>
                {t('trp.to')}
                <div className="trp-row">
                  <input type="text" value={toInput} spellCheck={false}
                    onChange={e => { setToInput(e.target.value); setCalPending(false); setError(''); }}
                    onKeyDown={e => e.key === 'Enter' && applyCustom()}
                    placeholder="2026-07-24 12:30:00" />
                  <input type="time" step={1} className="trp-time"
                    value={toMs !== null ? formatTimeOfDay(toMs) : ''}
                    onChange={onTimeChange('to')} />
                </div>
              </label>

              <div className="trp-calwrap">
                <div className="trp-cal-head">
                  <button className="trp-cal-nav" aria-label={t('trp.prevMonth')}
                    onClick={() => shiftMonth(-1)}>‹</button>
                  <span className="trp-cal-title">{MONTHS_LONG[lang][cal.m]} {cal.y}</span>
                  <button className="trp-cal-nav" aria-label={t('trp.nextMonth')}
                    onClick={() => shiftMonth(1)}>›</button>
                </div>
                <div className="trp-cal-grid">
                  {DOW_SHORT[lang].map(d => (
                    <span key={d} className="trp-cal-dow">{d}</span>
                  ))}
                  {cells.map(c => (
                    <button key={`${c.y}-${c.m}-${c.d}`} className={dayCellClass(c)}
                      onClick={() => onDayClick(c)}>
                      {c.d}
                    </button>
                  ))}
                </div>
              </div>

              {error && <div className="trp-error">{t(error)}</div>}

              <div className="trp-actions">
                <Button size="sm" onClick={applyCustom}>{t('trp.applyRange')}</Button>
              </div>

              {recents.length > 0 && (
                <div className="trp-recents">
                  <div className="trp-section-title">{t('trp.recentRanges')}</div>
                  {recents.map(enc => (
                    <button key={enc} className="trp-preset"
                      onClick={() => apply(decodeRange(enc, { preset: '30m' }))}>
                      {rangeLabel(decodeRange(enc, { preset: enc }))}
                    </button>
                  ))}
                </div>
              )}
            </div>

            <div className="trp-presets">
              <div className="trp-section-title">{t('trp.quickRanges')}</div>
              {QUICK_PRESETS.map((p, i) => (
                <button key={p}
                  ref={el => { presetRefs.current[i] = el; }}
                  className={'trp-preset' + (value.preset === p ? ' active' : '')}
                  onClick={() => apply({ preset: p })}
                  onKeyDown={onPresetKey(i)}>
                  {t('range.' + p)}
                </button>
              ))}
            </div>
          </div>
          <div className="trp-foot">
            {t('trp.browserTime')} ({utcOffsetLabel(-new Date().getTimezoneOffset())})
          </div>
        </div>
      )}
    </div>
  );
}
