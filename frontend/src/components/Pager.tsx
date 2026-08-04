import { useEffect, useState, FormEvent } from 'react';

// Pager — shared "← Prev / page input / Next →" strip used at the
// bottom of long tables (/traces, /logs, future /metrics views).
//
// Behaviour:
//   - Page input is 1-based for the user, 0-based for the caller.
//     Internal state caches the typed value so the user can edit
//     freely without each keystroke firing a fetch; submit on
//     Enter (or input blur) commits the new page number.
//   - When `total` is known we cap the page input at the last page.
//     When it isn't (skip-count mode on /traces) we trust hasMore
//     and accept any positive number.
//   - `extras` slot lets the caller drop "showing N+ · sorted by X"
//     style metadata in the middle of the strip without losing the
//     centred Page-input layout.
export function Pager({
  page, pageSize, total, hasMore, onPage, extras, stickyBottom, lastReachablePage,
}: {
  page: number;
  pageSize: number;
  total?: number;
  hasMore?: boolean;
  // v0.9.645 — sayfanın dibine yapış. Uzun listede "Next" ekranın
  // dışında kalıyordu (operatör-bildirimli: "next butonu çok
  // gözükmüyor, daha iyi bir yerde olabilir mi").
  stickyBottom?: boolean;
  // v0.9.645 — SON sayfaya atlama, YALNIZ ulaşılabilirse.
  //
  // `total`'dan türetilmiyor ve bu bilinçli: v0.9.638'de total'ı
  // Pager'dan kestik çünkü MV yolunun aşama-1 kimlik bütçesi sınırlı
  // ve ötesindeki sayfalar SUNULAMIYOR. Çağıran, sayının hem KESİN
  // hem ulaşılabilir olduğunu doğruladığında bu değeri veriyor;
  // vermezse Last butonu hiç çizilmiyor.
  lastReachablePage?: number;
  onPage: (next: number) => void;
  extras?: React.ReactNode;
}) {
  const [draft, setDraft] = useState(String(page + 1));

  // Keep the input synced when the page changes via Prev/Next.
  useEffect(() => { setDraft(String(page + 1)); }, [page]);

  const lastPage = total !== undefined ? Math.max(0, Math.ceil(total / pageSize) - 1) : null;
  const atEnd = total !== undefined ? (page + 1) * pageSize >= total : !hasMore;

  const submit = (e?: FormEvent) => {
    if (e) e.preventDefault();
    const n = parseInt(draft, 10);
    if (isNaN(n) || n < 1) {
      setDraft(String(page + 1));
      return;
    }
    let target = n - 1;
    if (lastPage !== null) target = Math.min(target, lastPage);
    target = Math.max(0, target);
    if (target !== page) onPage(target);
    setDraft(String(target + 1));
  };

  return (
    <div className={`pager${stickyBottom ? ' is-sticky-bottom' : ''}`}>
      <button className="sec" onClick={() => onPage(Math.max(0, page - 1))} disabled={page === 0}>
        ← Prev
      </button>

      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}>
        <span>Page</span>
        <form onSubmit={submit} style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
          <input value={draft}
            onChange={e => setDraft(e.target.value)}
            onBlur={() => submit()}
            inputMode="numeric"
            aria-label="Go to page"
            style={{
              width: 56, textAlign: 'center', fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
              fontVariantNumeric: 'tabular-nums', padding: '3px 6px',
            }} />
          {lastPage !== null && (
            <span style={{ color: 'var(--text3)' }}>/ {lastPage + 1}</span>
          )}
        </form>
        {extras && <span style={{ color: 'var(--text2)' }}>· {extras}</span>}
      </span>

      {/* v0.9.645 — Next artık BİRİNCİL vurgulu: `sec` ile diğer
          kontrollerden ayrışmıyordu ve uzun listede gözden kaçıyordu. */}
      <button onClick={() => onPage(page + 1)} disabled={atEnd}>
        Next →
      </button>
      {lastReachablePage !== undefined && lastReachablePage > page && (
        <button className="sec" onClick={() => onPage(lastReachablePage)}
          title={`Son sayfaya git (${lastReachablePage + 1})`}>
          Last ⇥
        </button>
      )}
    </div>
  );
}
