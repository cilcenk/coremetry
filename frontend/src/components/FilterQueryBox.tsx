// FilterQueryBox — v0.10.264 Traces "Add filter" yeniden tasarımı, Öneri A
// (mockup onayı 2026-09-02: tek satır sorgu kutusu; Datadog/Honeycomb faset
// araması). Yalnız Traces kullanır; Explore/Logs FilterBuilder'da kalır.
//
// Akış: yaz → anahtar önerileri (sayfanın penceresindeki gözlem sayısıyla
// sıralı) → Enter/Tab → op çipleri (varsayılan =) → Enter/Tab → değer
// önerileri (sayım + pay çubuğu, yazdıkça daralır, 200 ms debounce) →
// Enter çipi bitirir, kutu bir sonraki filtre için boşalır. Satır içi
// "anahtar op değer" (http.route=/x, channel_code in A,B) Enter ile tek
// adımda. Çip üç parçalı (anahtar · op · DEĞER), tık = düzenle, × = kaldır,
// Backspace boş kutuda son çipi düzenler, Esc vazgeçer.
//
// Veri kaynakları FilterBuilder ile AYNI (useAttributeKeys + attributeValues,
// aynı pencere kuralı) — sözleşme (FilterExpr, upsert aynı k+op) değişmedi.
// Saf çekirdek lib/filterQuery.ts (testli); burada yalnız durum + DOM.

import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react';
import { Chip } from '@/components/ui';
import { api } from '@/lib/api';
import { useEscLayer } from '@/lib/escLayer';
import { attrKeyWindowParams } from '@/lib/attrKeyWindow';
import { useAttributeKeys } from '@/lib/useAttributeKeys';
import { useUrlRange } from '@/lib/useUrlRange';
import { timeRangeToNs } from '@/lib/utils';
import type { FilterExpr, FilterOp } from '@/lib/types';
import {
  FILTER_OPS, OP_NEEDS_VALUE, OP_SHORT, opFromShorthand, parseInlineFilter, splitListValues,
  chipValueLabel, filterKey, upsertFilter, pushRecent, parseRecent, rankKeys,
} from '@/lib/filterQuery';

type Step = 'key' | 'op' | 'value';

interface Draft {
  step: Step;
  k: string;
  op: FilterOp;
  text: string;
  /** Düzenlenen çipin indeksi (Backspace / tık ile açıldıysa). */
  editing?: number;
}

interface ValueRow { value: string; count: number }

export interface FilterQueryBoxProps {
  value: FilterExpr[];
  onChange: (next: FilterExpr[]) => void;
  /** Anahtar başına tohum değerler (örn. kind → server/client…). */
  suggestedValues?: Record<string, string[]>;
  /** Sabit hızlı çipler (status_code = error …). */
  quick?: FilterExpr[];
  /** localStorage anahtarı; verilmezse son kullanılanlar tutulmaz. */
  recentKey?: string;
  /** Test/kompozisyon: anahtar keşfini atla, bu listeyi kullan. */
  keyOptions?: string[];
  placeholder?: string;
  /** v0.10.270 — metrik modu (Explore metrik kaynağı): anahtarlar metrikte
   *  görülmüş etiketler (api.metricAttrKeys), değerler etiket uzayı
   *  (api.metricLabels; sayım yok → pay çubuğu çizilmez). FilterBuilder'ın
   *  metricName/metricService sözleşmesiyle aynı. */
  metricName?: string;
  metricService?: string;
}

function fmtCount(n: number): string {
  if (n >= 1e9) return (n / 1e9).toFixed(1) + 'B';
  if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'k';
  return String(n);
}

function loadRecent(key?: string): FilterExpr[] {
  if (!key) return [];
  try { return parseRecent(localStorage.getItem(key)); } catch { return []; }
}

export function FilterQueryBox({ value, onChange, suggestedValues, quick = [], recentKey, keyOptions, placeholder, metricName, metricService }: FilterQueryBoxProps) {
  const metricMode = !!metricName?.trim();
  const [metricKeys, setMetricKeys] = useState<string[]>([]);
  useEffect(() => {
    if (!metricMode) { setMetricKeys([]); return; }
    let cancelled = false;
    api.metricAttrKeys(metricName!.trim(), metricService ?? '')
      .then(keys => { if (!cancelled) setMetricKeys(keys ?? []); })
      .catch(() => { if (!cancelled) setMetricKeys([]); });
    return () => { cancelled = true; };
  }, [metricMode, metricName, metricService]);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [hi, setHi] = useState(0);
  const [liveValues, setLiveValues] = useState<ValueRow[]>([]);
  const [liveLoading, setLiveLoading] = useState(false);
  const [recent, setRecent] = useState<FilterExpr[]>(() => loadRecent(recentKey));
  const inputRef = useRef<HTMLInputElement>(null);
  const listId = 'fq-list';

  const [range] = useUrlRange();
  const rangeNs = useMemo(() => timeRangeToNs(range), [range]);
  const attrWindow = useMemo(() => attrKeyWindowParams(range), [range]);
  const { keys: spanKeys, observed } = useAttributeKeys(value, attrWindow, !keyOptions && !metricMode);
  const allKeys = keyOptions ?? (metricMode ? metricKeys : spanKeys);
  const hints = useMemo(() => {
    const m: Record<string, number> = {};
    for (const o of observed) m[o.key] = o.count;
    return m;
  }, [observed]);

  useEscLayer(draft !== null, () => setDraft(null));

  // Değer önerileri: anahtar + yazılan metin (200 ms debounce), tohum + canlı.
  const typed = draft?.step === 'value' ? draft.text.trim() : '';
  const draftKey = draft?.step === 'value' ? draft.k : '';
  useEffect(() => {
    if (!draftKey) { setLiveValues([]); return; }
    let cancelled = false;
    setLiveLoading(true);
    const handle = setTimeout(() => {
      const fetch: Promise<ValueRow[]> = metricMode
        ? api.metricLabels(metricName!.trim(), draftKey).then(vals => {
            const t = typed.toLowerCase();
            return (vals ?? []).filter(v => !t || v.toLowerCase().includes(t)).map(v => ({ value: v, count: 0 }));
          })
        : api.attributeValues(draftKey, '1h', 200, typed || undefined, rangeNs)
            .then(rows => (rows ?? []).map(r => ({ value: r.value, count: r.count })));
      fetch
        .then(rows => { if (!cancelled) setLiveValues(rows); })
        .catch(() => { if (!cancelled) setLiveValues([]); })
        .finally(() => { if (!cancelled) setLiveLoading(false); });
    }, 200);
    return () => { cancelled = true; clearTimeout(handle); };
  }, [draftKey, typed, rangeNs, metricMode, metricName]);

  const valueOptions = useMemo<ValueRow[]>(() => {
    if (!draft || draft.step !== 'value') return [];
    const seed = (suggestedValues?.[draft.k] ?? []).filter(v => !typed || v.toLowerCase().includes(typed.toLowerCase()));
    const seen = new Set<string>();
    const out: ValueRow[] = [];
    for (const r of [...liveValues, ...seed.map(v => ({ value: v, count: 0 }))]) {
      if (seen.has(r.value)) continue;
      seen.add(r.value);
      out.push(r);
    }
    return out.slice(0, 12);
  }, [draft, liveValues, suggestedValues, typed]);
  const maxCount = useMemo(() => valueOptions.reduce((m, r) => Math.max(m, r.count), 0), [valueOptions]);

  const keyMatches = useMemo(
    () => (draft && draft.step === 'key' ? rankKeys(allKeys, draft.text, hints) : []),
    [draft, allKeys, hints],
  );
  const opMatches = useMemo<FilterOp[]>(() => {
    if (!draft || draft.step !== 'op') return [];
    const t = draft.text.trim();
    if (!t) return FILTER_OPS;
    const exact = opFromShorthand(t);
    return exact ? [exact, ...FILTER_OPS.filter(o => o !== exact)] : FILTER_OPS.filter(o => o.toLowerCase().startsWith(t.toLowerCase()));
  }, [draft]);

  useEffect(() => { setHi(0); }, [draft?.step, draft?.text]);

  const commit = useCallback((expr: FilterExpr, editing?: number) => {
    let next: FilterExpr[];
    if (editing !== undefined && editing >= 0 && editing < value.length) {
      next = value.map((f, i) => (i === editing ? expr : f));
    } else {
      next = upsertFilter(value, expr);
    }
    onChange(next);
    if (recentKey) {
      const r = pushRecent(recent, expr);
      setRecent(r);
      try { localStorage.setItem(recentKey, JSON.stringify(r)); } catch { /* private mode */ }
    }
    setDraft(null);
    inputRef.current?.focus();
  }, [value, onChange, recentKey, recent]);

  const removeAt = (i: number) => onChange(value.filter((_, j) => j !== i));

  const editChip = (i: number) => {
    const f = value[i];
    if (!f) return;
    setDraft({ step: OP_NEEDS_VALUE[f.op] ? 'value' : 'op', k: f.k, op: f.op, text: chipValueLabel(f), editing: i });
    inputRef.current?.focus();
  };

  const advance = () => {
    if (!draft) return;
    if (draft.step === 'key') {
      const inline = parseInlineFilter(draft.text);
      if (inline) { commit(inline, draft.editing); return; }
      const k = keyMatches[hi] ?? draft.text.trim();
      if (!k) return;
      setDraft({ ...draft, step: 'op', k, text: '' });
      return;
    }
    if (draft.step === 'op') {
      const op = opMatches[hi] ?? opFromShorthand(draft.text) ?? '=';
      if (!OP_NEEDS_VALUE[op]) { commit({ k: draft.k, op, v: [] }, draft.editing); return; }
      setDraft({ ...draft, step: 'value', op, text: '' });
      return;
    }
    // value
    const picked = valueOptions[hi]?.value;
    const raw = draft.text.trim() || picked || '';
    if (!raw) return;
    const isList = draft.op === 'IN' || draft.op === 'NOT IN';
    const v = isList ? splitListValues(raw) : [draft.text.trim() ? raw : picked ?? raw];
    if (v.length === 0) return;
    commit({ k: draft.k, op: draft.op, v }, draft.editing);
  };

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    const listLen = draft?.step === 'key' ? keyMatches.length : draft?.step === 'op' ? opMatches.length : valueOptions.length;
    if (e.key === 'ArrowDown' && listLen > 0) { e.preventDefault(); setHi(h => (h + 1) % listLen); return; }
    if (e.key === 'ArrowUp' && listLen > 0) { e.preventDefault(); setHi(h => (h - 1 + listLen) % listLen); return; }
    if (e.key === 'Enter') { e.preventDefault(); if (draft) advance(); return; }
    if (e.key === 'Tab' && draft) {
      e.preventDefault();
      if (draft.step === 'value') {
        const picked = valueOptions[hi]?.value;
        if (picked) setDraft({ ...draft, text: picked });
      } else {
        advance();
      }
      return;
    }
    if (e.key === 'Backspace' && (!draft || draft.text === '')) {
      if (draft && draft.step === 'value') { e.preventDefault(); setDraft({ ...draft, step: 'op', text: '' }); return; }
      if (draft && draft.step === 'op') { e.preventDefault(); setDraft({ ...draft, step: 'key', text: draft.k }); return; }
      if (!draft && value.length > 0) {
        e.preventDefault();
        const i = value.length - 1;
        const f = value[i];
        onChange(value.slice(0, i));
        setDraft({ step: OP_NEEDS_VALUE[f.op] ? 'value' : 'op', k: f.k, op: f.op, text: chipValueLabel(f) });
      }
    }
  };

  const onText = (t: string) => {
    if (!draft) {
      if (!t) return;
      setDraft({ step: 'key', k: '', op: '=', text: t });
      return;
    }
    setDraft({ ...draft, text: t });
  };

  const inputPlaceholder = !draft ? (placeholder ?? 'Filtre: anahtar yaz ya da http.route=/x')
    : draft.step === 'key' ? 'anahtar…' : draft.step === 'op' ? 'op (= ≠ ~ IN > < ∃)…'
      : draft.op === 'IN' || draft.op === 'NOT IN' ? 'değerler, virgülle…' : 'değer…';

  return (
    <div className="fq">
      <div className={draft ? 'fq-bar is-open' : 'fq-bar'} role="combobox" aria-expanded={draft !== null} aria-haspopup="listbox"
        aria-owns={draft ? listId : undefined} onClick={() => inputRef.current?.focus()}>
        <span className="fq-lens" aria-hidden>⌕</span>
        {value.map((f, i) => (
          <span key={`${filterKey(f)}#${i}`} className={draft?.editing === i ? 'fq-chip is-editing' : 'fq-chip'}
            title={`${f.k} ${f.op} ${chipValueLabel(f)} — tık: düzenle`}>
            <span className="fq-k" onClick={e => { e.stopPropagation(); editChip(i); }}>{f.k}</span>
            <span className="fq-o mono" onClick={e => { e.stopPropagation(); editChip(i); }}>{OP_SHORT[f.op]}</span>
            {OP_NEEDS_VALUE[f.op] && (
              <span className="fq-v" onClick={e => { e.stopPropagation(); editChip(i); }}>
                {chipValueLabel(f)}
                {(f.op === 'IN' || f.op === 'NOT IN') && f.v.length > 1 && <span className="fq-n">{f.v.length}</span>}
              </span>
            )}
            <button type="button" className="fq-x" aria-label={`Filtreyi kaldır: ${f.k}`} onClick={e => { e.stopPropagation(); removeAt(i); }}>✕</button>
          </span>
        ))}
        {draft && draft.step !== 'key' && (
          <span className="fq-chip is-draft" aria-live="polite">
            <span className="fq-k">{draft.k}</span>
            <span className="fq-o mono">{draft.step === 'value' ? OP_SHORT[draft.op] : '…'}</span>
          </span>
        )}
        <input ref={inputRef} className="fq-input" value={draft?.text ?? ''} placeholder={inputPlaceholder}
          aria-label="Filtre" aria-autocomplete="list" aria-controls={draft ? listId : undefined}
          aria-activedescendant={draft ? `${listId}-${hi}` : undefined}
          onChange={e => onText(e.target.value)} onKeyDown={onKeyDown} autoComplete="off" spellCheck={false} />
        <span className="fq-hint" aria-hidden>
          <kbd>↑</kbd><kbd>↓</kbd> seç · <kbd>Tab</kbd> adım · <kbd>Enter</kbd> ekle · <kbd>Esc</kbd> vazgeç
        </span>
      </div>
      {draft && (
        <div className="fq-pop">
          <div className="fq-steps">
            <div className={draft.step === 'key' ? 'on' : 'done'}>1 · Anahtar{draft.step !== 'key' && <span className="mono">{draft.k}</span>}</div>
            <div className={draft.step === 'op' ? 'on' : draft.step === 'value' ? 'done' : ''}>2 · Op{draft.step === 'value' && <span className="mono">{OP_SHORT[draft.op]}</span>}</div>
            <div className={draft.step === 'value' ? 'on' : ''}>3 · Değer</div>
          </div>
          <div className="fq-list" role="listbox" id={listId}>
            {draft.step === 'key' && (
              keyMatches.length === 0
                ? <div className="fq-empty">{draft.text ? 'Eşleşen anahtar yok — Enter ile olduğu gibi kullan' : 'Anahtar yazmaya başla'}</div>
                : keyMatches.map((k, i) => (
                  <div key={k} id={`${listId}-${i}`} role="option" aria-selected={i === hi} className={i === hi ? 'fq-opt sel' : 'fq-opt'}
                    onMouseEnter={() => setHi(i)} onMouseDown={e => { e.preventDefault(); setHi(i); setDraft({ ...draft, step: 'op', k, text: '' }); }}>
                    <span className="name mono">{k}</span>
                    {hints[k] ? <span className="cnt">{fmtCount(hints[k])} span</span> : null}
                  </div>
                ))
            )}
            {draft.step === 'op' && (
              <div className="fq-ops" role="presentation">
                {opMatches.map((op, i) => (
                  <button key={op} type="button" id={`${listId}-${i}`} role="option" aria-selected={i === hi}
                    className={i === hi ? 'sel mono' : 'mono'} title={op}
                    onMouseEnter={() => setHi(i)}
                    onMouseDown={e => { e.preventDefault(); setHi(i); if (!OP_NEEDS_VALUE[op]) commit({ k: draft.k, op, v: [] }, draft.editing); else setDraft({ ...draft, step: 'value', op, text: '' }); }}>
                    {OP_SHORT[op]}{OP_SHORT[op] !== op ? <span className="fq-oplong"> {op}</span> : null}
                  </button>
                ))}
              </div>
            )}
            {draft.step === 'value' && (
              <>
                <div className="fq-grp">{liveLoading ? 'değerler yükleniyor…' : valueOptions.length ? `bu pencerede görülen değerler${typed ? ` — "${typed}" ile eşleşen` : ''}` : (typed ? 'öneri yok — Enter ile yazdığını ekle' : 'değer yaz')}</div>
                {valueOptions.map((r, i) => (
                  <div key={r.value} id={`${listId}-${i}`} role="option" aria-selected={i === hi} className={i === hi ? 'fq-opt sel' : 'fq-opt'}
                    onMouseEnter={() => setHi(i)}
                    onMouseDown={e => { e.preventDefault(); const isList = draft.op === 'IN' || draft.op === 'NOT IN'; commit({ k: draft.k, op: draft.op, v: isList ? splitListValues(r.value) : [r.value] }, draft.editing); }}>
                    <span className="name mono">{r.value || '(boş)'}</span>
                    {r.count > 0 && (
                      <span className="cnt bar">
                        <i style={{ width: `${Math.max(2, Math.round((r.count / (maxCount || 1)) * 72))}px` }} />
                        {fmtCount(r.count)}
                      </span>
                    )}
                  </div>
                ))}
              </>
            )}
          </div>
          <div className="fq-foot">
            <span><b>Backspace</b> boş kutuda son çip / önceki adım</span>
            <span><b>Satır içi</b> anahtar=değer · anahtar in a,b · anahtar exists</span>
          </div>
        </div>
      )}
      {(quick.length > 0 || recent.length > 0) && !draft && (
        <div className="fq-quick">
          <span className="fq-quick-label">Hızlı:</span>
          {quick.map(f => (
            <Chip key={`q:${filterKey(f)}`} size="xs" className="mono" onClick={() => commit(f)} title="Filtre olarak ekle">
              {f.k} {OP_SHORT[f.op]} {chipValueLabel(f)}
            </Chip>
          ))}
          {recent.filter(r => !quick.some(q => filterKey(q) === filterKey(r))).map(f => (
            <Chip key={`r:${filterKey(f)}`} size="xs" className="mono" onClick={() => commit(f)} title="Son kullanılan (bu tarayıcı)">
              ⟲ {f.k} {OP_SHORT[f.op]} {chipValueLabel(f)}
            </Chip>
          ))}
        </div>
      )}
    </div>
  );
}
