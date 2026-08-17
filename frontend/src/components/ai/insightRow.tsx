import { useCallback } from 'react';
import { useSearchParams } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import { useEscLayer } from '@/lib/escLayer';
import { InsightCard } from '@/components/ai/InsightCard';
import type { InsightKind } from '@/lib/types';

// insightRow — InsightCard'ın SATIR YUVASI (v0.9.1133, AI Faz 2.3;
// onaylı mockup: satır-altı tıkla-açıl, liste-üstü kart feed'i DEĞİL).
//
// Kart (v0.9.1132) bilerek URL-AGNOSTİK yazıldı: yalnız prop alır. Adres
// disiplini burada, tek yerde — iki host (exception satırı + problem
// satırı) aynı kodeği, aynı Esc katmanını ve aynı "tek kart açık"
// kuralını paylaşıyor. İkinci host kendi yazımını doğurursa (iki ayrı
// param, iki ayrı Esc dinleyicisi) "kapı kapsamı göçte erir" sınıfı
// başlar; o yüzden yuva bir MODÜL, bir kalıp değil.
//
// ── NEDEN `?insight=`, `?ai=` DEĞİL ─────────────────────────────────
//
// `?ai=` TEK tüketicili: AppShell'de mount olan AIDrawer onu görüp sağ
// kenar çekmecesini açıyor (AIDrawer.tsx). Kart kind'larını AI_KINDS'a
// eklemek aynı adreste HEM satır kartını HEM çekmeceyi açardı (v0.9.479'da
// operatörün bildirdiği "iki AI yüzeyi üst üste") — gerekçenin tamamı
// InsightCard.tsx başındaki nottadır.
//
// ── NEDEN HOST'UN MEVCUT SATIR PARAMI DA DEĞİL ──────────────────────
//
// Her iki host'ta da satır kimliği zaten adreste yaşıyor AMA ikisi de
// TAM SAYFA detayı açıyor: `?exc=<fingerprint>` (AnomaliesPage listeyi
// komple bırakıp ProblemDetail'e geçer) ve `?problem=<id>` (Variant-B
// AlertProblemHost). Kartı o paramlara bindirmek, mockup'ın açıkça
// KORUMASINI istediği satır→detay gezinmesini kartla DEĞİŞTİRMEK
// olurdu. `?insight=` üçüncü bir eksen: satır yerinde kalır, kanıt
// altında açılır, ve link paylaşıldığında aynı satır aynı kartla açılır.
export const INSIGHT_PARAM = 'insight';

/**
 * insightParams — SAF kodek: `?insight=` yazar/siler, yabancı her
 * parametreyi korur.
 *
 * `live` (window.location.search) TABAN, `prev` (router konumu) üstüne
 * eklenir: Trace/Dashboard/Traces sayfaları URL'lerini ham
 * `history.replaceState` ile yazdığı için router'ın `prev`i BAYAT bir
 * alt küme olabiliyor ve yalnız prev'i kopyalamak operatörün seçili
 * span'ini adresten silerdi (v0.8.256/265/267'de üç kez gemiye giden
 * yabancı-param kaybı sınıfı). Canlı adres her zaman üst kümedir;
 * router onu geriden takip eder, asla önden gitmez. Emsal:
 * useAiSubject.ts (v0.9.477).
 */
export function insightParams(
  prev: URLSearchParams, live: string, id: string | null,
): URLSearchParams {
  const next = new URLSearchParams(live || prev.toString());
  prev.forEach((v, k) => { if (!next.has(k)) next.append(k, v); });
  if (id) next.set(INSIGHT_PARAM, id);
  else next.delete(INSIGHT_PARAM);
  return next;
}

export interface InsightRowState {
  /** Açık satırın kimliği (fingerprint / problem id), yoksa null. */
  openId: string | null;
  /** Aynı satır → kapat, başka satır → oraya taşı (TEK kart açık). */
  toggle: (id: string) => void;
  close: () => void;
}

/**
 * useInsightRow — açık kartın kimliği ADRESTEN okunur/yazılır.
 *
 * Yerel bir `useState` AYNASI BİLEREK YOK: URL tek kaynak. Ayna tutmak,
 * bu deponun üç kez gemiye giden hata sınıfını (URL'den tohumla, geri
 * yazma) davet ederdi — ve sig-guard tartışması da ancak ayna varsa
 * doğar. Ayna olmadığı için "tek kart açık" kuralı da bedava geliyor:
 * bir param tek değer taşır.
 */
export function useInsightRow(): InsightRowState {
  const [searchParams, setSearchParams] = useSearchParams();
  const openId = searchParams.get(INSIGHT_PARAM);

  const write = useCallback((id: string | null) => {
    setSearchParams(prev => insightParams(
      prev, typeof window !== 'undefined' ? window.location.search : '', id,
    ), { replace: true });
    // replace:true — triyaj sırasında açılıp kapanan kartlar history'ye
    // durak yığmamalı; "geri" operatörü listeye değil bir önceki SAYFAYA
    // götürmeli (ev kuralı, frontend-conventions §4).
  }, [setSearchParams]);

  const close = useCallback(() => write(null), [write]);
  const toggle = useCallback(
    (id: string) => write(openId === id ? null : id), [write, openId]);

  // Esc = kartı kapat. Katman yığını üzerinden (v0.9.950): kendi document
  // dinleyicisini kuran bir yüzey, üstünde açık bir modal/çekmece varken
  // de ateşler ve yanlış katmanı düşürür.
  useEscLayer(openId !== null, close);

  return { openId, toggle, close };
}

/**
 * InsightRowChip — satırdaki "▸ Ne oldu?" affordance'ı.
 *
 * KENDİ TIK HEDEFİ: `stopPropagation` olmadan satırın navigate-on-click'i
 * de ateşlerdi, yani kart açılırken operatör tam sayfa detaya ışınlanırdı.
 * Aynı şey KLAVYEDE de geçerli ve orada daha sinsi — her iki host'un
 * `<tr>`si `role="button"` + `onKeyDown` ile Enter/Space'i satır açma
 * olarak yorumluyor, dolayısıyla çipe odaklanıp Enter'a basmak hem çipi
 * tıklar hem satırı açardı. Bu yüzden keydown da durduruluyor (buton
 * kendi native tıkını yine üretir).
 */
export function InsightRowChip({ open, onToggle, title }: {
  open: boolean;
  onToggle: () => void;
  title?: string;
}) {
  return (
    <Button variant="accent" size="xs"
      aria-expanded={open}
      title={title ?? 'Bu satır için ilişkili sinyalleri topla ve ne olduğunu anlat (AI)'}
      onClick={e => { e.stopPropagation(); onToggle(); }}
      onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') e.stopPropagation(); }}>
      {open ? '▾' : '▸'} Ne oldu?
    </Button>
  );
}

/**
 * InsightRowSlot — kartı tabloda satırın ALTINDA taşıyan `<tr>`.
 *
 * Host bunu KOŞULLU çiziyor (`openId === id && …`), yani kart yalnız
 * açıkken mount olur ve kapanınca unmount edilir. Maliyet disiplini
 * tam burada: mount = tek üretim (ES projeksiyonu + LLM), unmount =
 * uçuştaki akışın iptali. Ne prefetch ne poll — kapalı satır SIFIR
 * istek.
 *
 * `padding: 0` bilinçli: dolgu, kenarlık ve zemin kartın kendi
 * `cardStyle`'ında; td'nin de dolgu eklemesi kartı kabuk içinde ikinci
 * bir çerçeveye sokardı.
 */
export function InsightRowSlot({ kind, id, colSpan, onClose }: {
  kind: InsightKind;
  id: string;
  colSpan: number;
  onClose: () => void;
}) {
  return (
    <tr data-insight-row={id}>
      <td colSpan={colSpan} style={{ padding: 0 }}>
        <InsightCard kind={kind} id={id} onClose={onClose} />
      </td>
    </tr>
  );
}
