// addressScope — /database kimliğinin kaç FİZİKSEL adresi kapsadığının
// beyanı (v0.10.19, F0.8'in ölçümlü yarısı).
//
// ── KUSUR ───────────────────────────────────────────────────────────────
//
// Sayfanın Scope satırı `system / instance` yazıyor ve operatör bunu bir
// makine sanıyor. `instance` aslında `peer_service`: MV onu ORDER BY
// anahtarı olarak kullanıyor, yani aynı peer.service'i paylaşan farklı
// `server.address` değerleri TEK satıra çöküyor.
//
// Çökme kasıtlı ve doğru — topoloji grafiğinde çekirdek bankacılık
// veritabanı tek düğüm olmalı. Kusur çökmenin kendisi değil, Scope
// satırının bunu SÖYLEMEMESİ: bir Oracle RAC SCAN adresi ile Data Guard
// yedeğinin TOPLAMI, tek bir makinenin sayısı gibi okunuyor.
//
// ── SESSİZLİK SÖZLEŞMESİ ────────────────────────────────────────────────
//
// Prob koşmadıysa (`probed=false`) HİÇBİR ŞEY ilan edilmiyor. Buradaki
// tuzak, ölçülmemiş sonucu "1 adres" diye okumak: o an tekilliği YANLIŞ
// yere iddia etmiş oluruz ve bu, susmaktan kötüdür.

import type { PhysicalAddrs } from '@/lib/types';

export interface AddressScopeNotice {
  /** Kısa etiket — Scope satırının yanına. */
  label: string;
  /** Uzun gerekçe — title/tooltip. */
  detail: string;
  /** Çokluk VAR mı — arayüz bunu vurgulamak için kullanır. */
  multiple: boolean;
}

/**
 * addressScopeNotice — Scope satırına eklenecek adres beyanı.
 *
 * null dönerse hiçbir şey ilan edilmez:
 *   • prob koşmadı        → ölçüm yok, iddia da yok
 *   • adres listesi boş   → span'lerde server.address yok (eski SDK)
 */
export function addressScopeNotice(
  p: PhysicalAddrs | undefined,
  instance: string,
): AddressScopeNotice | null {
  if (!p || !p.probed) return null;
  const addrs = p.addrs ?? [];
  if (addrs.length === 0) return null;

  if (addrs.length === 1) {
    // Tekillik de bir bilgi: operatör "bu gerçekten tek makine mi"
    // sorusunu ölçüyle cevaplamış oluyor. Ama yalnız prob KOŞTUYSA
    // söylenebilir — yukarıdaki kapı onu garanti ediyor.
    return {
      label: '1 fiziksel adres',
      detail: `Bu pencerede ${instance} tek bir fiziksel adrese çözüldü: ${addrs[0]}`,
      multiple: false,
    };
  }

  const n = p.capped ? `${addrs.length}+` : `${addrs.length}`;
  return {
    label: `${n} fiziksel adres`,
    detail:
      `⚠ Aşağıdaki sayılar ${n} ayrı fiziksel adresin TOPLAMI. ` +
      `"${instance}" bir makine değil, bir peer.service etiketi; aynı etiketi ` +
      `paylaşan adresler tek satıra çöküyor (ör. RAC SCAN + Data Guard). ` +
      `Bu pencerede görülenler: ${addrs.join(', ')}` +
      (p.capped ? ` (liste kırpıldı — daha fazlası olabilir)` : ''),
    multiple: true,
  };
}
