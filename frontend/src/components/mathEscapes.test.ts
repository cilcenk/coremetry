import { describe, it, expect } from 'vitest';
import { normalizeMathEscapes } from './mathEscapes';

// v0.10.12 — operatör bildirimi: "Problemi açıklarken right arrow right
// arrow yazıyor". LLM çıktısı deterministik değil, o yüzden düzeltmenin
// kendisi deterministik ve tablo-testli.

describe('normalizeMathEscapes', () => {
  it('OPERATÖRÜN GÖRDÜĞÜ satır — sarmalı ok zinciri', () => {
    // Ekrandaki gerçek şekil. Servis adları anonimleştirildi; korunan
    // şey biçim.
    const seen = 'svc-a $\\rightarrow$ svc-b $\\rightarrow$ svc-c';
    expect(normalizeMathEscapes(seen)).toBe('svc-a → svc-b → svc-c');
  });

  const cases: Array<[string, string, string]> = [
    ['çıplak komut', 'a \\rightarrow b', 'a → b'],
    ['sarmalı komut', 'a $\\rightarrow$ b', 'a → b'],
    ['boşluklu sarmal', 'a $ \\rightarrow $ b', 'a → b'],
    ['kısa ok', 'a \\to b', 'a → b'],
    ['çift ok', 'a \\Rightarrow b', 'a ⇒ b'],
    ['çarpı', '3 \\times 4', '3 × 4'],
    ['yaklaşık', 'p \\approx 0.5', 'p ≈ 0.5'],
    ['küçük eşit', 'n \\leq 5', 'n ≤ 5'],
    ['yunan harfi', '\\Delta t', 'Δ t'],
    ['çoklu değişim', 'a \\to b \\to c', 'a → b → c'],
  ];
  for (const [name, input, want] of cases) {
    it(name, () => expect(normalizeMathEscapes(input)).toBe(want));
  }

  // ── DOKUNULMAMASI gerekenler ──────────────────────────────────────────
  describe('tanınmayan girdi AYNEN kalır', () => {
    const untouched = [
      ['dolar işareti METİNDE', 'fiyat $5 ve $10'],
      ['shell değişkeni', 'echo $PATH ve $HOME'],
      ['ters bölü ama komut değil', 'C:\\tmp\\dosya'],
      ['bilinmeyen LaTeX komutu', 'a \\frac{1}{2} b'],
      ['ters bölü hiç yok — erken çıkış', 'düz bir cümle'],
      ['boş', ''],
    ];
    for (const [name, input] of untouched) {
      it(name, () => expect(normalizeMathEscapes(input)).toBe(input));
    }
  });

  // ── AYIRT EDİCİ VAKALAR ───────────────────────────────────────────────
  it('uzun ad önce eşleşir — \\leq geriye "q" bırakmaz', () => {
    // `\le` deseni önce denenirse `\leq` → "≤q" olur. Sıralama bu yüzden
    // uzunluğa göre; bu test o sıralamayı çiviliyor.
    expect(normalizeMathEscapes('n \\leq 5')).toBe('n ≤ 5');
    expect(normalizeMathEscapes('n \\geq 5')).toBe('n ≥ 5');
  });

  it('komut adının HARFLE devamı eşleşmez', () => {
    // `\tokenizer` içindeki `\to`yu çevirmek metni bozardı. Sınır
    // HARF: LaTeX komut adları yalnız harften oluşur.
    expect(normalizeMathEscapes('\\tokenizer')).toBe('\\tokenizer');
    expect(normalizeMathEscapes('\\rightarrows')).toBe('\\rightarrows');
  });

  it('RAKAMLA devam eden komut EŞLEŞİR — LaTeX semantiği', () => {
    // İlk yazımda bunu ihlal sandım; yanlıştı. LaTeX'te komut adı
    // harfte biter, yani `\times2` gerçekten "× 2" demek. Testi koda
    // uydurmak değil, kodu doğrulamak: bu davranış DOĞRU.
    expect(normalizeMathEscapes('\\times2')).toBe('×2');
  });

  it('karışık dolar içeriğinde YALNIZ komut çevrilir', () => {
    // `$5 \times 3$` — sarmal deseni tutmuyor (içerik tek komut değil),
    // o yüzden dolarlar kalıyor ama komut çevriliyor. Yarım iş, ama
    // YANLIŞ iş değil: metnin dolarları operatörün yazdığı olabilir.
    expect(normalizeMathEscapes('$5 \\times 3$')).toBe('$5 × 3$');
  });
});
