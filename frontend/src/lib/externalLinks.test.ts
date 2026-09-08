import { describe, it, expect } from 'vitest';
import { renderExternalLink, attrTimeParts, formatParts, collectLinkCtx, pickGroupedLinks, zonedParts, identityKeysFromLinks, identityOverrideCtx, shortIdentity, identityRoleTR } from './externalLinks';

// v0.10.345 — dış link şablonu (operatörün log platformu örneği: date=ddMMyyyyHHmm,
// functionId, channelCode; tarih function_id içindeki zamandan).
const TPL = 'https://logs.example/masterlog?date={{attrTime.function_id:ddMMyyyyHHmm}}&functionId={{attr.function_id}}&channelCode={{attr.channel_code}}';

describe('renderExternalLink', () => {
  const ctx = { traceId: 'abc', service: 'svc', startMs: Date.UTC(2026, 8, 4, 13, 14, 44), endMs: Date.UTC(2026, 8, 4, 13, 14, 45), attrs: { function_id: '060201dfii0013680164202609041614442481', channel_code: '060201' } };
  it('function_id içindeki zaman ddMMyyyyHHmm olur, değerler URL-kodlanır', () => {
    const r = renderExternalLink(TPL, ctx);
    expect(r.missing).toEqual([]);
    expect(r.url).toBe('https://logs.example/masterlog?date=040920261614&functionId=060201dfii0013680164202609041614442481&channelCode=060201');
  });
  it('eksik attribute → url yok, eksikler listelenir', () => {
    const r = renderExternalLink(TPL, { ...ctx, attrs: { function_id: 'nodate' } });
    expect(r.url).toBeUndefined();
    expect(r.missing).toEqual(['function_id (zaman)', 'channel_code']);
  });
  it('traceId/service/time çözülür; time tarayıcı yerel saati', () => {
    const r = renderExternalLink('https://x/{{traceId}}/{{service}}?d={{time:yyyy}}', ctx);
    expect(r.url).toBe('https://x/abc/svc?d=2026');
  });
  it('özel karakter kodlanır', () => {
    const r = renderExternalLink('https://x/?q={{attr.k}}', { ...ctx, attrs: { k: 'a b&c' } });
    expect(r.url).toBe('https://x/?q=a%20b%26c');
  });
});

describe('attrTimeParts / formatParts', () => {
  it('geçerli 14 hane', () => {
    expect(attrTimeParts('xx20260904161444yy')).toEqual({ y: 2026, M: 9, d: 4, H: 16, m: 14, s: 44 });
    expect(attrTimeParts('20261304161444')).toBeNull();
    expect(attrTimeParts('abc')).toBeNull();
  });
  it('tokenlar', () => {
    expect(formatParts({ y: 2026, M: 9, d: 4, H: 16, m: 14, s: 5 }, 'dd.MM.yy HH:mm:ss')).toBe('04.09.26 16:14:05');
  });
});

describe('collectLinkCtx', () => {
  it('kök span önce; ilk dolu değer kazanır; başlangıç en erken span', () => {
    const ctx = collectLinkCtx([
      { traceId: 't', serviceName: 'child', startTime: 2e9, parentId: 'p', attributes: { channel_code: '1', function_id: 'F' } },
      { traceId: 't', serviceName: 'root', startTime: 1e9, parentId: '', attributes: { channel_code: '2' } },
    ]);
    expect(ctx?.service).toBe('root');
    expect(ctx?.attrs).toEqual({ channel_code: '2', function_id: 'F' });
    expect(ctx?.startMs).toBe(1000);
    expect(collectLinkCtx([])).toBeNull();
  });
});

// v0.10.371 — operator-reported: "trace 11:49:20 bitmiş ama logizlemeye
// date=…1148 gönderiyorsun, bazen bulamıyor." Kimlik içindeki zaman isteğin
// üretim anı; log platformu o dakikanın ±1 penceresine bakıyor ve daha geç
// biten trace'in logları dışarıda kalıyordu. {{endTime:FMT}} trace bitişini
// (en geç span sonu) verir.
describe('endTime (v0.10.371)', () => {
  const base = { traceId: 'abc', service: 'svc', attrs: {} };
  it('trace bitişi dakikayı geçince endTime o dakikayı yazar, time başlangıcı', () => {
    const start = new Date(2026, 8, 5, 11, 48, 59, 500).getTime(); // yerel saat
    const ctx = { ...base, startMs: start, endMs: start + 21_000 }; // 11:49:20.5
    expect(renderExternalLink('d={{time:HHmm}}', ctx).url).toBe('d=1148');
    expect(renderExternalLink('d={{endTime:HHmm}}', ctx).url).toBe('d=1149');
  });
  it('FMT yoksa eksik olarak söyler', () => {
    const r = renderExternalLink('d={{endTime}}', { ...base, startMs: 0, endMs: 0 });
    expect(r.url).toBeUndefined();
    expect(r.missing).toEqual(['endTime']);
  });
  it('collectLinkCtx bitişi en geç span sonundan türetir; süresiz span kendi başlangıcı', () => {
    const t0 = new Date(2026, 8, 5, 11, 49, 20).getTime() * 1e6; // ns
    const ctx = collectLinkCtx([
      { traceId: 't', serviceName: 'root', startTime: t0, durationMs: 1260 },
      { traceId: 't', serviceName: 'child', startTime: t0 + 500_000_000, durationMs: 900, parentId: 'p' },
      { traceId: 't', serviceName: 'nodur', startTime: t0 + 100_000_000, parentId: 'p' },
    ])!;
    expect(ctx.startMs).toBe(Math.round(t0 / 1e6));
    expect(ctx.endMs).toBe(Math.round((t0 + 500_000_000 + 900_000_000) / 1e6)); // çocuk daha geç bitiyor
    expect(ctx.endMs).toBeGreaterThan(ctx.startMs);
  });
});

// v0.10.566 — operatör: "trace'in loglarının gövdesinde request_id varsa link
// ONUNLA üretilsin (channelCode gönderme); yoksa bugünkü function_id yolu."
// İki şablon aynı düğmeyi iki kez çizmesin diye grup: aynı gruptan yalnız
// çözülen ilk link çizilir.
describe('{{requestId}} (v0.10.566)', () => {
  const base = { traceId: 'abc', service: 'svc', startMs: 0, endMs: 0, attrs: {} };
  it('dolu → URL-kodlu yazılır', () => {
    const r = renderExternalLink('https://logs/?requestId={{requestId}}', { ...base, requestId: 'RQ 1/2' });
    expect(r.missing).toEqual([]);
    expect(r.url).toBe('https://logs/?requestId=RQ%201%2F2');
  });
  it('boş → çözülmez, eksik olarak requestId söyler', () => {
    const r = renderExternalLink('https://logs/?requestId={{requestId}}', base);
    expect(r.url).toBeUndefined();
    expect(r.missing).toEqual(['requestId']);
  });
  it('yok (alan hiç verilmemiş) → yine eksik', () => {
    expect(renderExternalLink('{{requestId}}', base).missing).toEqual(['requestId']);
  });
});

describe('pickGroupedLinks (v0.10.566)', () => {
  type L = { label: string; urlTemplate: string; group?: string };
  // resolve: şablonu "OK" içeriyorsa çözülür — saf seçiciyi render'dan ayırır.
  const resolve = (l: L) => (l.urlTemplate.includes('OK')
    ? { url: `https://x/${l.label}`, missing: [] as string[] }
    : { url: undefined, missing: ['function_id'] });

  it('gruplanmamış davranış BUGÜNKÜYLE aynı: her link kendi başına, sırayla', () => {
    const links: L[] = [
      { label: 'a', urlTemplate: 'OK' },
      { label: 'b', urlTemplate: 'no' },
      { label: 'c', urlTemplate: 'OK', group: '  ' }, // yalnız boşluk = grupsuz
    ];
    const out = pickGroupedLinks(links, resolve);
    expect(out.map(o => o.link.label)).toEqual(['a', 'b', 'c']);
    expect(out[1].url).toBeUndefined();
    expect(out[1].missing).toEqual(['function_id']);
  });

  it('grupta ikinci link çözülüyorsa O gelir (birincil requestId boşsa yedek)', () => {
    const out = pickGroupedLinks([
      { label: 'birincil', urlTemplate: 'no', group: 'log' },
      { label: 'yedek', urlTemplate: 'OK', group: 'log' },
    ], resolve);
    expect(out).toHaveLength(1);
    expect(out[0].link.label).toBe('yedek');
    expect(out[0].url).toBe('https://x/yedek');
  });

  it('grupta ilk link çözülüyorsa yedek ÇİZİLMEZ (ayardaki sıra kazanır)', () => {
    const out = pickGroupedLinks([
      { label: 'birincil', urlTemplate: 'OK', group: 'log' },
      { label: 'yedek', urlTemplate: 'OK', group: 'log' },
    ], resolve);
    expect(out.map(o => o.link.label)).toEqual(['birincil']);
  });

  it('hiçbiri çözülmezse grubun İLK linki missing ile BİR KEZ gelir', () => {
    const out = pickGroupedLinks([
      { label: 'birincil', urlTemplate: 'no', group: 'log' },
      { label: 'yedek', urlTemplate: 'no', group: 'log' },
    ], resolve);
    expect(out).toHaveLength(1);
    expect(out[0].link.label).toBe('birincil');
    expect(out[0].url).toBeUndefined();
    expect(out[0].missing).toEqual(['function_id']);
  });

  it('grup sırası İLK GÖRÜLME sırası; grupsuzlar araya yerinde girer', () => {
    const out = pickGroupedLinks([
      { label: 'g1-a', urlTemplate: 'no', group: 'g1' },
      { label: 'tek', urlTemplate: 'OK' },
      { label: 'g2-a', urlTemplate: 'OK', group: 'g2' },
      { label: 'g1-b', urlTemplate: 'OK', group: 'g1' },
    ], resolve);
    expect(out.map(o => o.link.label)).toEqual(['g1-b', 'tek', 'g2-a']);
  });

  it('deterministik: aynı girdi aynı çıktı', () => {
    const links: L[] = [
      { label: 'p', urlTemplate: 'no', group: ' log ' },
      { label: 's', urlTemplate: 'OK', group: 'log' }, // trim'lenmiş anahtar aynı gruptur
    ];
    const a = pickGroupedLinks(links, resolve);
    const b = pickGroupedLinks(links, resolve);
    expect(a.map(o => o.link.label)).toEqual(['s']);
    expect(a).toEqual(b);
  });

  it('gerçek render ile: requestId varsa birincil, yoksa function_id yedeği', () => {
    const PRIMARY = 'https://logs/?date={{time:ddMMyyyyHHmm}}&requestId={{requestId}}';
    const FALLBACK = 'https://logs/?date={{time:ddMMyyyyHHmm}}&functionId={{attr.function_id}}&channelCode={{attr.channel_code}}';
    const links: L[] = [
      { label: 'Log (requestId)', urlTemplate: PRIMARY, group: 'log' },
      { label: 'Log (functionId)', urlTemplate: FALLBACK, group: 'log' },
    ];
    const ctx = { traceId: 't', service: 'svc', startMs: new Date(2026, 8, 6, 10, 20).getTime(), endMs: 0, attrs: { function_id: 'F', channel_code: '060201' } };
    const withReq = pickGroupedLinks(links, l => renderExternalLink(l.urlTemplate, { ...ctx, requestId: 'R-42' }));
    expect(withReq).toHaveLength(1);
    expect(withReq[0].link.label).toBe('Log (requestId)');
    expect(withReq[0].url).toBe('https://logs/?date=060920261020&requestId=R-42');
    const noReq = pickGroupedLinks(links, l => renderExternalLink(l.urlTemplate, ctx));
    expect(noReq).toHaveLength(1);
    expect(noReq[0].link.label).toBe('Log (functionId)');
    expect(noReq[0].url).toBe('https://logs/?date=060920261020&functionId=F&channelCode=060201');
  });
});

// v0.10.567 — tarih artık TARAYICI dilimi değil, kurumun dilimi.
// Sabit an: 2026-01-15T22:30:00Z. Türkiye kalıcı +03 (2016'dan beri DST yok),
// yani Istanbul'da 16 Ocak 01:30 — gün DE değişiyor, bu yüzden vaka seçildi:
// tarayıcı UTC ise eski kod "15..2230" üretip log platformunda yanlış
// pencereyi açıyordu.
describe('zonedParts / sabit saat dilimi', () => {
  const MS = Date.UTC(2026, 0, 15, 22, 30, 0);
  it('Istanbul: gün sınırını doğru geçer', () => {
    expect(zonedParts(MS, 'Europe/Istanbul')).toEqual({ y: 2026, M: 1, d: 16, H: 1, m: 30, s: 0 });
  });
  it('UTC istenirse UTC', () => {
    expect(zonedParts(MS, 'UTC')).toEqual({ y: 2026, M: 1, d: 15, H: 22, m: 30, s: 0 });
  });
  it('boş/geçersiz dilim VARSAYILANA düşer (tarayıcı yereline DEĞİL)', () => {
    const ist = zonedParts(MS, 'Europe/Istanbul');
    expect(zonedParts(MS, undefined)).toEqual(ist);
    expect(zonedParts(MS, '   ')).toEqual(ist);
    expect(zonedParts(MS, 'Mars/Olympus')).toEqual(ist);
  });
  it('gece yarısı 24 değil 00 (hourCycle h23)', () => {
    const mid = Date.UTC(2026, 0, 15, 21, 0, 0); // Istanbul 16 Ocak 00:00
    expect(zonedParts(mid, 'Europe/Istanbul')).toMatchObject({ d: 16, H: 0 });
  });
  it('{{time:FMT}} ctx.tz ile biçimlenir; ctx.tz yoksa Istanbul', () => {
    const ctx = { traceId: 't', service: 's', startMs: MS, endMs: MS, attrs: {} };
    expect(renderExternalLink('https://x/?d={{time:ddMMyyyyHHmm}}', ctx).url)
      .toBe('https://x/?d=160120260130');
    expect(renderExternalLink('https://x/?d={{time:ddMMyyyyHHmm}}', { ...ctx, tz: 'UTC' }).url)
      .toBe('https://x/?d=150120262230');
  });
});

// v0.10.568 — kimlik SEÇİM menüsü (operatör: "kullanıcıya hangi function_id'ye
// gitmek istersin diye seçenek verelim"). Menünün KARARLARI burada saf test
// edilir; Trace.tsx yalnız çizer.
describe('identityKeysFromLinks', () => {
  it('requires birleşir, sıra korunur, tekilleşir', () => {
    expect(identityKeysFromLinks([
      { requires: ['function_id', 'channel_code'] },
      { requires: ['channel_code', 'msisdn'] },
      {},
    ])).toEqual(['function_id', 'channel_code', 'msisdn']);
  });
  it('EN ÇOK 5 anahtar (sunucu taramasının maliyeti sabit kalsın)', () => {
    expect(identityKeysFromLinks([{ requires: ['a', 'b', 'c', 'd', 'e', 'f', 'g'] }]))
      .toEqual(['a', 'b', 'c', 'd', 'e']);
    // Sınır çok linke yayıldığında da geçerli — link başına değil TOPLAMDA 5.
    expect(identityKeysFromLinks([
      { requires: ['a', 'b'] }, { requires: ['c', 'd'] }, { requires: ['e', 'f'] },
    ])).toEqual(['a', 'b', 'c', 'd', 'e']);
  });
  it('boş/whitespace anahtar düşer; requires yoksa boş dilim', () => {
    expect(identityKeysFromLinks([{ requires: ['', '  ', ' k '] }])).toEqual(['k']);
    expect(identityKeysFromLinks([])).toEqual([]);
    expect(identityKeysFromLinks([{}, {}])).toEqual([]);
  });
});

describe('identityOverrideCtx', () => {
  const ctx = { traceId: 't', service: 's', startMs: 0, endMs: 0, attrs: { function_id: 'F1', channel_code: '060201' }, requestId: 'R-1' };
  it("source 'log' → requestId ezilir, attrs'a DOKUNULMAZ", () => {
    const o = identityOverrideCtx(ctx, { value: 'R-9', key: 'request_id', source: 'log' })!;
    expect(o.requestId).toBe('R-9');
    expect(o.attrs).toEqual(ctx.attrs);
    expect(ctx.requestId).toBe('R-1'); // girdi mutasyona uğramaz
  });
  it("source 'span' → yalnız O anahtar ezilir, öteki attribute'lar kalır", () => {
    const o = identityOverrideCtx(ctx, { value: 'F2', key: 'function_id', source: 'span' })!;
    expect(o.attrs).toEqual({ function_id: 'F2', channel_code: '060201' });
    expect(o.requestId).toBe('R-1');
    expect(ctx.attrs.function_id).toBe('F1');
  });
  it('ctx yoksa null (satır pasif çizilir)', () => {
    expect(identityOverrideCtx(null, { value: 'x', key: 'k', source: 'span' })).toBeNull();
    expect(identityOverrideCtx(undefined, { value: 'x', key: 'k', source: 'log' })).toBeNull();
  });
  it('AYNI şablon override ile başka kimliğe çözülür (menü satırının sözleşmesi)', () => {
    const tpl = 'https://logs/?functionId={{attr.function_id}}&channelCode={{attr.channel_code}}';
    const alt = identityOverrideCtx(ctx, { value: 'F2', key: 'function_id', source: 'span' })!;
    expect(renderExternalLink(tpl, alt).url).toBe('https://logs/?functionId=F2&channelCode=060201');
    // Çözülemeyen aday → url yok, eksikler söylenir (satır pasif).
    const bare = identityOverrideCtx({ ...ctx, attrs: {} }, { value: 'F2', key: 'function_id', source: 'span' })!;
    expect(renderExternalLink(tpl, bare).missing).toEqual(['channel_code']);
  });
});

describe('shortIdentity / identityRoleTR', () => {
  it('>18 karakter: baş 8 + … + son 6', () => {
    expect(shortIdentity('060201dfii0013680164202609041614442481')).toBe('060201df…442481');
  });
  it('≤18 karakter aynen', () => {
    expect(shortIdentity('R-42')).toBe('R-42');
    expect(shortIdentity('123456789012345678')).toBe('123456789012345678'); // tam 18
    expect(shortIdentity('1234567890123456789')).toBe('12345678…456789');   // 19 → kısalır
    expect(shortIdentity('')).toBe('');
  });
  it('rol etiketleri Türkçe', () => {
    expect(identityRoleTR('selected')).toBe('seçili span');
    expect(identityRoleTR('error')).toBe('ilk hatalı span');
    expect(identityRoleTR('root')).toBe('root span');
    expect(identityRoleTR('span')).toBe('alt span');
  });
});
