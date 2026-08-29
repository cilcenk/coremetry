import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { idLinkPattern, type IdLink } from './inlineIdLinks';

// v0.10.35 — operatör isteği: "id'nin kendisinde link".
//
// CoSRE cevabı `request_id : INVM0130602037…` yazıyor ve operatör o
// kimliği log arayüzünde aramak için ELLE KOPYALIYORDU. Köprü mekanizması
// (answerRequestIDLinks) beş sohbet yüzeyinde vardı — operatörün deyişiyle
// "alttaki bağlantı çalışıyordu" — ama ✨ Explain yüzeylerinde yoktu ve
// arayüz `links` alanını hiç çizmiyordu.
//
// Satır içi hâli AYNI href'i kullanıyor; yeni bir link mantığı yok.

const ID = 'INVM01306020375C0035838312026082523354458293';
const link = (id: string, href = 'https://log/x'): IdLink =>
  ({ id, href, label: 'Logizleme (Prod) · ' + id.slice(0, 12) + '…' });

describe('idLinkPattern', () => {
  it('OPERATÖRÜN KİMLİĞİ — desen dizgenin başında eşleşir', () => {
    const p = idLinkPattern([link(ID)])!;
    expect(p).not.toBeNull();
    // renderInline desenleri HEP `rest`in başında dener.
    expect(`${ID} devamı`.match(p.re)?.[1]).toBe(ID);
    expect(p.byId.get(ID)!.href).toBe('https://log/x');
  });

  it('kimlik ortada başlıyorsa BAŞTA eşleşmez — renderInline sözleşmesi', () => {
    const p = idLinkPattern([link(ID)])!;
    expect(`önce ${ID}`.match(p.re)).toBeNull();
    // renderInline karakter karakter ilerlediği için sıra ona gelecek.
    expect(`${ID}`.match(p.re)).not.toBeNull();
  });

  // ⚠ Kısa kimlik uzun olanın ÖNEKİYSE, alternation kısa olanı eşleştirip
  // uzun kimliği İKİYE BÖLERDİ — operatör yanlış kimliğin linkine giderdi.
  it('UZUN kimlik önce denenir', () => {
    const long = 'REQ12345678901234';
    const short = 'REQ123456789012';
    const p = idLinkPattern([link(short, 'https://kisa'), link(long, 'https://uzun')])!;
    const m = long.match(p.re);
    expect(m?.[1]).toBe(long);
    expect(p.byId.get(m![1])!.href).toBe('https://uzun');
  });

  // Kimlikler nokta / iki nokta / tire içerebiliyor (reqIDToken deseni
  // bunlara izin veriyor); kaçırılmazsa `.` her karakteri eşleştirir ve
  // YANLIŞ metin linke döner.
  it('regex meta karakterleri KAÇIRILIR', () => {
    const p = idLinkPattern([link('a.b:c-d.e12345678')])!;
    // ⚠ YALNIZ `.` değiştiriliyor. İlk yazımda `:` ve `-`yi de
    // değiştirmiştim ve test ISIRMIYORDU: ikisi regex'te zaten LİTERAL,
    // onları bozunca kaçırma olsun olmasın eşleşme kırılıyordu — yani
    // test kaçırmayı değil, başka bir şeyi ölçüyordu. Mutasyon yakaladı.
    expect('aXb:c-dYe12345678'.match(p.re)).toBeNull();
    expect('a.b:c-d.e12345678'.match(p.re)).not.toBeNull();
  });

  // ── DESEN ÜRETİLMEYEN DURUMLAR ────────────────────────────────────────
  describe('null döner', () => {
    for (const [name, links] of [
      ['undefined', undefined],
      ['boş dizi', []],
      // Rota çipleri (id taşımayan) metinde aranacak bir şey taşımıyor.
      ['id\'siz link', [{ href: '/traces', label: 'Trace\'ler' }]],
      ['boş id', [{ id: '  ', href: 'https://x', label: 'x' }]],
      ['href\'siz', [{ id: ID, href: '', label: 'x' }]],
    ] as Array<[string, IdLink[] | undefined]>) {
      it(name, () => expect(idLinkPattern(links)).toBeNull());
    }
  });

  it('id\'li ve id\'siz karışıkken yalnız id\'liler girer', () => {
    const p = idLinkPattern([
      { href: '/traces', label: 'Trace\'ler' },
      link(ID),
    ])!;
    expect(p.byId.size).toBe(1);
    expect(p.byId.has(ID)).toBe(true);
  });
});

// KABLOLAMA PİNİ — saf çekirdek yeşil ama çağrılmıyorsa satır içi link yok.
describe('kablolama', () => {
  const md = readFileSync(new URL('../Markdown.tsx', import.meta.url), 'utf8');
  const ex = readFileSync(new URL('../CopilotExplain.tsx', import.meta.url), 'utf8');

  it('Markdown deseni kuruyor ve renderInline\'a geçiriyor', () => {
    expect(md).toContain('idLinkPattern(idLinks)');
    expect(md).toContain('renderInline(');
  });

  it('CopilotExplain sunucudan gelen links\'i geçiriyor', () => {
    // v0.10.165 — kart gövdesi ExplainBody'ye taşındı: CopilotExplain links'i
    // ExplainBody'ye, o da RenderedMarkdown'a (idLinks) geçirir — zincir pinli.
    expect(ex).toContain('links={links}');
    const body = readFileSync(new URL('./ExplainBody.tsx', import.meta.url), 'utf8');
    expect(body).toContain('idLinks={links}');
  });

  it('Markdown ham HTML basmıyor — model çıktısı yolu', () => {
    // Satır içi link eklerken innerHTML'e kaymak, escape edilmiş bir
    // boru hattına ham HTML sokmak olurdu (tooltipEscapeGate sınıfı).
    expect(md).not.toContain('dangerouslySetInnerHTML');
  });
});
