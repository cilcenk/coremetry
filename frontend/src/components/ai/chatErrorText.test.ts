import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { chatErrorText } from './chatErrorText';

// v0.10.22 — Copilot denetimi: sohbet hatası HAM basılıyordu. Operatör
// balonda şunu görüyordu:
//
//   ⚠ openai-compat call: Post "http://<host>:8000/v1/chat/completions":
//     dial tcp 10.x.x.x:8000: connect: connection refused
//
// Aynı depoda ona ne yapacağını söyleyen aiErrorHint v0.9.200'den beri
// duruyordu — AIAnalysisPanel kullanıyor, sohbet kullanmıyordu.

describe('chatErrorText', () => {
  it('OPERATÖRÜN GÖRDÜĞÜ hata — eyleme dönük cümleye çevrilir', () => {
    const raw = 'openai-compat call: Post "http://ai-gw.internal:8000/v1/chat/completions": '
      + 'dial tcp 10.4.2.9:8000: connect: connection refused';
    const v = chatErrorText(raw);
    expect(v.text).toContain('AI uç noktasına ulaşılamıyor');
    expect(v.text).toContain('Settings');
    // Ham metin SİLİNMİYOR — tooltip'te duruyor. Teşhis için hangi
    // adrese ulaşılamadığı gerekli; operatörün duruşu tam-sadakat.
    expect(v.raw).toBe(raw);
  });

  it('kota/429 — kendi sınıfı', () => {
    const v = chatErrorText('openai-compat 429: {"error":{"code":429,"message":"quota"}}');
    expect(v.text).toMatch(/kota/i);
    expect(v.raw).not.toBeNull();
  });

  it('zaman aşımı — kendi sınıfı', () => {
    const v = chatErrorText('context deadline exceeded');
    expect(v.text).toMatch(/zaman aşımına/i);
  });

  it('yapılandırılmamış — arıza DEĞİL, eksik kurulum', () => {
    // Bu ayrım önemli: "AI patladı" ile "AI hiç kurulmamış" farklı
    // eylemler gerektiriyor.
    const v = chatErrorText('AI copilot not configured');
    expect(v.text).toContain('yapılandırılmamış');
    expect(v.text).toContain('Settings');
  });

  // ⚠ EN ÖNEMLİ DAL. Bilinmeyen bir sınıfta ipucu UYDURULMAMALI.
  //
  // Yarım bir tahmin ("herhalde ağ sorunudur") ham metinden kötüdür:
  // operatörü yanlış yöne gönderir ve ham metni de gizler.
  describe('bilinmeyen sınıf — ham metin AYNEN', () => {
    for (const raw of [
      'openai-compat 500: internal server error',
      'unexpected EOF while parsing tool call',   // "EOF" ağ sınıfına düşer, bkz. not
      'model produced invalid JSON',
      'something entirely new',
    ]) {
      it(raw.slice(0, 34), () => {
        const v = chatErrorText(raw);
        // Ya ham metnin kendisi gösterilir (raw=null, tekrar yok),
        // ya da tanınan bir sınıfa düşer ve ham metin tooltip'te kalır.
        if (v.raw === null) {
          expect(v.text).toBe(raw);
        } else {
          expect(v.raw).toBe(raw);
        }
      });
    }
  });

  it('boş hata sessiz kalmaz', () => {
    // Boş bir hata balonu, hata olmadığı izlenimi verir.
    expect(chatErrorText('').text).toContain('bilinmeyen');
    expect(chatErrorText('   ').text).toContain('bilinmeyen');
  });

  it('ham metin gösterildiğinde tooltip TEKRARLAMAZ', () => {
    const v = chatErrorText('bilinmeyen bir şey');
    expect(v.raw).toBeNull();
  });
});

// KABLOLAMA PİNİ — saf çekirdek yeşil ama çağrılmıyorsa kusur yerinde
// kalır (v0.9.1334, v0.10.11 sınıfı).
describe('ChatBubble kablolaması', () => {
  const src = readFileSync(new URL('./ChatBubble.tsx', import.meta.url), 'utf8');

  it('hata dalı chatErrorText kullanıyor', () => {
    expect(src).toContain('chatErrorText(turn.error)');
  });

  it('ham metni tooltip olarak taşıyor', () => {
    expect(src).toContain('title={ev.raw ?? undefined}');
  });

  it('ham turn.error artık DOĞRUDAN basılmıyor', () => {
    // Eski satır: `⚠ {turn.error}` — geri gelirse ipucu tamamen atlanır.
    expect(src).not.toContain('⚠ {turn.error}');
  });
});
