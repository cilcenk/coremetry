// Trace.identityMenu.pin.test.ts — v0.10.568 (operatör, 2026-09-08).
//
// Sözleşme: "Farklı function_id'ler alt span'lerde ama aynı trace'te olabilir…
// kullanıcıya hangi function_id'ye gitmek istersin diye seçenek verelim."
// Bugüne kadar kazananı sunucu SESSİZCE seçiyordu.
//
// Neden kaynak taraması: kuralın saf çekirdeği (identityKeysFromLinks /
// identityOverrideCtx / shortIdentity / identityRoleTR) lib/externalLinks.
// test.ts'te yeşil — ama o testler yeşilken menü ekranda HİÇ olmayabilir
// ("test edilmiş ama ulaşılamaz", v0.9.1334 sınıfı). Burada pinlenen şey
// çekirdeğin doğruluğu değil, ÇEKİRDEĞE GİDEN BAĞ:
//   1. anahtar kümesi şablonlardan türüyor VE sorgu anahtarına giriyor,
//   2. aday ≤ 1 iken menü YOK (bugünkü tek düğme birebir korunuyor),
//   3. > 1 iken ok + role="menu" + role="menuitem" satırları,
//   4. satır tıklaması AYNI şablonu override'lı ctx ile çözüyor,
//   5. Esc katmanı bağlı ve ham window.open noopener taşıyor.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// Yorumları at: pinlenen metinlerin çoğu bu dosyanın kendi açıklama
// cümlelerinde de geçiyor (v0.10.568'in "kimlik menüsü" başlıkları), yorum
// eşleşmesi sahte yeşil verirdi — "gate kendi metnini ısırır" sınıfı.
const raw = readFileSync(resolve(__dirname, 'Trace.tsx'), 'utf8');
const src = raw.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');

describe('Trace kimlik seçim menüsü (v0.10.568)', () => {
  it('anahtar kümesi ŞABLONLARDAN türer ve descriptor’a geçer', () => {
    expect(src).toContain("import { renderExternalLink, collectLinkCtx, pickGroupedLinks, identityKeysFromLinks, identityOverrideCtx, shortIdentity, identityRoleTR, type ExternalLinkCtx } from '@/lib/externalLinks';");
    // links'ten türer — sabit bir liste ya da "tüm attribute'lar" DEĞİL.
    expect(src).toContain('const idKeys = identityKeysFromLinks(links);');
    expect(src).toContain('api.traceLinkIdentity(traceId, selectedSpanId ?? undefined, idKeys, signal)');
  });

  it('anahtar kümesi SORGU ANAHTARINA girer (küme değişince yeniden çekilir)', () => {
    expect(src).toContain("const keysParam = idKeys.join(',');");
    expect(src).toMatch(/queryKey: \['trace-link-identity', traceId, selectedSpanId \?\? '', keysParam\]/);
    // Anahtar kümesi anahtara girmezse admin yeni bir `requires` eklediğinde
    // React Query eski (dar) cevabı taze sayar ve menü o anahtarı hiç
    // göstermez. Eski 3 bileşenli anahtar geri gelmesin.
    expect(src).not.toMatch(/queryKey: \['trace-link-identity', traceId, selectedSpanId \?\? '']/);
  });

  it('aday ≤ 1 ise menü YOK — bugünkü tek düğme birebir', () => {
    expect(src).toContain('if (identities.length <= 1) return mainBtn(false);');
    // Aday listesi ASLA null: eski (identities taşımayan) sunucuya karşı da
    // karar tek yerde verilir.
    expect(src).toContain('const identities = ident?.identities ?? [];');
    // Ana tık DEĞİŞMEDİ: kazanan kimliğin url'i, yeni sekmede.
    expect(src).toContain("onClick={() => window.open(url, '_blank', 'noopener,noreferrer')}");
  });

  it('aday > 1 ise ok tetiği + role="menu" / role="menuitem" satırları', () => {
    // Tetik hand-rolled <button> değil, aria-label'lı IconButton (a11y kapısı).
    expect(src).toContain("import { IconButton, MenuItem } from '@/components/ui';");
    expect(src).toContain('<IconButton');
    expect(src).toMatch(/aria-label=\{`\$\{l\.label\}: kimlik seç \(\$\{identities\.length\} aday\)`\}/);
    expect(src).toContain('aria-haspopup="menu"');
    expect(src).toContain('aria-expanded={open}');
    expect(src).toContain('icon="▾"');
    expect(src).toContain('role="menu"');
    // Satırlar MenuItem atomundan geliyor — role="menuitem" atomda basılı.
    expect(src).toContain('<MenuItem');
    expect(src).not.toMatch(/<button[\s\S]{0,80}role="menuitem"/);
    // Başlık + aday sayısı (onaylanmış mockup).
    expect(src).toContain('Hangi işlem?');
    expect(src).toContain('{identities.length} farklı kimlik');
  });

  it('satır tıklaması AYNI şablonu override’lı ctx ile çözer', () => {
    expect(src).toContain('const octx = identityOverrideCtx(ctx, cand);');
    // Şablon yeniden yazılmıyor: düğmenin çizdiği linkin ta kendisi (l).
    expect(src).toContain('renderExternalLink(l.urlTemplate, octx)');
    // Çözülmeyen aday PASİF satır, title eksikleri söyler (sessizce kaybolmaz).
    expect(src).toContain('disabled={!cu}');
    expect(src).toContain('bu kimlikle çözülemeyen alanlar — ${cm.join(\', \')}');
    // Kısaltma + rol etiketi + tam değer title'da.
    expect(src).toContain('shortIdentity(cand.value)');
    expect(src).toContain('identityRoleTR(cand.role)');
    expect(src).toContain('{[cand.service, cand.spanName].filter(Boolean).join(\' · \')}');
    // used / isError işaretleri.
    expect(src).toContain("icon={cand.used ? '●' : cand.isError ? '⚠' : ''}");
  });

  it('gruplama korunur: menü, o grupta ÇİZİLEN linkin şablonuna göre çözer', () => {
    // v0.10.566 pickGroupedLinks satırı ExternalLinkRow'a `link` olarak akar.
    expect(src).toContain('const rows = pickGroupedLinks(links, l =>');
    expect(src).toContain('<ExternalLinkRow key={l.label} link={l} url={url} missing={missing}');
    // request_id adayları üstte, span adayları anahtar anahtar altta.
    expect(src).toContain("const logItems = resolved.filter(r => r.cand.source === 'log');");
    expect(src).toContain("{ title: 'log gövdesinden', items: logItems }");
  });

  it('Esc KATMANI bağlı, dışa tık kapatır, window.open noopener taşır', () => {
    expect(src).toContain('useEscLayer(open, () => { setOpen(false); triggerRef.current?.focus(); });');
    expect(src).toContain('useOutsideClose(wrapRef, open, close);');
    // Ham window.open çağrılarının HEPSİ noopener,noreferrer taşır — bir
    // reverse-tabnabbing açığı tek bir eksik argümanla doğar.
    const opens = src.match(/window\.open\([^)]*\)/g) ?? [];
    expect(opens.length).toBeGreaterThanOrEqual(2);
    for (const o of opens) expect(o).toContain("'_blank', 'noopener,noreferrer'");
  });
});

// v0.10.570 — operatör-raporlu (prod ekran görüntüsü): ok düğmesi ana
// düğmeden kısa kalıp basamak yapıyordu (.btn-icon.ib-md sabit 28×28 kare,
// Button md dolgudan daha uzun). Yükseklik kardeşten gelmeli.
describe('split düğme hizası', () => {
  it('ok yüksekliği kardeşten alır (stretch + height auto)', () => {
    expect(src).toContain("alignSelf: 'stretch'");
    expect(src).toContain("height: 'auto'");
    // Ortak kenarlık hâlâ tek piksel: iki hedef, tek kontrol.
    expect(src).toContain('marginLeft: -1');
  });
});
