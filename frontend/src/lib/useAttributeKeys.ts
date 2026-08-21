// useAttributeKeys — v0.9.933 (UX denetimi Ö14). Attribute-key KEŞFİ, tek yer.
//
// FilterBuilder'ın içinde yaşıyordu ve orada iyi çalışıyordu. Sorun: /traces'in
// aggregate "group by attribute" kutusu — AYNI SAYFADA, yirmi satır ötede —
// çıplak bir `<input placeholder="attribute key (e.g. user.id)">`ti. Yani
// operatör tam da veri KEŞFETMEYE çalışırken (hangi attribute'a göre gruplasam?)
// anahtarı ezberden yazmak zorundaydı, oysa uygulama o listeye zaten sahipti.
//
// Yanlış yazılan bir anahtarın bedeli sessiz: sorgu koşuyor, sonuç boş dönüyor
// ve ekranda "bu pencerede hiçbir span'de yok" yazıyor — doğru cümle, ama
// operatör onu "böyle veri yok" diye okuyor, "adı farklı" diye değil.
//
// GÖZLENEN ÖNCE, statik SONRA: gerçek veri lider. Ters sıra (v0.5.261 öncesi)
// operatörün KENDİ custom attribute'larını bayat sabit listenin altına
// gömüyordu.

import { useEffect, useMemo, useState } from 'react';
import type { AttrKeyWindow } from './attrKeyWindow';
import { api } from '@/lib/api';
import type { FilterExpr } from '@/lib/types';

// Statik OTel semconv önerileri — gözlenen listede olmayanlar için tamamlayıcı.
// Tempo tarzı scope önekleri nereye bakılacağını seçer:
//   resource.X → resource attribute · span.X → span attribute · X → iyi bilinen
//   kolon varsa o, yoksa span attribute.
export const SUGGESTED_KEYS = [
  'name', 'operation', 'kind', 'status', 'duration_ms',
  'http.method', 'http.route', 'http.status_code',
  'db.system', 'db.statement',
  'rpc.system', 'rpc.method',
  'peer.service', 'messaging.system',
  'span.http.method', 'span.http.route', 'span.http.status_code',
  'span.db.system', 'span.db.statement',
  'span.peer.service',
  'resource.service.name',
  'resource.host.name',
  'resource.deployment.environment',
  'resource.service.version',
  'resource.telemetry.sdk.name',
  'resource.telemetry.sdk.language',
];

export interface ObservedKey { key: string; count: number }

// mergeKeys — gözlenen + statik, SAF. Gözlenen sırayı korur, kopyaları eler.
export function mergeKeys(observed: ObservedKey[], suggested: string[] = SUGGESTED_KEYS): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const o of observed) {
    if (seen.has(o.key)) continue;
    seen.add(o.key);
    out.push(o.key);
  }
  for (const k of suggested) {
    if (seen.has(k)) continue;
    seen.add(k);
    out.push(k);
  }
  return out;
}

// scopedKey — backend'in scope alanını URL/filtre dilindeki ada çevirir.
export function scopedKey(r: { scope: string; key: string }): string {
  return r.scope === 'resource' ? `resource.${r.key}` : r.key;
}

/**
 * Canlı attribute-key keşfi.
 *
 * @param filters bağlam filtreleri; verilirse anahtarlar O DİLİM altında
 *                veri taşıyanlarla sınırlanır (global top-N değil).
 */
export function useAttributeKeys(
  filters?: FilterExpr[],
  window_: AttrKeyWindow = { since: '1h' },
  // v0.9.1200 — metrik-kaynak filtre satırları span attr taramasını hiç
  // istemez (anahtarlar metrik etiketlerinden gelir); enabled=false keşfi
  // atlar, statik liste döner ve çağıran onu zaten yok sayar.
  enabled = true,
): {
  keys: string[];
  observed: ObservedKey[];
} {
  const [observed, setObserved] = useState<ObservedKey[]>([]);
  const sig = JSON.stringify(filters ?? []);
  // v0.9.969 — pencere artık bir NESNE (since | fromNs/toNs). Effect'in dep
  // listesine nesneyi koymak her render'da yeni referans demek olurdu, yani
  // 200 ms'lik debounce sonsuza dek yeniden kurulur ve istek HİÇ atılmazdı.
  // Dize imza, filters'ın zaten kullandığı deseni izliyor.
  const winSig = JSON.stringify(window_);

  // v0.9.602 (traces D4) — debounce + yarış koruması. Öncesinde her chip
  // değişimi ANINDA ateşliyordu (anahtar seç → operatör seç → değer yaz =
  // üç ayrı /api/attribute-keys). Daha sinsi olanı `cancelled` muhafızının
  // yokluğuydu: iki istek uçuştayken GEÇ dönen ESKİ yanıt yeniyi ezebiliyor,
  // yani operatör filtresini daralttıkça daha ESKİ bir liste görebiliyordu.
  useEffect(() => {
    if (!enabled) { setObserved([]); return; }
    let cancelled = false;
    const t = window.setTimeout(() => {
      const parsed = JSON.parse(sig) as FilterExpr[];
      const ctx = parsed.length > 0 ? sig : undefined;
      // v0.9.953 (F3/Ö14c) — pencere SAYFANIN aralığından, basamaklı.
      // Sabit '1h' iken 7 günlük pencereye bakan operatör son bir saatte
      // görülmemiş bir anahtarı öneride bulamıyordu.
      api.attributeKeys(JSON.parse(winSig) as AttrKeyWindow, 500, ctx)
        .then(rows => {
          if (cancelled) return;
          setObserved((rows ?? []).map(r => ({ key: scopedKey(r), count: r.count })));
        })
        .catch(() => { if (!cancelled) setObserved([]); });
    }, 200);
    return () => { cancelled = true; window.clearTimeout(t); };
  }, [sig, winSig, enabled]);

  const keys = useMemo(() => mergeKeys(observed), [observed]);
  return { keys, observed };
}
