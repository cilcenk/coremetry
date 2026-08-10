import { describe, it, expect } from 'vitest';
import { comboFromEvent } from './keyboard';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

// v0.8.x — comboFromEvent read e.key.length unguarded, throwing
// "Cannot read properties of undefined (reading 'length')" on synthetic /
// programmatic KeyboardEvents (password managers, IME composition, autofill
// dispatch) that fire keydown with no `key`. These pin the guard.
const ev = (o: Partial<KeyboardEvent>): KeyboardEvent => o as KeyboardEvent;

describe('comboFromEvent', () => {
  it('does not throw when e.key is undefined', () => {
    expect(() => comboFromEvent(ev({}))).not.toThrow();
    expect(() => comboFromEvent(ev({ shiftKey: true }))).not.toThrow();
    expect(() => comboFromEvent(ev({ metaKey: true, altKey: true }))).not.toThrow();
  });

  it('builds the expected combo for real keys', () => {
    expect(comboFromEvent(ev({ key: 'k' }))).toBe('k');
    expect(comboFromEvent(ev({ key: 'K' }))).toBe('k'); // printable → lowercased
    expect(comboFromEvent(ev({ key: 'Enter' }))).toBe('Enter'); // special key kept verbatim
    expect(comboFromEvent(ev({ metaKey: true, key: 'k' }))).toBe('mod+k');
    expect(comboFromEvent(ev({ shiftKey: true, key: 'ArrowDown' }))).toBe('shift+ArrowDown');
  });
});

// v0.9.949 — UX denetimi E1 / Ö27.
//
// ORİJİNAL BELİRTİ: `G` (son satıra atla) TÜM uygulamada ölüydü ve
// Shift+G yanlışlıkla `g`-prefix dizisini başlatıyordu — Shift+G'den
// sonra 1.2 sn içinde `s` basan kendini /services'te buluyordu. Yardım
// modalı ise "Jump to last row (G)" diyordu.
//
// Kök neden: `if (e.shiftKey && key.length > 1)` — tek karakterli HER
// tuşta shift yutuluyordu, yani Shift+G → 'g'.
//
// BU TESTİN ESKİ HÂLİ YANLIŞ DAVRANIŞI BİLEREK PİNLİYORDU
// ('shift+A' → 'a'). Pin'in YÖNÜ döndü; eskisini korumak, kapanmış bir
// kusuru sonsuza kadar zorunlu kılmak olurdu.
describe('comboFromEvent — Shift katlanması yalnız A-Z (v0.9.949)', () => {
  it('Shift+harf → shift+<küçük harf>', () => {
    expect(comboFromEvent(ev({ shiftKey: true, key: 'G' }))).toBe('shift+g');
    expect(comboFromEvent(ev({ shiftKey: true, key: 'A' }))).toBe('shift+a');
  });

  it('Shift+G artık `g` ÜRETMEZ — dizi öneki başlatamaz', () => {
    // Ö27'nin ikinci yarısı: 'g' iki-tuşlu dizilerin öneki. Shift+G 'g'
    // üretirse operatör farkında olmadan `g s` sekansına girer.
    expect(comboFromEvent(ev({ shiftKey: true, key: 'G' }))).not.toBe('g');
  });

  it('NOKTALAMA katlanmaz — `?` kısayolu YAŞAR', () => {
    // Naif düzeltme (`if (e.shiftKey)`) tam olarak burayı kırardı:
    // Shift+/ zaten '?' üretir, shift bilgisi tuşun KENDİSİNDEDİR ve
    // ikinci kez kodlanamaz. 'shift+?' hiçbir kayda uymaz → yardım
    // modalı sessizce ölür.
    expect(comboFromEvent(ev({ shiftKey: true, key: '?' }))).toBe('?');
    expect(comboFromEvent(ev({ shiftKey: true, key: ':' }))).toBe(':');
    expect(comboFromEvent(ev({ shiftKey: true, key: '_' }))).toBe('_');
  });

  it('shift’siz harf değişmez — j/k/o gezinmesi bozulmaz', () => {
    expect(comboFromEvent(ev({ key: 'j' }))).toBe('j');
    expect(comboFromEvent(ev({ key: 'g' }))).toBe('g');
  });

  it('özel tuşlar aynen katlanır (eski davranış)', () => {
    expect(comboFromEvent(ev({ shiftKey: true, key: 'Tab' }))).toBe('shift+Tab');
  });

  it('mod+shift birleşimi sıralı', () => {
    expect(comboFromEvent(ev({ metaKey: true, shiftKey: true, key: 'P' }))).toBe('mod+shift+p');
  });
});

// useTableNav ÖLÜ kayıt taşımaz: çalışmayan bir kısayolu yardım
// ekranında listelemek operatöre yalan söylemektir.
describe('useTableNav — ulaşılamaz kayıt yok (v0.9.949)', () => {
  it("'G' kaydı kaldırıldı; üretilen combo 'shift+g'", () => {
    const src = readFileSync(join(__dirname, 'useTableNav.ts'), 'utf8');
    expect(src).not.toMatch(/keys: 'G'/);
    expect(src).toMatch(/keys: 'shift\+g'/);
  });
});
