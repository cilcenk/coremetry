import { useCallback, useEffect, useRef, type RefObject } from 'react';

// stickToBottom — v0.10.650 (ai-ui-patterns bulgusu #4). İki sohbet yüzeyi
// de her `turns` değişiminde (yani her delta'da) dibe kaydırıyordu: operatör
// yukarıdaki cevabı okurken her token onu dibe geri fırlatıyordu. Kalıp:
// yalnız zaten dipteyken yapış; kullanıcı yukarı kaydırdıysa rahat bırak;
// kullanıcı yeni soru gönderince (pin) her hâlükârda dibe in.
//
// Delta yapışması ANLIK (scrollTop ataması): smooth kaydırma sırasında ara
// konumlar "dipte değil" okunur ve yapışma kendi kendini kapatırdı. Smooth
// yalnız pin'de.

/** Dibe bu kadar px yakınsa "dipte" sayılır (son satırın yarısı payı). */
export const STICK_THRESHOLD_PX = 48;

export function isNearBottom(
  el: { scrollTop: number; scrollHeight: number; clientHeight: number },
  threshold = STICK_THRESHOLD_PX,
): boolean {
  return el.scrollHeight - el.scrollTop - el.clientHeight <= threshold;
}

/**
 * findScrollParent — el'in kendisi ya da en yakın kaydırılabilir atası
 * (overflow-y auto/scroll). Bulunamazsa null (çağıran yapışmayı atlar).
 */
export function findScrollParent(el: HTMLElement | null): HTMLElement | null {
  let cur: HTMLElement | null = el;
  while (cur) {
    const oy = (cur.style.overflowY || (typeof getComputedStyle === 'function' ? getComputedStyle(cur).overflowY : '')) ?? '';
    if (oy === 'auto' || oy === 'scroll') return cur;
    cur = cur.parentElement;
  }
  return null;
}

/**
 * useStickToBottom — `ref` (kaydırma kabı ya da kabın içindeki bir düğüm;
 * kap findScrollParent ile bulunur) için: deps değişince yalnız kullanıcı
 * dipteyse dibe kaydır. Dönen `pin()` koşulsuz dibe indirir ve yapışmayı
 * yeniden açar (yeni soru gönderimi).
 */
export function useStickToBottom(ref: RefObject<HTMLElement | null>, deps: unknown[]) {
  const stuck = useRef(true);
  const elRef = useRef<HTMLElement | null>(null);

  useEffect(() => {
    const el = findScrollParent(ref.current);
    elRef.current = el;
    if (!el) return;
    const onScroll = () => { stuck.current = isNearBottom(el); };
    el.addEventListener('scroll', onScroll, { passive: true });
    return () => el.removeEventListener('scroll', onScroll);
  }, [ref]);

  useEffect(() => {
    const el = elRef.current ?? findScrollParent(ref.current);
    if (!el || !stuck.current) return;
    el.scrollTop = el.scrollHeight;
    // deps çağıranın listesi (turns/open): kuralın statik doğrulaması bilerek atlanır.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);

  return useCallback(() => {
    stuck.current = true;
    const el = elRef.current ?? findScrollParent(ref.current);
    if (!el) return;
    if (typeof el.scrollTo === 'function') el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' });
    else el.scrollTop = el.scrollHeight;
  }, [ref]);
}
