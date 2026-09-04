import { describe, it, expect } from 'vitest';
import { renderExternalLink, attrTimeParts, formatParts, collectLinkCtx } from './externalLinks';

// v0.10.345 — dış link şablonu (operatörün log platformu örneği: date=ddMMyyyyHHmm,
// functionId, channelCode; tarih function_id içindeki zamandan).
const TPL = 'https://logs.example/masterlog?date={{attrTime.function_id:ddMMyyyyHHmm}}&functionId={{attr.function_id}}&channelCode={{attr.channel_code}}';

describe('renderExternalLink', () => {
  const ctx = { traceId: 'abc', service: 'svc', startMs: Date.UTC(2026, 8, 4, 13, 14, 44), attrs: { function_id: '060201dfii0013680164202609041614442481', channel_code: '060201' } };
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
