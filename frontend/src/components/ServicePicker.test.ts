import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { shouldAutoCommit } from './ServicePicker';
import { stripTsComments } from '../styles/zLayers.test';

// v0.7.27 — Operator-reported: in service-topology "Focus on", typing the FIRST
// letter of a service immediately loaded it. Root cause was the picker's
// datalist-pick heuristic auto-firing onEnter on the first keystroke from an
// empty field. shouldAutoCommit now only treats a >1-char growth (a datalist
// pick or paste of a full option value) as a commit — incremental typing never
// grows by more than one char at a time. Regression test per CLAUDE.md #11.

describe('shouldAutoCommit', () => {
  it('does NOT commit on the first keystroke from empty (the reported bug)', () => {
    // "b" is the first char of "bsa-config-server"; even if "b" were a known
    // 1-char option, a single-char change must not auto-commit.
    expect(shouldAutoCommit('', 'b', true)).toBe(false);
  });

  it('does NOT commit while typing one char at a time through a prefix', () => {
    // orders → orders-api: each step grows by one char, never a pick.
    expect(shouldAutoCommit('orders', 'orders-', true)).toBe(false);
    expect(shouldAutoCommit('order', 'orders', true)).toBe(false);
  });

  it('commits when a datalist pick replaces the field with a full option (>1 char jump)', () => {
    expect(shouldAutoCommit('', 'payment-api', true)).toBe(true);
    expect(shouldAutoCommit('pay', 'payment-api', true)).toBe(true);
  });

  it('never commits when the value is not a known option, however it changed', () => {
    expect(shouldAutoCommit('', 'payment-api', false)).toBe(false);
    expect(shouldAutoCommit('pay', 'payment-xyz', false)).toBe(false);
  });

  it('does NOT commit on deletion / backspacing to a shorter exact match', () => {
    // User editing down to a prefix that happens to be a known option — not a pick.
    expect(shouldAutoCommit('orders-api', 'orders', true)).toBe(false);
  });

  it('requires strictly more than one char of growth', () => {
    // Exactly +1 char is typing, not a pick.
    expect(shouldAutoCommit('order', 'orders', true)).toBe(false);
    // +2 chars is a jump (e.g. an autocomplete fill / paste).
    expect(shouldAutoCommit('order', 'orders!', true)).toBe(true);
  });
});

// ─── v0.9.1024 · BAĞLANMA kapısı ──────────────────────────────────
//
// ÖLÇÜLEN DURUM: yukarıdaki testler v0.7.27'den beri YEŞİLDİ ve
// düzeltme CANLIDA HİÇ OLMADI. `shouldAutoCommit` yazıldı, export
// edildi, test edildi — ama hiçbir yerden ÇAĞRILMADI. Dört picker de
// düzeltme öncesi ifadeyi satır içinde taşımaya devam ediyordu:
//
//   Math.abs(next.length - prev.length) > 1 || (next.length > 0 && prev === '')
//
// Yani operatörün gördüğü davranış, testlerin YASAKLADIĞI iki vakayı
// hâlâ yapıyordu: (a) boş alandan ilk tuş vuruşunda commit, (b) çok
// karakterli SİLME sıçraması bilinen bir ada denk gelirse commit
// (`orders-api` → `orders` yazarken filtre kendiliğinden atlıyordu).
//
// Ders: saf fonksiyonu çıkarıp test etmek, düzeltmenin YARISIDIR.
// Bağlanmayan bir saf fonksiyon, testleri sonsuza dek yeşil tutan
// ölü koddur. Bu kapı bağlantının kendisini ölçüyor.
describe('shouldAutoCommit — picker’lara BAĞLI mı', () => {
  const PICKERS = ['ServicePicker', 'OperationPicker', 'MetricNamePicker', 'EnvPicker'];
  const read = (n: string) =>
    stripTsComments(readFileSync(join(__dirname, `${n}.tsx`), 'utf8'));

  it('dört picker de fonksiyonu ÇAĞIRIYOR', () => {
    for (const p of PICKERS) {
      expect(read(p), `${p} shouldAutoCommit çağırmıyor — düzeltme yine ölü kod`)
        .toMatch(/shouldAutoCommit\s*\(/);
    }
  });

  it('düzeltme ÖNCESİ satır içi ifade hiçbir picker’da kalmadı', () => {
    // Yorumlar soyuluyor: bu dosyanın ve picker'ların düzyazısı ifadeyi
    // AÇIKLAMAK için anıyor. Soymadan yazılmış bir tarama, kuralı
    // açıklayan yorumu ihlal sanardı (zLayers dersi, v0.9.1013).
    for (const p of PICKERS) {
      expect(read(p), `${p} eski sezgiseli hâlâ satır içinde taşıyor`)
        .not.toMatch(/prev === ''/);
      expect(read(p), `${p} eski Math.abs sıçrama ifadesini taşıyor`)
        .not.toMatch(/Math\.abs\(next\.length/);
    }
  });
});
