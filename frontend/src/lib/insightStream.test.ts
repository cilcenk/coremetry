import { describe, it, expect, afterEach, vi } from 'vitest';
import { api } from './api';

// v0.9.1130 (AI Faz 2.2) — insight kartının İSTEMCİ sözleşmesi.
//
// Uç, explain ailesinden bir yerde AYRILIYOR ve testin varlık sebebi tam
// olarak o ayrım: `signals` çerçevesi prose'tan ÖNCE düşer ve AI kapalı
// bir kurulumda `answer` çerçevesi HİÇ GELMEZ. explainStream o hâli
// "cevapsız kapandı" diye hata sayıyor; burada aynı davranış kartın
// yarısını (sinyaller + pivotlar) çöpe atmak olurdu.
//
// Gerçek çağrı yolundan (fetch mock'lu) test ediliyor, iç yardımcıdan
// değil: sözleşme "kanca verirsen akar, vermezsen tek gövde alırsın".

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

const SIGNALS_FRAME = 'event: signals\ndata: '
  + JSON.stringify({
      prose: '',
      signals: [{ kind: 'deploy', label: 'Yakın deploy', value: 'v2.4.1', severity: 'warn' }],
      links: [{ label: 'Loglar (error+)', href: '/logs?service=payment' }],
      exchangeId: 'x1',
      model: 'gemma4',
    })
  + '\n\n';

afterEach(() => {
  calls.length = 0;
  vi.unstubAllGlobals();
});

describe('insight — akan kip', () => {
  it('sinyaller İLK çerçevede bildirilir, prose SONRA akar', async () => {
    mockFetch(() => sseResponse(
      SIGNALS_FRAME
      + 'event: delta\ndata: {"text":"Havuz "}\n\n'
      + 'event: delta\ndata: {"text":"doygunluğu."}\n\n'
      + 'event: answer\ndata: {"text":"Havuz doygunluğu.","exchangeId":"x1"}\n\n'
      + 'event: done\ndata: {"ok":true}\n\n',
    ));

    // Sıra ÖLÇÜLÜYOR: tek bir olay dizisine hem sinyal hem token yazılıyor,
    // yani "sinyaller prose'tan sonra geldi" bir sıralama assert'i olarak
    // kırmızı yanar — iki ayrı sayaç bunu göremezdi.
    const order: string[] = [];
    const r = await api.insight('exception', 'a3f19c', {
      onSignals: s => order.push(`signals:${s.signals.length}`),
      onDelta: d => order.push(`delta:${d}`),
    });

    expect(order).toEqual(['signals:1', 'delta:Havuz ', 'delta:doygunluğu.']);
    expect(r.prose).toBe('Havuz doygunluğu.');
    expect(r.exchangeId).toBe('x1');
    // Deterministik yarı, answer çerçevesi prose'u getirdikten SONRA da
    // cevapta duruyor (answer çerçevesi yalnız metin taşır).
    expect(r.signals).toHaveLength(1);
    expect(r.links[0].href).toBe('/logs?service=payment');
    expect(r.model).toBe('gemma4');
    expect(calls[0].url).toContain('stream=1');
    expect(calls[0].url).toContain('/api/insight/exception/a3f19c');
  });

  it('answer çerçevesi biriken delta\'lardan FARKLI olabilir — kazanan answer', async () => {
    mockFetch(() => sseResponse(
      SIGNALS_FRAME
      + 'event: delta\ndata: {"text":"yarım"}\n\n'
      + 'event: answer\ndata: {"text":"tam ve nihai anlatı","exchangeId":"x1"}\n\n'
      + 'event: done\ndata: {"ok":true}\n\n',
    ));
    const r = await api.insight('problem', 'p1', { onDelta: () => {} });
    expect(r.prose).toBe('tam ve nihai anlatı');
  });

  it('answer kimliksizse signals çerçevesindeki kimlik korunur', async () => {
    mockFetch(() => sseResponse(
      SIGNALS_FRAME
      + 'event: answer\ndata: {"text":"anlatı"}\n\n'
      + 'event: done\ndata: {"ok":true}\n\n',
    ));
    const r = await api.insight('exception', 'fp', { onDelta: () => {} });
    expect(r.exchangeId).toBe('x1');
  });
});

describe('insight — AI kapalı', () => {
  it('answer çerçevesi HİÇ gelmeden çözülür (signals → done)', async () => {
    // Sunucu LLM'e hiç gitmez: 503 değil, 200 + aiOff bayrağı. Kart bu
    // hâlde de TAM: sinyaller ve pivotlar geçerli.
    mockFetch(() => sseResponse(
      'event: signals\ndata: '
      + JSON.stringify({
          prose: '', aiOff: true,
          signals: [{ kind: 'exception', label: 'Oluşum', value: '1.240', severity: 'err' }],
          links: [{ label: 'Servis', href: '/service?name=payment' }],
        })
      + '\n\nevent: done\ndata: {"ok":true}\n\n',
    ));
    const seen: string[] = [];
    const r = await api.insight('exception', 'fp', {
      onSignals: () => seen.push('signals'), onDelta: d => seen.push(d),
    });

    expect(r.aiOff).toBe(true);
    expect(r.prose).toBe('');
    // Oylanacak bir model cevabı YOK → kimlik de yok (feedback atomu
    // kendini gizler).
    expect(r.exchangeId).toBeUndefined();
    expect(r.signals).toHaveLength(1);
    expect(seen).toEqual(['signals']);
  });

  it('AI AÇIKken cevapsız kapanan akış YİNE hata (yarım anlatı tam sanılmaz)', async () => {
    mockFetch(() => sseResponse(SIGNALS_FRAME + 'event: delta\ndata: {"text":"yarım"}\n\n'));
    await expect(api.insight('problem', 'p1', { onDelta: () => {} }))
      .rejects.toThrow('cevapsız kapandı');
  });
});

describe('insight — sessiz geri düşüşler', () => {
  it('sunucu DÜZ JSON dönerse buffered kabul edilir', async () => {
    mockFetch(() => jsonResponse({
      prose: 'buffered anlatı', exchangeId: 'x7',
      signals: [{ kind: 'generic', label: 'Servis', value: 'payment' }],
      links: [],
    }));
    const seen: string[] = [];
    const r = await api.insight('problem', 'p1', {
      onSignals: () => seen.push('signals'), onDelta: d => seen.push(d),
    });

    expect(r.prose).toBe('buffered anlatı');
    expect(r.signals).toHaveLength(1);
    // Kanca ÇAĞRILMAZ: her şey aynı anda geldi, iki kez bildirmek
    // "sinyaller önce geldi" yalanını söylemek olurdu.
    expect(seen).toEqual([]);
  });

  it('null diziler boş diziye normalize edilir (.map çökmesi sınıfı)', async () => {
    mockFetch(() => jsonResponse({ prose: 'x', signals: null, links: null }));
    const r = await api.insight('problem', 'p1', { onDelta: () => {} });
    expect(r.signals).toEqual([]);
    expect(r.links).toEqual([]);
  });

  it('kanca YOKSA akan yola hiç girilmez', async () => {
    mockFetch(() => jsonResponse({ prose: 'tek gövde', signals: [], links: [] }));
    const r = await api.insight('exception', 'fp');
    expect(r.prose).toBe('tek gövde');
    expect(calls[0].url).not.toContain('stream=1');
  });

  it('kancasız yol da normalize eder (kipe bağlı çökme yok)', async () => {
    mockFetch(() => jsonResponse({ prose: 'x' }));
    const r = await api.insight('exception', 'fp');
    expect(r.signals).toEqual([]);
    expect(r.links).toEqual([]);
    expect(r.aiOff).toBe(false);
  });

  it('id kaçırılır — eğik çizgi taşıyan kimlik rotayı bölmez', async () => {
    mockFetch(() => jsonResponse({ prose: '', signals: [], links: [] }));
    await api.insight('exception', 'a/b c');
    expect(calls[0].url).toContain('/api/insight/exception/a%2Fb%20c');
  });
});

describe('insight — hata ve iptal', () => {
  it('error çerçevesi Error olarak fırlar', async () => {
    mockFetch(() => sseResponse(
      SIGNALS_FRAME
      + 'event: error\ndata: {"error":"model kotası doldu"}\n\n'
      + 'event: done\ndata: {"ok":false}\n\n',
    ));
    await expect(api.insight('problem', 'p1', { onDelta: () => {} }))
      .rejects.toThrow('model kotası doldu');
  });

  it('HTTP hatası akan kipte de Error (SSE içine gizlenmez)', async () => {
    mockFetch(() => jsonResponse({ error: 'not found' }, 404));
    await expect(api.insight('exception', 'yok', { onDelta: () => {} })).rejects.toThrow(/404/);
  });

  it('iptal edilmiş signal isteği başlatmaz', async () => {
    mockFetch(() => sseResponse(SIGNALS_FRAME));
    const ac = new AbortController();
    ac.abort();
    await expect(api.insight('problem', 'p1', { onDelta: () => {}, signal: ac.signal }))
      .rejects.toThrow();
  });

  it('signal fetch\'e GEÇER — "Yeniden üret" eskisini kesebilsin', async () => {
    mockFetch(() => sseResponse(
      SIGNALS_FRAME + 'event: answer\ndata: {"text":"x"}\n\nevent: done\ndata: {"ok":true}\n\n',
    ));
    const ac = new AbortController();
    await api.insight('problem', 'p1', { onDelta: () => {}, signal: ac.signal });
    expect(calls[0].init.signal).toBe(ac.signal);
  });
});
