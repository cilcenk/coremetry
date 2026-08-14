import { useEffect, useMemo, useRef, useState } from 'react';
import { api } from '@/lib/api';
import { detectFieldNameToken, rankFieldNames, applyFieldName, fieldNameHint } from '@/lib/kqlFieldToken';

// KqlSearchInput (v0.5.464) — drop-in replacement for the bare
// <input> on /logs search. Layers field-aware autocomplete on
// top of the existing KQL-ish text input: as the operator types
// `service.name:` or after `AND severity:` the dropdown shows
// real values from the indexed data (ES _terms_enum). Empty
// prefix surfaces the most common values; typed prefix narrows.
//
// Why a wrapper component, not inline in Logs.tsx: token parsing
// + dropdown state + click-outside dismissal would balloon
// Logs.tsx. This component owns its dropdown lifetime and only
// emits value changes via onChange — caller (Logs.tsx) keeps
// its existing controlled-input semantics.
//
// Token detection — minimal grammar:
//   <name>:[<value-prefix>]
// We walk backwards from the cursor, find the most recent ':'
// not inside quotes, and treat the text between the preceding
// whitespace and ':' as the field name. The text between ':'
// and the cursor is the value prefix. Value matches with spaces
// get wrapped in double quotes on insert.
//
// Limitations (acceptable for v1):
// - Only single-key prefix; nested booleans like
//   `(a:1 OR b:2)` parse fine — we just look at the rightmost
//   `field:` on the line up to cursor.
// - Doesn't handle multi-line input — search is a single line.

interface KqlSearchInputProps {
  value: string;
  onChange: (v: string) => void;
  onSubmit?: () => void;
  placeholder?: string;
  title?: string;
  width?: number | string;
  // v0.9.291 — the page's current range, as a Go duration ("6h", "7d").
  // Bounds the ES term-dictionary walk behind the autocomplete: without
  // it the lookup prefix-scanned every index in retention on every
  // keystroke. Optional — the server clamps and defaults on its own, so
  // a caller that omits it still gets a bounded query, just a wider one.
  since?: string;
  // v0.9.955 (F4/Ö16) — ALAN ADI tamamlaması için kullanılabilir alanlar.
  // Sayfa bu listeyi ZATEN çekiyor (/api/logs/fields, LogFieldsPanel'in
  // "Available fields" bölümü), yani yeni bir ağ turu YOK — operatörün
  // "elastic api kullanımını çok artırma" şartı korunuyor. Verilmezse
  // davranış bayt-bayt eski hâl (yalnız değer tamamlaması).
  fields?: string[];
  // v0.9.970 (Ö16 ikinci tur) — mapping'in GERÇEK yol sayısı
  // (/api/logs/fields `total`). `fields` 500'de kırpıldığında liste
  // eksiktir ve sessiz kalmak "böyle alan yok" diye okunur; yan panel
  // bunu zaten "first N of M" diye söylüyordu, kutu söylemiyordu.
  // Verilmezse hiçbir uyarı çıkmaz — bilinmeyeni "tamam" diye sunmak da
  // "kırpık" diye sunmak da uydurma olurdu.
  fieldsTotal?: number;
  // v0.9.1003 (etkileşim denetimi O4) — bu kutu sayfanın `/` KISAYOL
  // hedefi mi? ServicePicker'daki bayrakla AYNI sözleşme: işaretsizken
  // GlobalShortcuts "ilk görünür metin kutusu" fallback'ine düşüyor ve
  // /logs'ta o kutu DOM sırasında ServicePicker — yani sayfanın TÜM
  // kimliği olan KQL araması `/` ile ulaşılamıyordu. Koşullu basılıyor:
  // koşulsuz olsaydı bir sayfadaki her KQL kutusu hedef olur ve
  // querySelectorAll yine DOM sırası kumarına dönerdi.
  shortcutSearch?: boolean;
}

interface TokenInfo {
  field: string;
  valuePrefix: string;
  // Char indices in the input string for the value range we
  // replace on insert (everything from after `:` to current
  // cursor).
  valueStart: number;
  valueEnd: number;
}

// detectFieldToken — walks back from cursor to find a field:value
// pattern. Returns null when the cursor isn't in a position
// where autocomplete would help (e.g. inside body free-text).
function detectFieldToken(text: string, cursor: number): TokenInfo | null {
  if (cursor <= 0) return null;
  // Walk back to the most recent ':' that isn't escaped + isn't
  // inside double quotes.
  let inQuotes = false;
  let colonAt = -1;
  for (let i = cursor - 1; i >= 0; i--) {
    const c = text[i];
    if (c === '"') { inQuotes = !inQuotes; continue; }
    if (inQuotes) continue;
    if (c === ':' && text[i - 1] !== '\\') { colonAt = i; break; }
    // Whitespace / boolean breaks the token chain.
    if (c === ' ' || c === '\t' || c === '(' || c === ')') return null;
  }
  if (colonAt < 0) return null;
  // Field name: from preceding whitespace/boundary to ':'.
  let nameStart = colonAt;
  for (let i = colonAt - 1; i >= 0; i--) {
    const c = text[i];
    if (c === ' ' || c === '\t' || c === '(') break;
    nameStart = i;
  }
  const field = text.slice(nameStart, colonAt);
  if (!field) return null;
  // Value prefix: from colon+1 to cursor.
  const rawValue = text.slice(colonAt + 1, cursor);
  // Strip a leading double-quote if the operator already started
  // a quoted value — we'll re-quote on insert anyway.
  const valuePrefix = rawValue.startsWith('"') ? rawValue.slice(1) : rawValue;
  return { field, valuePrefix, valueStart: colonAt + 1, valueEnd: cursor };
}

// Wrap a value in double quotes when it contains characters
// that ES query_string treats as syntax (spaces, operators,
// reserved punctuation). Otherwise emit bare.
function quoteIfNeeded(v: string): string {
  if (!v) return '""';
  if (/[\s+\-=&|!(){}[\]^"~*?:\\/]/.test(v)) {
    return `"${v.replace(/"/g, '\\"')}"`;
  }
  return v;
}

export function KqlSearchInput({
  value, onChange, onSubmit, placeholder, title, width = 380, since, fields,
  fieldsTotal, shortcutSearch,
}: KqlSearchInputProps) {
  const inputRef = useRef<HTMLInputElement>(null);
  const [cursor, setCursor] = useState(0);
  const [open, setOpen] = useState(false);
  const [values, setValues] = useState<string[]>([]);
  const [highlight, setHighlight] = useState(0);
  const [loading, setLoading] = useState(false);

  const token = useMemo(() => detectFieldToken(value, cursor), [value, cursor]);
  // v0.9.955 (F4/Ö16) — ALAN ADI konumu. Değer token'ı VARSA burası
  // null döner (detectFieldNameToken ':' gördüğünde çekilir), yani iki
  // öneri kaynağı asla çakışmaz.
  const nameToken = useMemo(
    () => (token ? null : detectFieldNameToken(value, cursor)),
    [token, value, cursor]);
  const nameSuggestions = useMemo(
    () => (nameToken && fields?.length ? rankFieldNames(fields, nameToken.prefix) : []),
    [nameToken, fields]);
  // v0.9.970 (Ö16 ikinci tur) — katalog kırpıksa bunu SÖYLE. Kritik hâl
  // `clipped-no-match`: operatör bir önek yazdı, hiçbir şey eşleşmedi ve
  // katalog 500'de kesilmiş. Eskiden liste hiç açılmıyordu, yani sessizlik
  // "böyle bir alan yok" diye okunuyordu — Ö16'nın kapatmak için var
  // olduğu yanlış okumanın ta kendisi.
  const hint = useMemo(
    () => (nameToken ? fieldNameHint(nameSuggestions.length, fields?.length ?? 0, fieldsTotal)
      : ({ kind: 'none' } as const)),
    [nameToken, nameSuggestions.length, fields?.length, fieldsTotal]);
  // Yalnız-başlık hâli: gösterilecek satır yok ama söylenecek söz VAR.
  const hintOnly = hint.kind === 'clipped-no-match';
  // Açık liste HANGİ kaynaktan? Değer önerileri ağ turu ister ve
  // gecikmelidir; alan adları elde HAZIR, o yüzden anında açılır.
  const nameMode = nameSuggestions.length > 0 || hintOnly;
  const items = nameMode ? nameSuggestions : values;

  // v0.9.955 (F4/Ö16) — alan adı listesi AĞ TURU BEKLEMEZ: liste elde
  // hazır olduğu için açılış anında. Değer listesinin `open`ı fetch
  // sonrası set ediliyor (yukarıdaki effect); iki kaynağın açılış
  // koşulunu ayrı tutmak, hazır bir listeyi 180 ms debounce'un arkasında
  // bekletmemek için.
  useEffect(() => {
    if (nameMode) { setOpen(true); setHighlight(0); }
  }, [nameMode, nameToken?.prefix]);

  // v0.8.217 — single-line Kibana-style bar (was an auto-growing textarea): the
  // KQL query stays on ONE line and scrolls horizontally instead of wrapping +
  // pushing the page down, matching Elastic/Kibana and the app's single-row
  // toolbar chrome. The auto-grow effect is gone; height is fixed below.

  // Debounced fetch when the token changes. 180ms is short
  // enough for keystroke responsiveness; the 30s server cache
  // (see api.go getLogsFieldValues) absorbs the rapid bursts.
  useEffect(() => {
    if (!token) {
      // v0.9.971 — `nameMode` ŞART. Bu iki effect aynı commit'te `open`u
      // ters yönde yazıyor ve React onları BİLDİRİM SIRASINDA koşturuyor;
      // koşulsuz `setOpen(false)` bir üstteki ad effect'inin `true`sunu
      // eziyordu. Belirti: `level:` → `level` (iki noktayı geri silmek)
      // alan-adı listesini açmıyor, ama sonraki tuşta açılıyordu —
      // sonraki render'da ad effect'inin bağımlılıkları değişmediği için
      // liste kapalı kalıyordu. `token` varken `nameMode` daima false
      // (nameToken ':' görünce çekiliyor), yani bu bağımlılık değer
      // tamamlamasına FAZLADAN tur ekleyemez.
      if (!nameMode) setOpen(false);
      setValues([]);
      return;
    }
    let cancelled = false;
    setLoading(true);
    const t = window.setTimeout(async () => {
      try {
        const r = await api.logsFieldValues(token.field, token.valuePrefix, 12, since);
        if (cancelled) return;
        const vs = r?.values ?? [];
        setValues(vs);
        setOpen(vs.length > 0);
        setHighlight(0);
      } catch {
        if (!cancelled) { setValues([]); setOpen(false); }
      } finally {
        if (!cancelled) setLoading(false);
      }
    }, 180);
    return () => { cancelled = true; clearTimeout(t); };
  }, [token?.field, token?.valuePrefix, since, nameMode]);

  // v0.9.955 (F4/Ö16) — alan adını yazar ve ':' EKLER. İki nokta süs
  // değil: alan adı tek başına KQL'de SERBEST METİNDİR, yani tamamlama
  // operatörü tam da kaçındığı hâle (yanlış sorgu, sıfır satır)
  // bırakırdı. Üstelik ':' değer tamamlamasının tetikleyicisi — tek
  // seçimle zincirin ikinci halkası açılıyor.
  const insertFieldName = (f: string) => {
    if (!nameToken) return;
    const next = applyFieldName(value, nameToken, f);
    onChange(next.text);
    setOpen(false);
    requestAnimationFrame(() => {
      inputRef.current?.focus();
      inputRef.current?.setSelectionRange(next.cursor, next.cursor);
      setCursor(next.cursor);
    });
  };

  const insertValue = (v: string) => {
    if (!token) return;
    const quoted = quoteIfNeeded(v);
    const before = value.slice(0, token.valueStart);
    const after = value.slice(token.valueEnd);
    const next = before + quoted + after;
    onChange(next);
    setOpen(false);
    // Restore cursor right after the inserted value.
    const nextCursor = before.length + quoted.length;
    requestAnimationFrame(() => {
      inputRef.current?.focus();
      inputRef.current?.setSelectionRange(nextCursor, nextCursor);
      setCursor(nextCursor);
    });
  };

  const pick = (i: number) => {
    if (nameMode) insertFieldName(nameSuggestions[i]);
    else insertValue(values[i]);
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (open && items.length > 0) {
      if (e.key === 'ArrowDown') {
        e.preventDefault();
        setHighlight(h => Math.min(items.length - 1, h + 1));
        return;
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault();
        setHighlight(h => Math.max(0, h - 1));
        return;
      }
      if (e.key === 'Enter') {
        e.preventDefault();
        pick(highlight);
        return;
      }
      if (e.key === 'Escape') {
        e.preventDefault();
        setOpen(false);
        return;
      }
      if (e.key === 'Tab') {
        // Tab picks like Enter without firing submit.
        e.preventDefault();
        pick(highlight);
        return;
      }
    }
    if (e.key === 'Enter' && onSubmit) {
      // Single-line input: Enter runs the search.
      e.preventDefault();
      onSubmit();
    }
  };

  const onSelect = (e: React.SyntheticEvent<HTMLInputElement>) => {
    setCursor(e.currentTarget.selectionStart ?? 0);
  };

  return (
    <span style={{ position: 'relative', display: 'inline-block', width }}>
      {/* Leading magnifier (Kibana-style). Decorative — clicks fall through to
          the input so the operator can click anywhere in the bar to focus. */}
      <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor"
        strokeWidth="2.2" strokeLinecap="round"
        style={{ position: 'absolute', left: 9, top: '50%', transform: 'translateY(-50%)',
          color: 'var(--text3)', pointerEvents: 'none' }}>
        <circle cx="11" cy="11" r="7" /><line x1="21" y1="21" x2="16.65" y2="16.65" />
      </svg>
      {/* v0.8.221 — a real single-line <input> (was a <textarea> that grew /
          wrapped): inherently one line + horizontal scroll, and it picks up the
          global `input, select` style so the bar matches every other field in
          the app (Kibana-like). Kept for autocomplete + Enter-to-submit. */}
      <input ref={inputRef}
        type="text"
        {...(shortcutSearch ? { 'data-shortcut-search': '' } : {})}
        value={value}
        onChange={e => { onChange(e.target.value); setCursor(e.target.selectionStart ?? e.target.value.length); }}
        onSelect={onSelect}
        onClick={onSelect}
        onKeyDown={onKeyDown}
        onBlur={() => setTimeout(() => setOpen(false), 120)}
        placeholder={placeholder}
        title={title}
        spellCheck={false}
        autoComplete="off"
        style={{ width: '100%', paddingLeft: 28 }} />
      {open && (items.length > 0 || hintOnly) && (
        <div style={{
          position: 'absolute', top: '100%', left: 0,
          width: '100%', minWidth: 260,
          background: 'var(--bg)', color: 'var(--text)',
          border: '1px solid var(--border)', borderRadius: 4,
          boxShadow: '0 6px 24px rgba(0,0,0,0.18)',
          zIndex: 'var(--z-dropdown)', maxHeight: 280, overflowY: 'auto',
          marginTop: 2,
        }}>
          <div style={{
            padding: '4px 10px', fontSize: 10, color: 'var(--text3)',
            borderBottom: '1px solid var(--border)',
            fontFamily: 'ui-monospace, monospace',
          }}>
            {/* v0.9.955 (F4/Ö16) — başlık HANGİ soruya cevap verdiğimizi
                söyler. Alan adı listesi ile değer listesi ekranda aynı
                görünseydi operatör "field" ile "value"yu karıştırırdı. */}
            {/* v0.9.970 (Ö16 ikinci tur) — katalog kırpıksa BUNU da söyle.
                Kırpma alfabetik olduğu için `attributes.CHANNEL_CODE`
                pekâlâ 500'ün dışında kalabilir; sessiz bir boş liste
                "böyle alan yok" diye okunurdu, doğrusu "katalog oraya
                kadar uzanmıyor". */}
            {nameMode
              ? (hint.kind === 'clipped-no-match'
                ? `bu önekle eşleşme yok · katalog ilk ${hint.shown.toLocaleString()}/${hint.total.toLocaleString()} alan`
                : hint.kind === 'clipped'
                  ? `alan adı · ${nameSuggestions.length} eşleşme · katalog ilk ${hint.shown.toLocaleString()}/${hint.total.toLocaleString()} alan`
                  : `alan adı · ${nameSuggestions.length} eşleşme`)
              : `${token?.field}: ${loading ? '· searching…' : ''}`}
          </div>
          {items.map((v, i) => (
            <div key={v}
              onMouseEnter={() => setHighlight(i)}
              onMouseDown={e => { e.preventDefault(); pick(i); }}
              style={{
                padding: '6px 10px', cursor: 'pointer', fontSize: 12,
                fontFamily: 'ui-monospace, monospace',
                background: i === highlight ? 'var(--bg2)' : 'transparent',
                borderLeft: i === highlight ? '2px solid var(--accent2)' : '2px solid transparent',
                whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis',
              }}>{v}</div>
          ))}
        </div>
      )}
    </span>
  );
}
