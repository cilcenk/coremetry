import { describe, it, expect } from 'vitest';
import { readSSE, type AIStreamFrame } from './sse';

// v0.9.1127 (AI Faz 1.5) — SSE çerçeve okuyucusunun sözleşmesi.
//
// NEDEN TEST: bu ayrıştırıcı v0.6.53'ten beri api.ts'in içinde gömülü
// yaşadı ve HİÇ testi olmadı. Faz 1.5 onu ikinci bir yüzeye (akan ✨
// Explain) açtı — artık iki tüketicisi olan bir kod, ve bozulduğunda
// her ikisi de SESSİZCE boş panel çizer (istisna yok, hata yok).
//
// Kritik hâl: çerçeve sınırının ağ paketiyle HİÇBİR ilgisi yok. Bir
// `read()` bir çerçevenin yarısını, ya da üç çerçeveyi birden taşıyabilir.

/** streamOf — verilen parçaları ayrı ayrı yayan bir gövde. */
function streamOf(...chunks: string[]): ReadableStream<Uint8Array> {
  const enc = new TextEncoder();
  return new ReadableStream({
    start(c) {
      for (const ch of chunks) c.enqueue(enc.encode(ch));
      c.close();
    },
  });
}

async function collect(...chunks: string[]): Promise<AIStreamFrame[]> {
  const out: AIStreamFrame[] = [];
  await readSSE(streamOf(...chunks), f => out.push(f));
  return out;
}

describe('readSSE — çerçeve ayrıştırma', () => {
  it('event + data çiftini çözer', async () => {
    const frames = await collect('event: delta\ndata: {"text":"merhaba"}\n\n');
    expect(frames).toEqual([{ kind: 'delta', text: 'merhaba' }]);
  });

  it('tek okumada gelen ÜÇ çerçeveyi ayırır', async () => {
    const frames = await collect(
      'event: delta\ndata: {"text":"a"}\n\n' +
      'event: delta\ndata: {"text":"b"}\n\n' +
      'event: done\ndata: {"ok":true}\n\n',
    );
    expect(frames.map(f => f.kind)).toEqual(['delta', 'delta', 'done']);
    expect(frames[1].text).toBe('b');
  });

  it('PARÇA SINIRI çerçevenin ortasından geçse de birleştirir', async () => {
    // Gerçek hayattaki hâl: TCP parçası `\n\n`'e denk gelmez.
    const frames = await collect('event: ans', 'wer\ndata: {"te', 'xt":"tam"}\n', '\n');
    expect(frames).toEqual([{ kind: 'answer', text: 'tam' }]);
  });

  it('bozuk JSON taşıyan çerçeveyi ATLAR, akışı kesmez', async () => {
    const frames = await collect(
      'event: delta\ndata: {bozuk\n\n' +
      'event: delta\ndata: {"text":"sağlam"}\n\n',
    );
    expect(frames).toEqual([{ kind: 'delta', text: 'sağlam' }]);
  });

  it('event satırı yoksa kind = message', async () => {
    const frames = await collect('data: {"x":1}\n\n');
    expect(frames[0].kind).toBe('message');
  });

  it('data satırı olmayan çerçeve (yorum/keep-alive) yok sayılır', async () => {
    const frames = await collect(': keep-alive\n\nevent: done\ndata: {"ok":true}\n\n');
    expect(frames.map(f => f.kind)).toEqual(['done']);
  });

  it('answer çerçevesinin EKSTRA alanları korunur', async () => {
    // evidenceSpanIds / similarCount / code buradan geçiyor; düşerlerse
    // waterfall kutulaması ve "N geçmiş çözüm" satırı sessizce kaybolur.
    const frames = await collect(
      'event: answer\ndata: {"text":"t","exchangeId":"x1","evidenceSpanIds":["s1"],"similarCount":3}\n\n',
    );
    expect(frames[0]).toEqual({
      kind: 'answer', text: 't', exchangeId: 'x1', evidenceSpanIds: ['s1'], similarCount: 3,
    });
  });
});
