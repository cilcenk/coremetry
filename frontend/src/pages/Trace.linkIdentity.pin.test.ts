// Trace.linkIdentity.pin.test.ts — v0.10.566 (operatör kuralı).
//
// Sözleşme: "trace'in loglarının gövdesinde request_id varsa link ONUNLA
// üretilir (channelCode gönderilmez); yoksa bugünkü span-attribute yolu."
// Kimliği sunucu seçer (/api/traces/{id}/link-identity, kazanan span önceliği
// seçili span → ilk hatalı span → root), yani SEÇİLİ SPAN sorguya girmezse
// kural ekranda YOKTUR — pin ettiğimiz şey tam olarak bu bağ.
//
// Kaynak taraması, çünkü kural üç yerde birden yaşıyor: (1) descriptor çağrısı
// + selectedId, (2) attrs ezmesi, (3) grup seçimi. Saf çekirdeğin (lib/
// externalLinks.ts) testi yeşilken bu bağlardan biri kopmuş olabilir —
// "test edilmiş ama ulaşılamaz" sınıfı.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// Yorumları at: bu dosyanın PIN'lediği metinlerin çoğu Trace.tsx'te bir
// açıklama cümlesinde de geçiyor ("{{requestId}}", "pickGroupedLinks"),
// yorum eşleşmesi sahte yeşil verirdi.
const raw = readFileSync(resolve(__dirname, 'Trace.tsx'), 'utf8');
const src = raw.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');
// JSX yorumları ({/* … */}) yukarıdaki blok-yorum kuralıyla zaten düşüyor.

describe('Trace dış link kimliği (v0.10.566)', () => {
  it('descriptor çağrılıyor: api.traceLinkIdentity + signal', () => {
    expect(src).toContain('api.traceLinkIdentity(');
    // İptal edilebilirlik: queryFn signal'i İLETİYOR.
    const call = src.slice(src.indexOf('api.traceLinkIdentity('));
    expect(call.slice(0, call.indexOf(')') + 1)).toContain('signal');
    expect(src).toContain("queryKey: ['trace-link-identity'");
    expect(src).toContain('staleTime: 30_000');
  });

  it('SEÇİLİ SPAN descriptor’a geçiyor (kazanan span önceliği)', () => {
    // Çağrı yeri: <ExternalLinkButtons … selectedSpanId={selectedId} />
    expect(src).toContain('selectedSpanId={selectedId}');
    expect(src).toContain('traceId={id}');
    // Sorgu anahtarı seçili span'i taşır — span değişince kimlik yeniden sorulur.
    // v0.10.568: anahtar kümesi (idKeys) sorgu anahtarının 4. bileşeni oldu;
    // seçili span'in 3. bileşen olarak orada durması bu testin PİN'i.
    expect(src).toMatch(/queryKey: \['trace-link-identity', traceId, selectedSpanId \?\? '', keysParam\]/);
    expect(src).toContain('api.traceLinkIdentity(traceId, selectedSpanId ?? undefined, idKeys, signal)');
  });

  it('descriptor attrs BASE’i ezer ve requestId descriptor’dan gelir', () => {
    expect(src).toContain('collectLinkCtx(spans)');
    expect(src).toContain('const ident = identQ.data;');
    // Birleştirme base.attrs üzerine kurulur, descriptor değerleri üstüne yazar.
    expect(src).toContain('{ ...base.attrs }');
    expect(src).toContain('Object.entries(ident.attrs ?? {})');
    expect(src).toContain('requestId: ident.requestId');
  });

  it('descriptor yoksa/hata verirse ESKİ yola düşülür (geriye dönük)', () => {
    // ctx = descriptor varsa birleşik, yoksa DÜZ base — düğmeler kaybolmaz.
    expect(src).toMatch(/const ctx = base && ident\s*\?/);
    expect(src).toMatch(/\}\s*:\s*base;/);
    // Descriptor hatası düğmeleri gizlemez: render kararı YALNIZ links'e bakar.
    expect(src).toContain('if (links.length === 0) return null;');
    expect(src).not.toContain('identQ.isError');
    expect(src).not.toContain('identQ.isLoading');
  });

  it('düğme listesi pickGroupedLinks üzerinden çiziliyor', () => {
    // v0.10.568 ile aynı import satırına kimlik menüsünün saf yardımcıları eklendi.
    expect(src).toMatch(/import \{ renderExternalLink, collectLinkCtx, pickGroupedLinks[^}]*\} from '@\/lib\/externalLinks';/);
    expect(src).toContain('const rows = pickGroupedLinks(links, l =>');
    expect(src).toContain('rows.map(({ link: l, url, missing })');
    // Ham links üzerinde map YOK (grup seçimi atlanamaz).
    expect(src).not.toContain('{links.map(l =>');
  });

  it('pasif düğme davranışı korunuyor: eksikleri sayar, tooltip söyler', () => {
    expect(src).toContain('bu trace\'te çözülemeyen alanlar — ${missing.join(\', \')}');
    expect(src).toContain('disabled');
    // Kimliğin kaynağı (log gövdesi / span attribute) tooltip'e ekleniyor.
    expect(src).toContain('kimlik: ');
    expect(src).toContain("ident.source === 'log' ? 'log gövdesi'");
    expect(src).toContain('ident.note');
  });
});
