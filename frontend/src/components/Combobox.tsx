import { useEffect, useId, useMemo, useRef, useState } from 'react';

/**
 * Free-text input with a custom dropdown panel that filters as you
 * type. Replaces the previous native <datalist> implementation —
 * datalist's browser-controlled rendering cuts off long option
 * strings when the input is narrow, which made operation /
 * peer-service pickers unreadable on /traces.
 *
 * Behaviour:
 *   - Opens on focus or arrow click; closes on outside click / Esc.
 *   - Filters options by case-insensitive substring as the user
 *     types. The current input value is the source of truth — Enter
 *     keeps whatever's typed (so the user can submit a string that
 *     isn't in the suggestion list, e.g. a brand-new search term).
 *   - Arrow keys navigate, Enter picks (or fires onEnter if there's
 *     no active highlight), Tab picks and moves focus on.
 *   - Dropdown sizes to its content via CSS (min-width = input
 *     width, max-width capped) so long span names aren't truncated
 *     mid-word.
 *
 * v0.9.1022 — ÇIKIŞ SÖZLEŞMESİ (satır içi düzenleyici gereksinimi).
 * Atom bugüne dek yalnız BAĞIMSIZ bir alan olarak yaşadı: odağı hep
 * kullanıcı verirdi, kilitlenmezdi, ve alandan ÇIKIŞ yolu yoktu.
 * Satır içi bir düzenleyici (tabloda bir hücre, bir çip) bunların
 * dördünü de ister — `autoFocus` ile düzenleme moduna girer,
 * `disabled` ile kayıt sürerken kilitlenir, `onBlurCommit` ile
 * odaktan çıkınca yazılanı kaydeder, `onEscape` ile İPTAL eder.
 * Dördü de opsiyonel: hiçbiri verilmezse davranış birebir eskisi.
 */
export function Combobox({
  value, onChange, options, placeholder, width, onEnter,
  autoFocus, disabled, onBlurCommit, onEscape,
}: {
  value: string;
  onChange: (v: string) => void;
  options: string[];
  placeholder?: string;
  width?: number | string;
  onEnter?: () => void;
  // Mount'ta odaklan ve metni SEÇ. Seçmek şart: satır içi düzenleyici
  // mevcut değerle açılır, operatör yazmaya başlayınca onu değiştirmek
  // ister — imleci sona koymak "sil sonra yaz" demek olurdu.
  autoFocus?: boolean;
  // Kilitli alan: yazılamaz, liste AÇILMAZ. Temizle (✕) düğmesi çalışır
  // KALIR — atıl/kilitli bir alanda bile bayat bir değeri bırakabilmek
  // tam olarak orada işe yarar (EnvPicker'ın atıl hâli bunu yaşıyor).
  disabled?: boolean;
  // Odaktan çıkışta O ANKİ yazılı değeri verir. Karar çağıranın:
  // kaydetmek (TeamEditor) ya da eski değere dönmek (EnvPicker).
  // Listeden seçim BLUR ÜRETMEZ (satırlar mousedown'da preventDefault
  // eder) — yani bu, "alanı bıraktı" olayının tek yorumu.
  onBlurCommit?: (value: string) => void;
  // Liste KAPALIYKEN Esc. Düzenleme modundan iptal-çıkış. Liste
  // açıkken ÇAĞRILMAZ: v0.9.1021 katman sözleşmesi (bir Esc bir
  // katman) — ilk Esc listeyi kapatır, ikincisi düzenleyiciden çıkar.
  onEscape?: () => void;
}) {
  const id = useId();
  const wrapRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const [highlight, setHighlight] = useState<number>(-1);
  // Esc ile İPTAL edildi mi? Sonraki blur'un commit ETMEMESİ için.
  // Tuzak: iptal çoğu çağırıcıda düzenleyiciyi söker ya da odağı
  // taşır — yani Esc'in hemen ardından bir blur gelir. O blur commit
  // etseydi "iptal" sessizce KAYDET olurdu (tam tersi).
  const escapedRef = useRef(false);

  // Filtered list — substring match, case-insensitive. Empty query
  // shows the full list so clicking the field reveals all options
  // (matches native <select> "open and look at everything" UX).
  // Cap to 200 rows; service / operation lists in the wild stay
  // well under this but the cap keeps render cheap on degenerate
  // inputs.
  const filtered = useMemo(() => {
    const q = value.trim().toLowerCase();
    if (!q) return options.slice(0, 200);
    return options.filter(o => o.toLowerCase().includes(q)).slice(0, 200);
  }, [value, options]);

  // Reset highlight whenever the filtered set changes — otherwise
  // the index points into a stale list and arrow nav jumps around.
  useEffect(() => { setHighlight(-1); }, [filtered]);

  // Satır içi düzenleyici açılışı: odak + metni seç. Yalnız mount'ta.
  useEffect(() => {
    if (!autoFocus || disabled) return;
    inputRef.current?.focus();
    inputRef.current?.select();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Click-outside / Esc close.
  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      if (!wrapRef.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onDoc);
    return () => document.removeEventListener('mousedown', onDoc);
  }, [open]);

  // Scroll the highlighted row into view when arrow-navigating past
  // the visible portion of the dropdown.
  useEffect(() => {
    if (!listRef.current || highlight < 0) return;
    const row = listRef.current.querySelector<HTMLElement>(`[data-i="${highlight}"]`);
    row?.scrollIntoView({ block: 'nearest' });
  }, [highlight]);

  const pick = (v: string) => {
    onChange(v);
    setOpen(false);
    setHighlight(-1);
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    // Esc'ten SONRA gelen her tuş iptali geçersiz kılar: çağıran alanı
    // ayakta bıraktıysa (sökmediyse) operatör yazmaya devam edebilir ve
    // o oturumun blur'u yeniden commit etmelidir.
    if (e.key !== 'Escape') escapedRef.current = false;
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      if (!open) setOpen(true);
      setHighlight(h => Math.min(filtered.length - 1, h + 1));
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      setHighlight(h => Math.max(-1, h - 1));
    } else if (e.key === 'Enter') {
      if (open && highlight >= 0 && highlight < filtered.length) {
        e.preventDefault();
        pick(filtered[highlight]);
      } else {
        // No highlight → take the typed value as-is and let the
        // caller submit. Common case: user typed a custom search.
        setOpen(false);
        onEnter?.();
      }
    } else if (e.key === 'Escape') {
      // Katman sözleşmesi (v0.9.950 / Ö28, E2 dalgası): bir Esc BİR
      // katman kapatır. Liste AÇIKKEN Esc'i tüketiyoruz —
      // keyboard.ts'in escLayer'ı defaultPrevented'a bakar; tüketmezsek
      // açık Combobox'lı bir Drawer'da tek Esc ikisini birden kapatır
      // (FilterBuilder bunu yaşıyordu). Liste KAPALIYKEN dokunmuyoruz:
      // olay katmana akar ve Drawer/Modal normal kapanır.
      if (open) {
        e.preventDefault();
        setOpen(false);
        setHighlight(-1);
      } else if (onEscape) {
        // Liste kapalı + çağıran bir çıkış yolu verdi → SATIR İÇİ
        // DÜZENLEYİCİ katmanı. Aynı kural: bunu da tüketiyoruz, yoksa
        // bir Drawer içindeki düzenleyicide tek Esc ikisini birden
        // kapatırdı (v0.9.1021'in düzelttiği hatanın aynısı, bir
        // katman aşağıda). onEscape VERİLMEDİĞİNDE hiçbir şey
        // yapmıyoruz: olay üst katmana akar — eski davranış birebir.
        e.preventDefault();
        escapedRef.current = true;
        onEscape();
      }
    } else if (e.key === 'Tab') {
      if (open && highlight >= 0 && highlight < filtered.length) {
        pick(filtered[highlight]);
      } else {
        setOpen(false);
      }
    }
  };

  return (
    <div ref={wrapRef} className="cb-wrap" style={{ width }}>
      <input
        ref={inputRef}
        id={id}
        value={value}
        placeholder={placeholder}
        disabled={disabled}
        onChange={e => { escapedRef.current = false; onChange(e.target.value); setOpen(true); }}
        onFocus={() => { if (!disabled) setOpen(true); }}
        onClick={() => { if (!disabled) setOpen(true); }}
        onBlur={() => {
          setOpen(false);
          setHighlight(-1);
          // Esc iptalinden sonra gelen blur commit ETMEZ.
          if (escapedRef.current) { escapedRef.current = false; return; }
          onBlurCommit?.(value);
        }}
        onKeyDown={onKeyDown}
        autoComplete="off"
        spellCheck={false}
      />
      {/* Caret indicator + clear button. Caret only when value is
          empty so the affordance pair isn't redundant. */}
      {value ? (
        <button className="cb-clear" type="button"
          aria-label="Clear"
          title="Clear"
          onClick={() => {
            onChange('');
            // Kilitliyken odak/açılış YOK — ama temizleme çalışır.
            if (disabled) return;
            inputRef.current?.focus();
            setOpen(true);
          }}
          onMouseDown={e => e.preventDefault()}>
          ✕
        </button>
      ) : disabled ? null : (
        <button className="cb-caret" type="button" tabIndex={-1}
          aria-label={open ? 'Close' : 'Open'}
          onClick={() => { setOpen(o => !o); inputRef.current?.focus(); }}
          onMouseDown={e => e.preventDefault()}>
          ▾
        </button>
      )}

      {/* `disabled` liste render'ını da kapatır: alan AÇIK LİSTEYLE
          kilitlenebiliyor (TeamEditor Enter'da busy=true yapıyor,
          alan hâlâ odakta) — o hâlde liste satırın üstünde asılı
          kalırdı. */}
      {open && !disabled && filtered.length > 0 && (
        <div ref={listRef} className="cb-list" role="listbox">
          {filtered.map((o, i) => (
            <div
              key={o + i}
              role="option"
              aria-selected={i === highlight}
              data-i={i}
              className={`cb-row${i === highlight ? ' cb-row-on' : ''}${o === value ? ' cb-row-cur' : ''}`}
              onMouseDown={e => { e.preventDefault(); pick(o); }}
              onMouseEnter={() => setHighlight(i)}>
              {renderMatch(o, value)}
            </div>
          ))}
        </div>
      )}
      {open && !disabled && filtered.length === 0 && value.trim() && (
        <div className="cb-list">
          <div className="cb-row cb-row-empty">No matches — Enter will use the typed value</div>
        </div>
      )}
    </div>
  );
}

// renderMatch highlights the matched substring inside the option
// label so the user sees why each row qualified. Bolded run is the
// first occurrence (case-insensitive); rest stays plain.
function renderMatch(option: string, query: string): React.ReactNode {
  const q = query.trim();
  if (!q) return option;
  const lc = option.toLowerCase();
  const i = lc.indexOf(q.toLowerCase());
  if (i < 0) return option;
  return (
    <>
      {option.slice(0, i)}
      <b style={{ color: 'var(--accent2)' }}>{option.slice(i, i + q.length)}</b>
      {option.slice(i + q.length)}
    </>
  );
}
