import { describe, it, expect } from 'vitest';
import { renderExternalLink, attrTimeParts, formatParts, collectLinkCtx } from './externalLinks';

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
