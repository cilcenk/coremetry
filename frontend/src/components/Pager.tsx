import { useEffect, useState, FormEvent } from 'react';
import { Button } from './ui/Button';

// Pager — depodaki TEK "daha fazla satır nasıl gelir" yüzeyi.
//
// ——— SÖZLEŞME (v0.9.1014) ————————————————————————————————————
//
// v0.9.1013'e kadar atomun tek tüketicisi /traces'ti; diğer beş
// sayfalama yüzeyi elle çizilmişti ve her biri kendi kararını
// vermişti — konum (tablo altı / tablo üstü / ortada), vurgu (Next
// birincil mi ikincil mi), sayının ANLAMI (kesin mi tavanlı mı),
// commit anı (Enter mı blur mu). Beş yüzey, beş cevap.
//
// İki prop ZORUNLU ve ikisi de tip düzeyinde (Button.variant
// hilesinin aynısı — v0.9.1005): statik tarama tahmin eder, `tsc`
// ZORLAR.
//
//   mode  — 'offset' (← Prev / sayfa girdisi / Next →) ya da
//           'cursor' (↓ daha fazla yükle, satırlar BİRİKİR).
//           Bu bir görünüm tercihi değil bir VERİ modeli beyanı:
//           keyset imleçli bir yüzeyde "sayfa 7'ye git" ifade
//           edilemez, çünkü 7'nin imleci ancak 6 çekilerek bilinir.
//
//   count — `total` sayısının ne DEMEK olduğu. Depodaki en pahalı
//           sessiz yalan buradaydı: v0.9.288'de "of 10,000" ES'in
//           `track_total_hits` tavanıydı, gerçek sayı değil; v0.9.638'de
//           /traces total'ı Pager'a vermeyi BIRAKTI çünkü tavanlı bir
//           sayı operatörü listenin ULAŞAMAYACAĞI sayfalara yolluyordu.
//           Artık sayının anlamını beyan etmek zorunlu:
//             'exact'  — kesin. YALNIZ bunda `total` son sayfayı türetir.
//             'capped' — tavana dayandı; "N+" basılır, son sayfa TÜRETİLMEZ.
//             'approx' — yaklaşık; "~N" basılır, son sayfa TÜRETİLMEZ.
//             'skip'   — sayılmadı. `total` tip düzeyinde YASAK.
//
// ——— Konum ve vurgu ——————————————————————————————————————————
//
// Şerit tablonun ALTINDA ve yapışkan (`stickyBottom` varsayılan
// true, v0.9.645: uzun listede "Next" ekranın dışında kalıyordu).
// Gutenberg diyagonali gereği "ileri" eylemi SAĞDA ve şeritteki TEK
// vurgulu kontrol o — Prev/Last ikincil. Bu zaten v0.9.645'te
// alınmış bir karardı, artık sözleşmenin parçası.
//
// ——— "Son sayfa" tek anlam ————————————————————————————————————
//
// `lastReachablePage` açık kaçış kapısı olarak duruyor: çağıran hem
// KESİN hem SUNULABİLİR bir son sayfa hesapladığında verir. Tavanlı
// bir `total`dan asla türetilmez — v0.9.638'in kararı korunuyor,
// artık `count` ile tip düzeyinde çivili.

export type PagerMode = 'offset' | 'cursor';
export type PagerCount = 'skip' | 'approx' | 'exact' | 'capped';

// `skip` beyan eden bir yüzey `total` SMUGGLE EDEMEZ. Sayının anlamı
// ile sayının varlığı tek bir tip kararına bağlanıyor.
type CountDecl =
  | { count: 'skip'; total?: never }
  | { count: 'exact' | 'approx' | 'capped'; total?: number };

interface PagerCommon {
  extras?: React.ReactNode;
  stickyBottom?: boolean;
}

interface OffsetOnly {
  mode: 'offset';
  page: number;
  pageSize: number;
  hasMore?: boolean;
  onPage: (next: number) => void;
  // YALNIZ hem kesin hem ulaşılabilir olduğunda verilir; verilmezse
  // "Last" hiç çizilmez.
  lastReachablePage?: number;
}

interface CursorOnly {
  mode: 'cursor';
  hasMore: boolean;
  onMore: () => void;
  loading?: boolean;
  // Birikmiş satır sayısı — dürüst son için ("… yüklendi").
  loaded?: number;
  moreLabel?: string;
  doneLabel?: React.ReactNode;
}

export type PagerProps = PagerCommon & CountDecl & (OffsetOnly | CursorOnly);

// countLabel — sayının anlamını GÖRÜNÜR kılar. Tavanlı bir sayının
// yanındaki "+" ve yaklaşık bir sayının önündeki "~" tesadüfi
// tipografi değil: operatör "12.847 kayıt" ile "en az 12.847 kayıt"
// arasındaki farkı bilmeden kapasite kararı veremez.
export function countLabel(count: PagerCount, total: number | undefined): string | null {
  if (count === 'skip' || total === undefined) return null;
  const n = total.toLocaleString();
  if (count === 'capped') return `${n}+`;
  if (count === 'approx') return `~${n}`;
  return n;
}

// derivedLastPage — son sayfayı YALNIZ kesin sayıdan türet.
//
// Bu fonksiyon ayrı ve saf, çünkü çivilenmesi gereken kural tam
// olarak bu: v0.9.638'in olayı "tavanlı total son sayfayı sürdü"
// idi ve o hata bir bileşenin içinde gömülü kaldığı sürece test
// edilemezdi.
export function derivedLastPage(
  count: PagerCount, total: number | undefined, pageSize: number,
): number | null {
  if (count !== 'exact' || total === undefined || pageSize <= 0) return null;
  return Math.max(0, Math.ceil(total / pageSize) - 1);
}

// cursorProgress — birikimli bir listede "neredeyim" cümlesi.
//
// Saf ve ayrı, çünkü çivilenmesi gereken DÜRÜSTLÜK burada: v0.9.288'de
// /logs "showing 200 of 10,000" basıyordu ve o 10.000 ES'in
// `track_total_hits` tavanıydı — gerçek sayı değil. Tavanlı sayı artık
// "+" ile geliyor ve bu birleştirme tek yerde yaşıyor, üç yüzeyin
// kendi cümlesini kurmasına gerek kalmıyor.
export function cursorProgress(
  loaded: number | undefined, count: PagerCount, total: number | undefined,
): string | null {
  const label = countLabel(count, total);
  if (loaded !== undefined && label) return `showing ${loaded.toLocaleString()} of ${label}`;
  if (label) return label;
  if (loaded !== undefined) return `showing ${loaded.toLocaleString()}`;
  return null;
}

export function Pager(props: PagerProps) {
  const { count, total, extras, stickyBottom = true } = props;
  const cls = `pager${stickyBottom ? ' is-sticky-bottom' : ''}`;
  const label = countLabel(count, total);

  if (props.mode === 'cursor') {
    const { hasMore, onMore, loading, loaded, moreLabel, doneLabel } = props;
    const progress = cursorProgress(loaded, count, total);
    return (
      <div className={cls} data-pager-mode="cursor">
        {hasMore ? (
          <>
            <Button variant="primary" size="sm" onClick={onMore} loading={loading}>
              {moreLabel ?? '↓ Load more'}
            </Button>
            {progress && <span style={{ color: 'var(--text2)' }}>{progress}</span>}
          </>
        ) : (
          // Biten listede ilerleme cümlesi TEKRAR olurdu — dürüst son
          // sayıyı zaten taşıyor.
          <span style={{ color: 'var(--text3)' }}>
            {doneLabel ?? (loaded !== undefined
              ? `penceredeki tüm eşleşmeler yüklendi (${loaded.toLocaleString()} satır)`
              : 'tümü yüklendi')}
          </span>
        )}
        {extras && <span style={{ color: 'var(--text2)' }}>· {extras}</span>}
      </div>
    );
  }

  return <OffsetPager {...props} cls={cls} label={label} />;
}

function OffsetPager({
  page, pageSize, hasMore, onPage, lastReachablePage, count, total, extras, cls, label,
}: PagerCommon & CountDecl & OffsetOnly & { cls: string; label: string | null }) {
  const [draft, setDraft] = useState(String(page + 1));

  // Prev/Next ile sayfa değişince girdi senkron kalsın.
  useEffect(() => { setDraft(String(page + 1)); }, [page]);

  const lastPage = derivedLastPage(count, total, pageSize);
  const atEnd = lastPage !== null ? page >= lastPage : !hasMore;

  const commit = (e?: FormEvent) => {
    if (e) e.preventDefault();
    const n = parseInt(draft, 10);
    if (isNaN(n) || n < 1) { setDraft(String(page + 1)); return; }
    let target = n - 1;
    if (lastPage !== null) target = Math.min(target, lastPage);
    target = Math.max(0, target);
    if (target !== page) onPage(target);
    setDraft(String(target + 1));
  };

  return (
    <div className={cls} data-pager-mode="offset">
      <Button variant="secondary" size="sm"
        onClick={() => onPage(Math.max(0, page - 1))} disabled={page === 0}>
        ← Prev
      </Button>

      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}>
        <span>Page</span>
        <form onSubmit={commit} style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
          <input value={draft}
            onChange={e => setDraft(e.target.value)}
            // Ö5 (v0.9.1014) — commit YALNIZ Enter'da. Öncesinde `onBlur`
            // da commit ediyordu ve bu iki şekilde ısırıyordu: (a) yarım
            // yazılmış bir sayı (operatör "12"yi silip "3" yazacakken
            // sekmeye bastı) sessizce bir fetch tetikliyordu; (b) Tab ile
            // şeritte gezinmek sayfa atlatıyordu. Blur artık GERİ ALIR —
            // "yazdım ama onaylamadım" hâli kaybolmuş sayılır, uydurulmaz.
            onBlur={() => setDraft(String(page + 1))}
            inputMode="numeric"
            aria-label="Go to page"
            title="Enter ile git"
            style={{
              width: 56, textAlign: 'center', fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
              fontVariantNumeric: 'tabular-nums', padding: '3px 6px',
            }} />
          {lastPage !== null && (
            <span style={{ color: 'var(--text3)' }}>/ {lastPage + 1}</span>
          )}
        </form>
        {/* Tavanlı/yaklaşık sayı son sayfayı SÜRMEZ ama görünür kalır. */}
        {lastPage === null && label && (
          <span style={{ color: 'var(--text2)' }}>· {label}</span>
        )}
        {extras && <span style={{ color: 'var(--text2)' }}>· {extras}</span>}
      </span>

      {/* Gutenberg: ileri eylemi SAĞDA ve şeritteki TEK vurgulu kontrol. */}
      <Button variant="primary" size="sm" onClick={() => onPage(page + 1)} disabled={atEnd}>
        Next →
      </Button>
      {lastReachablePage !== undefined && lastReachablePage > page && (
        <Button variant="secondary" size="sm" onClick={() => onPage(lastReachablePage)}
          title={`Son sayfaya git (${lastReachablePage + 1})`}>
          Last ⇥
        </Button>
      )}
    </div>
  );
}
