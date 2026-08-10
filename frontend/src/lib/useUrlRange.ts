import { useCallback, useEffect, useMemo } from 'react';
import { useSearchParams } from 'react-router-dom';
import type { TimeRange } from './types';
import { encodeRange, decodeRange } from './urlState';
import { STORAGE_KEYS, getSessionRaw, setSessionRaw } from './storage';

// useUrlRange (v0.7.87) — the SINGLE source of truth for a page's time
// range: the URL `?range=` param, not component-local state.
//
// Why: ~20 pages held the range only in useState, so a drill link
// (?range=…) was silently dropped on the target page, cross-signal
// pivots (service → its logs/traces) lost the operator's window, and
// Share / saved-views / browser-back all produced the wrong window.
// This hook makes the range shareable + restorable everywhere with a
// drop-in swap: `useState<TimeRange>({preset:'30m'})` → `useUrlRange()`.
// Same [value, setValue] tuple, same SetStateAction signature, so a
// page adopts it by changing one line.
//
// CRITICAL — object identity. `range` is derived from the URL string on
// every render. If it were a fresh object each render, any
// `useMemo(() => timeRangeToNs(range), [range])` downstream would see a
// new dep every render and refetch forever (the v0.5.184 trap). So
// `range` is memoised on the raw `?range=` STRING: its identity changes
// only when the URL range actually changes. defaultPreset is a string
// primitive (stable) for the same reason.
//
// Writes use { replace: true }: a range tweak refines the current view,
// it shouldn't pile a history entry per click — browser-back returns to
// the previous PAGE, while the current page's range still lives in its
// shareable URL.
//
// SEÇİLEN ARALIK SAYFALAR ARASI KALICI (v0.9.937, operatör isteği:
// "kullanıcı aralık seçtiyse sayfalar arası kalıcı olsun"). Öncelik:
//   1. `?range=` — URL kazanır (paylaşılabilir link + geri/ileri)
//   2. oturum kaydı — operatörün BU SEKMEDE seçtiği son aralık
//   3. defaultPreset — sayfanın kendi varsayılanı
// URL = tek doğruluk kaynağı sözleşmesi DOKUNULMADI: oturum basamağı
// yalnız "varsayılana düşme"nin ÖNÜNE girdi. `effective` stabil bir
// string kalıyor, yani memo kimliği ancak çözülen aralık gerçekten
// değişince değişir (v0.5.184 sonsuz-refetch tuzağı).
//
// ── Neden OTURUM, neden artık custom: de saklanıyor ─────────────────
// v0.7.124'te bu kayıt localStorage'daydı ve v0.8.409'da operatör
// bildirdi: bir grafiği brush'lamak mutlak (custom:) pencereyi KALICI
// yapıyor, sonraki her sayfa açılışı ve F5 geçmiş bir pencerede
// açılıyordu — "yeni traceler gelmiyor, cacheten getiriyor sanırım".
// O gün çözüm mutlak pencereleri saklamamaktı; ama bu, operatörün
// bilerek seçtiği bir pencerenin sayfa değişince kaybolması demekti.
//
// Şimdiki çözüm o olayın İKİ kök nedenini de kesiyor:
//   • KAPSAM: kayıt sekme oturumunda. Sekme kapanınca gider; haftalar
//     sonra bayat bir mutlak pencere açılamaz.
//   • GÖRÜNÜRLÜK: kayıttan gelen aralık URL'E DE YAZILIYOR (aşağıda).
//     v0.8.409'un asıl acısı pencerenin SESSİZ olmasıydı — URL temiz
//     görünüyor, seçici bir şey söylemiyor, ama sayfa geçmişte. Artık
//     `?range=custom:…` URL'de duruyor ve seçici onu gösteriyor: donmuş
//     durum görünür ve tek tıkla geri alınır.
const RANGE_STORE_KEY = STORAGE_KEYS.lastRange;

function readStoredRange(): string | null {
  return getSessionRaw(RANGE_STORE_KEY);
}
function writeStoredRange(enc: string): void {
  setSessionRaw(RANGE_STORE_KEY, enc);
}

// storedRangeString — public read of the persisted global range, for pages
// that own a bespoke URL-range pipeline (Explore, Metrics) and only need to
// INHERIT the cross-page window on first render without adopting the hook's
// write path. Returns null when nothing's been picked yet. (UX pass #2.)
export function storedRangeString(): string | null {
  return readStoredRange();
}

// rememberRange (v0.9.937) — "operatör bir aralık SEÇTİ" olayının
// oturum kaydına yazılması, hook'un setRange'i dışından.
//
// Neden gerekli: Clusters sayfası grafik-zoom'unu setRange yerine
// doğrudan setParams ile yazıyor (v0.9.206 — aynı yazımda `?tw=`
// silinebilsin diye; iki ayrı yazım react-router'da zincirlenmiyor).
// O yol setRange'i atladığı için kayıt da atlanırdı. Eskiden bu bir
// SORUN DEĞİLDİ: zoom mutlak pencere üretir, mutlak pencereler de
// saklanmazdı. v0.9.937 mutlağı da sakladığı için o boşluk artık
// gerçek — Clusters'ta zoom yapıp başka sayfaya geçen operatör
// penceresini kaybederdi.
export function rememberRange(enc: string): void {
  writeStoredRange(enc);
}

// pickRangeString — v0.9.855. useUrlRange's precedence chain (URL `?range=` →
// sticky → default) as a PURE function, so a non-hook caller can resolve the
// window the operator is currently looking at without duplicating the rule.
//
// Why it exists: href BUILDERS outside a range-owning page (⌘K palette) must
// hand pivotHref a window, and pivotHref makes the window REQUIRED precisely
// because "forgot the window" shipped four times. Re-deriving the precedence
// by hand at each such call site is how the fifth one ships.
// v0.9.937 — MUTLAK PENCERE ARTIK ELENMİYOR. v0.8.409'dan beri buradaki
// filtre custom:'u atıyordu çünkü kayıt KALICIYDI; kayıt oturuma
// taşındığı ve URL'e yazıldığı için (bkz. dosya başındaki gerekçe)
// operatörün bilerek seçtiği mutlak pencere artık sekme boyunca geçerli
// bir cevap. Filtreyi burada bırakmak, hook ile bu saf fonksiyonun
// AYRIŞMASI demek olurdu — ikisi tek zinciri anlatmak zorunda.
export function pickRangeString(
  urlRaw: string | null, stored: string | null, defaultPreset = '30m',
): string {
  return urlRaw ?? stored ?? defaultPreset;
}

/** Resolved TimeRange for a non-hook caller. `search` = location.search. */
export function currentRange(search: string, defaultPreset = '30m'): TimeRange {
  const raw = new URLSearchParams(search).get('range');
  return decodeRange(pickRangeString(raw, readStoredRange(), defaultPreset), { preset: defaultPreset });
}

// useUrlRange(defaultPreset?) — defaultPreset VERMEK, çağıranın sayfanın
// penceresine SAHİP olduğunu bildirir.
//
// Sahip olan çağıran, oturumdaki aralığı URL'e de yazar (aşağıdaki
// efekt); sahip olmayan (salt-okur) çağıran YAZMAZ. Ayrım şart, çünkü
// AppShell'de global mount edilen iki salt-okur tüketici var
// (CopilotChat, FilterBuilder) ve onlar da yazsaydı:
//   • aralığı olmayan her sayfanın URL'i (/settings, /admin/*) gereksiz
//     `?range=` ile kirlenirdi, VE
//   • daha kötüsü, salt-okur tüketici kendi '30m' varsayılanını yazar,
//     sayfanın kendi '1h' varsayılanını EZERDİ.
// Sözleşme kaynak-pinli: useUrlRange.contract.test.ts.
export function useUrlRange(
  defaultPreset?: string,
): [TimeRange, (r: TimeRange | ((prev: TimeRange) => TimeRange)) => void] {
  const [searchParams, setSearchParams] = useSearchParams();
  const owns = defaultPreset !== undefined;
  const preset = defaultPreset ?? '30m';
  const raw = searchParams.get('range');
  const stored = readStoredRange();
  const effective = raw ?? stored ?? preset;

  const range = useMemo(
    () => decodeRange(effective, { preset }),
    [effective, preset],
  );

  // v0.9.937 — oturumdaki aralığı URL'E DE YAZ (yalnız sahip çağıran).
  //
  // Neden: aralık zaten yukarıda çözülüyor, yani sayfa doğru pencereyi
  // ÇİZİYOR. Eksik olan tek şey PAYLAŞILABİLİRLİK — operatör sekmede
  // 6 saati seçip /traces'e geçtiğinde adres çubuğunu kopyalayınca
  // karşı taraf 30 dakikayı görürdü. Bir de görünürlük: v0.8.409'un
  // asıl acısı pencerenin URL'de görünmemesiydi.
  //
  // DÖNGÜ YOK (sig-guard): yazımdan sonra `raw` dolu olur ve ilk koşul
  // efekti erkenden bitirir. Yalnız `raw` boşken ve kayıt varken bir
  // kez koşar.
  //
  // YABANCI PARAMLARI KORUMAK İÇİN window.location.search'ten TOHUMLA,
  // router'ın `prev`inden DEĞİL: ham replaceState ile URL yazan
  // sayfalarda (Traces/Explore) `prev` bayat bir ALT KÜME olabiliyor ve
  // updater biçimi o sayfaların paramlarını sessizce düşürürdü.
  useEffect(() => {
    if (!owns || raw !== null || !stored) return;
    const next = new URLSearchParams(window.location.search);
    if (next.get('range')) return; // başka bir sahip bizden önce yazmış
    next.set('range', stored);
    setSearchParams(next, { replace: true });
  }, [owns, raw, stored, setSearchParams]);

  const setRange = useCallback(
    (r: TimeRange | ((prev: TimeRange) => TimeRange)) => {
      setSearchParams(
        prev => {
          const next = new URLSearchParams(prev);
          const curr = decodeRange(prev.get('range') ?? readStoredRange(), { preset });
          const val = typeof r === 'function' ? r(curr) : r;
          const enc = encodeRange(val);
          // v0.9.937 — RELATİF DE MUTLAK DA yazılır. Kayıt oturum
          // kapsamlı ve URL'de görünür olduğu için mutlak pencereyi
          // elemek artık gereksiz; elemek, operatörün bilerek yaptığı
          // seçimi sayfa değişince yutmak olurdu.
          writeStoredRange(enc);   // oturum boyu süreklilik
          next.set('range', enc);  // URL → paylaşılabilir + geri/ileri
          return next;
        },
        { replace: true },
      );
    },
    [setSearchParams, preset],
  );

  return [range, setRange];
}
