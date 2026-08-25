import { serviceHref } from '@/lib/serviceHref';
import { endpointDetailHref } from '@/pages/endpoints/endpointParam';

// spanEntityLinks — span detayındaki değerleri VARLIK sayfalarına bağlar
// (v0.10.34, operatör isteği).
//
// ── NEDEN ───────────────────────────────────────────────────────────────
//
// Span detayında `Service` mavi ve tıklanabilir; operatör oradan servis
// sayfasına iniyor. Ama `http.route` DÜZ METİNDİ — oysa endpoint da tam
// anlamıyla bir VARLIK: kendi sayfası (/endpoint), kendi RED'i, kendi
// baseline'ı, kendi trace listesi var.
//
// Operatörün cümlesi: "trace http route ile endpoint arasında service
// name gibi bağlantı olsun". Yani eksik olan veri değil, KENARDI.
//
// ── NEDEN url.path DEĞİL ────────────────────────────────────────────────
//
// ⚠ EN KRİTİK KARAR. Endpoint varlığı ŞABLONLANMIŞ yola göre kuruluyor;
// ham yola değil. Prod'da ikisi gerçekten ayrışıyor:
//
//     url.path   = /BSAWEB/rest/application/directCreditPreApproval
//     http.route = /BSAWEB/application/directCreditPreApproval   ← endpoint bu
//
// `url.path`ten link kurmak, var olmayan bir endpoint'e götürürdü: sayfa
// açılır, boş gelir, operatör "veri yok" sanır. Yanlış bir link,
// linksizlikten KÖTÜDÜR.
//
// ── NEDEN SUNUCUNUN ÇÖZDÜĞÜ DEĞER ───────────────────────────────────────
//
// Route'u ön yüzde attribute'lardan yeniden türetmiyoruz. `spans.http_route`
// ingest'te doldurulan promoted bir kolon (http.route → http.target
// fallback, internal/chstore/endpoints.go) ve SpanRow onu `httpRoute`
// olarak taşıyor. Aynı değeri iki yerde türetmek, /endpoints ile trace'in
// sessizce ayrışacağı bir yüzey açardı.
//
// ── NEDEN YALNIZ GİRİŞ SPAN'İ ───────────────────────────────────────────
//
// Endpoint tablosu yalnız `kind IN ('server','consumer')` span'lerinden
// kuruluyor (giriş-span ilkesi). Bir CLIENT span'inden link kurmak,
// çağıranın servisi ile çağrılanın route'unu birleştirir — var olmayan
// bir (servis, yol) çiftine götürür.

/** Span detayının link kurarken bildiği bağlam. */
export interface SpanLinkCtx {
  /** Uygulama-içi link üretimi açık mı (public trace görüntüleyicide KAPALI). */
  on: boolean;
  window?: { fromNs: number; toNs: number };
  /** Span'in kendi servisi — endpoint kimliğinin yarısı. */
  service?: string;
  /** Span kind'ı; endpoint yalnız giriş span'lerinde var. */
  kind?: string;
  /** Sunucunun çözdüğü şablonlanmış yol (SpanRow.httpRoute). */
  httpRoute?: string;
}

/** Giriş span'i mi — endpoint varlığı yalnız bunlarda var. */
export function isEntrySpanKind(kind: string | undefined): boolean {
  const k = (kind ?? '').toLowerCase();
  return k === 'server' || k === 'consumer';
}

/**
 * spanAttrHref — bir attribute satırı bir varlığa gidiyorsa hedefi verir.
 *
 * null = link yok. Saf: hangi anahtarın hangi varlığa gittiği bir
 * SÖZLEŞME ve testlenebilir olmalı.
 */
export function spanAttrHref(k: string, v: string, ctx: SpanLinkCtx): string | null {
  if (!ctx.on || !v) return null;

  if (k === 'Service' || k === 'service.name' || k === 'peer.service') {
    return serviceHref(v, { range: ctx.window });
  }

  // ── ENDPOINT ──────────────────────────────────────────────────────────
  // YALNIZ http.route. url.path / http.target satırları bilerek dışarıda:
  // endpoint kimliği şablonlanmış yol ve ham yol ondan ayrışıyor.
  if (k === 'http.route') {
    if (!isEntrySpanKind(ctx.kind)) return null;
    const svc = (ctx.service ?? '').trim();
    if (!svc) return null;
    // Sunucunun çözdüğü değer varsa O kullanılıyor; attribute yalnız
    // görüntülenen metin. İkisi ayrışırsa (ingest fallback'i devreye
    // girmişse) doğru olan sunucunun kolonudur.
    const path = (ctx.httpRoute ?? '').trim() || v.trim();
    return endpointDetailHref(
      { service: svc, path, sig: false },
      ctx.window ? { range: `custom:${Math.floor(ctx.window.fromNs / 1e6)}-${Math.floor(ctx.window.toNs / 1e6)}` } : {},
    );
  }
  return null;
}

/**
 * spanEndpointHref — INFO bölümündeki "Endpoint" satırı için.
 *
 * Attribute listesinden bağımsız: span `http.route` attribute'unu
 * TAŞIMASA bile sunucu route'u çözmüş olabilir (http.target fallback),
 * ve o durumda da endpoint'e gidilebilmeli.
 */
export function spanEndpointHref(ctx: SpanLinkCtx): { href: string; label: string } | null {
  if (!ctx.on) return null;
  if (!isEntrySpanKind(ctx.kind)) return null;
  const svc = (ctx.service ?? '').trim();
  const path = (ctx.httpRoute ?? '').trim();
  if (!svc || !path) return null;
  return { href: spanAttrHref('http.route', path, ctx) ?? '', label: path };
}
