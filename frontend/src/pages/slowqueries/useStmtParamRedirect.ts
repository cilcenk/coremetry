import { useEffect } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { decodeStmtParam, stmtDetailHref } from './stmtParam';
import type { TimeRange } from '@/lib/types';

/**
 * useStmtParamRedirect — v0.9.1374.
 *
 * v0.9.1374 öncesinde üç yüzey (`/databases`, `/databases/slow-queries`,
 * `/database`) satır tıklamasında `?stmt=<hash>|<system>` YAZIYOR ve o
 * parametreyi görünce bir çekmece açıyordu. Çekmece emekli oldu, hedef
 * kendi sayfası. Ama parametre kimlik grameridir ve o gramerle kurulmuş
 * linkler DIŞARIDA yaşıyor: operatörün yer imleri, paylaşılmış URL'ler,
 * açık kalmış sekmeler.
 *
 * Onları öylece bırakmak, `stmtParam`'ın kendi şerhinin adını koyduğu
 * hataya düşmek olurdu (v0.9.1323): parametre okunur, hiçbir şey açılmaz,
 * ekran sessizce katalog olarak kalır — 404 bile değil, "düğme bozuk"
 * diye okunan bir hiçlik. Bu bir geriye-dönük uyumluluk şimi DEĞİL;
 * kaldırılan şey ÇEKMECEYDİ, kimlik grameri değil, ve o gramer hâlâ
 * geçerli bir cevaba karşılık geliyor.
 *
 * `replace: true`: geri tuşu operatörü yeniden yönlendirilen adrese
 * atmamalı, geldiği yere götürmeli.
 */
export function useStmtParamRedirect(range: TimeRange): void {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const raw = params.get('stmt');
  useEffect(() => {
    if (!raw) return;
    const ref = decodeStmtParam(raw);
    // Bozuk bir `?stmt=` DEĞİŞMEDEN kalır: onu sayfaya yönlendirmek,
    // operatörü kendi boş-durumunu açıklayan bir sayfaya atmak olurdu.
    // Çöp parametre burada da eskisi gibi sessizce yok sayılıyor.
    if (!ref) return;
    const href = stmtDetailHref(ref, range);
    if (href) navigate(href, { replace: true });
  }, [raw, range, navigate]);
}
