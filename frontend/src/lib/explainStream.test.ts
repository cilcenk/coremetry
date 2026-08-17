import { describe, it, expect, afterEach, vi } from 'vitest';
import { api } from './api';

// v0.9.1127 (AI Faz 1.5) — akan ✨ Explain'in İSTEMCİ sözleşmesi.
//
// Bu dosya `api.copilotExplain*` uçlarını GERÇEK çağrı yolundan test
// ediyor (fetch mock'lu), iç yardımcıyı değil: sözleşmenin kendisi
// "onDelta verirsen akar, vermezsen bugünkü gövdeyi alırsın".
//
// Üç geri düşüş yolu da burada pinli, çünkü üçü de SESSİZ olmak zorunda:
// operatör cevabını alır, taşımanın hangi yoldan geldiğini bilmez.
// Sessiz bir yol test edilmezse bozulduğunda da sessiz kalır.

interface FetchCall { url: string; init: RequestInit }
const calls: FetchCall[] = [];

function mockFetch(res: () => Response) {
  vi.stubGlobal('fetch', (url: string, init: RequestInit = {}) => {
    calls.push({ url, init });
    if (init.signal?.aborted) return Promise.reject(new DOMException('aborted', 'AbortError'));
    return Promise.resolve(res());
  });
}

function sseResponse(body: string): Response {
  const enc = new TextEncoder();
  return new Response(
    new ReadableStream({ start(c) { c.enqueue(enc.encode(body)); c.close(); } }),
    { status: 200, headers: { 'Content-Type': 'text/event-stream' } },
  );
}

function jsonResponse(obj: unknown, status = 200): Response {
  return new Response(JSON.stringify(obj), {
    status, headers: { 'Content-Type': 'application/json' },
  });
}

afterEach(() => {
  calls.length = 0;
  vi.unstubAllGlobals();
});

describe('akan ✨ Explain — SSE yolu', () => {
  it('delta çerçeveleri SIRAYLA akar, cevap answer çerçevesinden gelir', async () => {
    mockFetch(() => sseResponse(
      'event: delta\ndata: {"text":"Kök "}\n\n' +
      'event: delta\ndata: {"text":"neden: "}\n\n' +
      'event: delta\ndata: {"text":"redis."}\n\n' +
      'event: answer\ndata: {"text":"Kök neden: redis.","exchangeId":"x9"}\n\n' +
      'event: done\ndata: {"ok":true}\n\n',
    ));
    const seen: string[] = [];
    const r = await api.copilotExplainProblem('p1', { onDelta: d => seen.push(d) });

    expect(seen).toEqual(['Kök ', 'neden: ', 'redis.']);
    expect(r.explanation).toBe('Kök neden: redis.');
    expect(r.exchangeId).toBe('x9');
    expect(calls[0].url).toContain('stream=1');
  });

  it('answer çerçevesi delta toplamından FARKLI olabilir — kazanan answer', async () => {
    // Model reasoning üretip cevabı sonda toparladığında sunucu düşünce
    // bloğunu akıtmaz, kurtarılmış cevabı tek parça yollar.
    mockFetch(() => sseResponse(
      'event: delta\ndata: {"text":"yarım"}\n\n' +
      'event: answer\ndata: {"text":"tam ve nihai cevap"}\n\n' +
      'event: done\ndata: {"ok":true}\n\n',
    ));
    const r = await api.copilotExplainProblem('p1', { onDelta: () => {} });
    expect(r.explanation).toBe('tam ve nihai cevap');
  });

  it('handler ekstra alanları answer çerçevesinden çözülür', async () => {
    mockFetch(() => sseResponse(
      'event: answer\ndata: {"text":"t","exchangeId":"x1","evidenceSpanIds":["s1","s2"]}\n\n' +
      'event: done\ndata: {"ok":true}\n\n',
    ));
    const r = await api.copilotExplainTrace('t1', false, { onDelta: () => {} });
    expect(r.evidenceSpanIds).toEqual(['s1', 's2']);
    expect(r.exchangeId).toBe('x1');
  });

  it('runbook similarCount akan kipte de gelir', async () => {
    mockFetch(() => sseResponse(
      'event: answer\ndata: {"text":"adımlar","exchangeId":"x2","similarCount":4}\n\n' +
      'event: done\ndata: {"ok":true}\n\n',
    ));
    const r = await api.copilotRunbook('p1', { onDelta: () => {} });
    expect(r.similarCount).toBe(4);
  });

  it('sorgu taşıyan path stream=1 bayrağını & ile ekler', async () => {
    mockFetch(() => sseResponse('event: answer\ndata: {"text":"x"}\n\nevent: done\ndata: {"ok":true}\n\n'));
    await api.copilotExplainSpan('trace1', 'span1', { onDelta: () => {} });
    expect(calls[0].url).toContain('?span=span1&stream=1');
  });
});

describe('akan ✨ Explain — sessiz geri düşüşler', () => {
  it('sunucu DÜZ JSON dönerse buffered kabul edilir (StreamText şeffaf geri düşüşü)', async () => {
    // `?stream=1` bir TALEP, garanti değil: akıyamayan uçta sunucu
    // buffered çağrıya düşer ve tam gövdeyi yollar.
    mockFetch(() => jsonResponse({ explanation: 'buffered cevap', exchangeId: 'x3' }));
    const seen: string[] = [];
    const r = await api.copilotExplainProblem('p1', { onDelta: d => seen.push(d) });

    expect(r.explanation).toBe('buffered cevap');
    expect(r.exchangeId).toBe('x3');
    expect(seen).toEqual([]); // sıfır delta — panel cevabı tek seferde çizer
  });

  it('SSE ama SIFIR delta: answer çerçevesi tek başına yeterli', async () => {
    mockFetch(() => sseResponse(
      'event: answer\ndata: {"text":"tek parça","exchangeId":"x4"}\n\n' +
      'event: done\ndata: {"ok":false}\n\n',
    ));
    const seen: string[] = [];
    const r = await api.copilotExplainProblem('p1', { onDelta: d => seen.push(d) });
    expect(r.explanation).toBe('tek parça');
    expect(seen).toEqual([]);
  });

  it('onDelta VERİLMEZSE akan yola hiç girilmez (bayt bayt eski davranış)', async () => {
    mockFetch(() => jsonResponse({ explanation: 'eski yol', exchangeId: 'x5' }));
    const r = await api.copilotExplainProblem('p1');
    expect(r.explanation).toBe('eski yol');
    expect(calls[0].url).not.toContain('stream=1');
  });
});

describe('akan ✨ Explain — hata ve iptal', () => {
  it('error çerçevesi Error olarak fırlar', async () => {
    mockFetch(() => sseResponse(
      'event: delta\ndata: {"text":"yarı"}\n\n' +
      'event: error\ndata: {"error":"model kotası doldu"}\n\n' +
      'event: done\ndata: {"ok":false}\n\n',
    ));
    await expect(api.copilotExplainProblem('p1', { onDelta: () => {} }))
      .rejects.toThrow('model kotası doldu');
  });

  it('cevapsız kapanan akış sessizce ÇÖZÜLMEZ', async () => {
    // Boş panel, hata mesajı olmadan: v0.9.1127 öncesi bu sınıfın en
    // sinsi hâli. Akış cevapsız kapanırsa çağıran bunu bilmeli.
    mockFetch(() => sseResponse('event: delta\ndata: {"text":"yarı"}\n\n'));
    await expect(api.copilotExplainProblem('p1', { onDelta: () => {} })).rejects.toThrow();
  });

  it('HTTP hatası akan kipte de Error (SSE içine gizlenmez)', async () => {
    mockFetch(() => jsonResponse({ error: 'yapılandırılmamış' }, 503));
    await expect(api.copilotExplainProblem('p1', { onDelta: () => {} })).rejects.toThrow(/503/);
  });

  it('iptal edilen signal isteği başlatmaz', async () => {
    mockFetch(() => sseResponse('event: answer\ndata: {"text":"x"}\n\n'));
    const ac = new AbortController();
    ac.abort();
    await expect(api.copilotExplainProblem('p1', { onDelta: () => {}, signal: ac.signal }))
      .rejects.toThrow();
  });

  it('signal fetch\'e GEÇER — aksi halde "Yeniden sor" eskisini kesemez', async () => {
    mockFetch(() => sseResponse('event: answer\ndata: {"text":"x"}\n\nevent: done\ndata: {"ok":true}\n\n'));
    const ac = new AbortController();
    await api.copilotExplainProblem('p1', { onDelta: () => {}, signal: ac.signal });
    expect(calls[0].init.signal).toBe(ac.signal);
  });
});
