import { describe, it, expect } from 'vitest';
import { suggestAttrKey } from './attrKeySuggest';

// v0.9.637 — Aggregated sekmesinde yanlış yazılmış anahtar SESSİZCE
// boş tablo veriyordu: "bu attribute yok" ile "yazımı yanlış" ayırt
// edilemiyordu. Sorgu harf duyarlı KALIYOR (bilinçli); değişen tek şey
// boş sonucun AÇIKLANMASI.

const KNOWN = [
  'channel_code', 'function_code', 'user.id', 'http.route',
  'messaging.system', 'thread.name', 'db.statement',
];

describe('suggestAttrKey', () => {
  // BU OTURUMUN TÜM HATA SINIFI: prod küçük harf yazıyor, operatör
  // BÜYÜK harf yazıyor, tablo boş.
  it('harf-düzeni ıskasını yakalar ve öyle etiketler', () => {
    expect(suggestAttrKey('CHANNEL_CODE', KNOWN)).toEqual({ key: 'channel_code', reason: 'case' });
    expect(suggestAttrKey('Function_Code', KNOWN)).toEqual({ key: 'function_code', reason: 'case' });
  });

  it('yazım hatasını yakalar', () => {
    expect(suggestAttrKey('channel_cod', KNOWN)).toEqual({ key: 'channel_code', reason: 'similar' });
    expect(suggestAttrKey('http.rout', KNOWN)).toEqual({ key: 'http.route', reason: 'similar' });
  });

  // Anahtar GERÇEKTEN varsa boşluk yazımdan değil — pencerede o
  // attribute'u taşıyan trace yok demektir. Öneri göstermek yanıltırdı.
  it('anahtar mevcutsa öneri YOK', () => {
    expect(suggestAttrKey('channel_code', KNOWN)).toBeNull();
  });

  // Uydurma öneri, öneri yokluğundan kötüdür.
  it('alakasız girdide öneri YOK', () => {
    expect(suggestAttrKey('tenant_id', KNOWN)).toBeNull();
    expect(suggestAttrKey('zzzzzzzzzz', KNOWN)).toBeNull();
  });

  // Sabit bir eşik kısa anahtarlarda saçmalardı: "id" → "user.id"
  // önerisi operatörü yanlış yola sokar.
  it('kısa girdilerde cömert davranmaz', () => {
    expect(suggestAttrKey('id', KNOWN)).toBeNull();
    expect(suggestAttrKey('db', KNOWN)).toBeNull();
  });

  it('boş girdi / boş liste güvenli', () => {
    expect(suggestAttrKey('', KNOWN)).toBeNull();
    expect(suggestAttrKey('   ', KNOWN)).toBeNull();
    expect(suggestAttrKey('channel_code', [])).toBeNull();
  });

  // Harf ıskası, yazım yakınlığından ÖNCE gelmeli: en olası kast ve en
  // net açıklama o.
  it('harf ıskası yazım yakınlığını yener', () => {
    const known = ['channel_code', 'channel_codes'];
    expect(suggestAttrKey('CHANNEL_CODE', known)).toEqual({ key: 'channel_code', reason: 'case' });
  });

  it('baştaki/sondaki boşluğu yok sayar', () => {
    expect(suggestAttrKey('  CHANNEL_CODE  ', KNOWN)).toEqual({ key: 'channel_code', reason: 'case' });
  });
});
