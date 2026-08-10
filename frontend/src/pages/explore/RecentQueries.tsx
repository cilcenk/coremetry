import { useEffect, useMemo, useRef, useState } from 'react';
import { useEscLayer } from '@/lib/escLayer';
import { Button } from '@/components/ui/Button';
import { historyItemView, type QueryHistoryEntry } from './useQueryHistory';

// RecentQueries (v0.9.849) — "Son sorgular ▾".
//
// useQueryHistory v0.9.562'den beri halkayı YAZIYORDU ama hiçbir yer
// OKUMUYORDU: tek tüketicisi giriş ekranının soru kartlarıydı ve o ekran
// kaldırılınca hook öksüz kaldı. Yani her düzenleme localStorage'a bir kayıt
// düşürüyor, kayıt hiçbir zaman geri gösterilmiyordu.
//
// 4 SLOT — halkanın kendi kapasitesi (MAX_HISTORY). Daha uzun bir liste
// "arama" ister; bu düğmenin işi arama değil, "az önce neye bakıyordum".
//
// UYGULAMA = NAVİGASYON. Kayıt Phase-1'den beri tam arama dizesini ('?…')
// saklıyor, o yüzden geri dönüş builder state'ini elle kurmak değil o URL'e
// gitmektir; ExplorePage'in imza-anahtarı (v0.9.805) dışarıdan gelen bu
// URL'i kendi yazımızdan ayırıp remount tetikliyor ve ExploreInner URL'i
// mount'ta bir kez okuyor. Yani mekanizma zaten yerinde — eksik olan tek şey
// listeyi göstermekti.
//
// PUSH, replace DEĞİL: sayfanın kendi state→URL yazımı replace kullanır
// (geçmişi kirletmemek için), ama bu bir operatör NAVİGASYONUDUR ve
// tarayıcının geri düğmesi onu geri alabilmeli.
export function RecentQueries({ history, onApply }: {
  history: QueryHistoryEntry[];
  onApply: (search: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);

  // Click-outside + Esc. Dinleyici YALNIZ açıkken bağlı (FacetMultiSelect
  // deseni) — kapalı bir düğme sayfaya global dinleyici bırakmaz.
  useEffect(() => {
    if (!open) return;
    const onDown = (ev: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(ev.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onDown);
    return () => document.removeEventListener('mousedown', onDown);
  }, [open]);
  // v0.9.950 (E2/Ö28) — Esc KATMAN (FacetMultiSelect deseni).
  useEscLayer(open, () => setOpen(false));

  // Göreli zaman AÇILIŞTA bir kez donuyor: liste açıkken tik tik güncellemek
  // bir zamanlayıcı + render döngüsü demek olurdu ve "3 dk önce"nin 4'e
  // dönmesini kimse beklemiyor. now, panel her açıldığında tazeleniyor.
  const items = useMemo(
    () => (open ? history.map(h => historyItemView(h, Date.now())) : []),
    [open, history]);

  // Boşsa düğme HİÇ çizilmez — tıklandığında boş bir kutu açan düğme,
  // olmayan düğmeden kötüdür.
  if (history.length === 0) return null;

  return (
    <div ref={rootRef} style={{ position: 'relative' }}>
      <Button variant="secondary" size="sm"
        aria-expanded={open}
        onClick={() => setOpen(o => !o)}
        title="Bu tarayıcıda en son çalıştırdığın sorgular — tıkla, aynen geri yükle">
        ⟲ Son sorgular ▾
      </Button>
      {open && (
        <div role="listbox" aria-label="Son sorgular"
          style={{
            position: 'absolute', top: '100%', right: 0, zIndex: 'var(--z-dropdown)', marginTop: 4,
            minWidth: 320, maxWidth: 560,
            background: 'var(--bg1)', border: '1px solid var(--border)',
            borderRadius: 8, padding: 4,
            boxShadow: '0 8px 24px rgba(0,0,0,.28)',
          }}>
          {items.map((it, i) => (
            <button key={`${it.text}:${i}`} type="button" role="option"
              aria-selected={false}
              disabled={!it.search}
              title={it.search
                ? it.title
                : `${it.title}\n(bu kayıt geri yüklenemiyor — eski/bozuk biçim)`}
              onClick={() => { if (it.search) { setOpen(false); onApply(it.search); } }}
              style={{
                all: 'unset', boxSizing: 'border-box',
                display: 'flex', alignItems: 'baseline', gap: 8,
                width: '100%', padding: '6px 8px', borderRadius: 5,
                cursor: it.search ? 'pointer' : 'not-allowed',
                opacity: it.search ? 1 : 0.5,
                fontSize: 12, color: 'var(--text)',
              }}
              onMouseEnter={e => { e.currentTarget.style.background = 'var(--bg3)'; }}
              onMouseLeave={e => { e.currentTarget.style.background = 'transparent'; }}>
              <span style={{
                flex: 1, minWidth: 0,
                overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
              }}>{it.text}</span>
              <span style={{ flexShrink: 0, fontSize: 10.5, color: 'var(--text3)' }}>
                {it.when}
              </span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
