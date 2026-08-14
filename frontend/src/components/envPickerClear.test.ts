// v0.9.1024 — EnvPicker'ın ✕ (temizle) yolu.
//
// NE DEĞİŞTİ: picker native <datalist>'ten ev Combobox'ına geçti.
// Eskiden ✕ düğmesi EnvPicker'ın kendisindeydi ve doğrudan
// `commit('')` çağırıyordu — yani "tüm ortamlar"a dönüş ANINDA
// oluyordu. Combobox kendi ✕'ini çizdiği için picker artık düğmeyi
// görmüyor; yalnız `onChange('')` görüyor. Sinyal dolaylılaştı.
//
// NEDEN KAPI: bu, taşımanın sessizce bozabileceği tek OPERATÖR
// davranışıydı. ✕'in commit etmemesi hiçbir testi kızdırmaz, tsc'yi
// ilgilendirmez ve ekranda da "çalışmış" gibi görünür (kutu boşalır)
// — ama global env filtresi ÜZERİNDE KALIR. Operatör `env=uat`
// filtresini bıraktığını sanıp tüm ortamlara baktığını zanneder;
// v0.9.864'ün düzelttiği "sessizce yanlış" sınıfının aynısı.
//
// Karşı taraf da önemli: tek tek geri silerek boşaltmak commit
// ETMEMELİ, yoksa `uat` → `ua` → `u` → `` yazan operatör (yeni bir
// env yazmak üzere) her sayfayı bir kez boşuna yeniden yükletir.
import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { isClearJump } from './EnvPicker';
import { stripTsComments } from '../styles/zLayers.test';

describe('isClearJump — ✕ mi, silme mi', () => {
  const cases: Array<[string, string, boolean, string]> = [
    // prev, next, beklenen, gerekçe
    ['uat', '', true, '✕ tıklandı: dolu değer tek olayda boşaldı'],
    ['prod-eu', '', true, '✕ tıklandı (uzun değer)'],
    ['ua', '', true, 'tümünü seç + sil — kasıtlı temizleme'],
    ['u', '', false, 'geri silerek boşaltmanın SON adımı — commit yok'],
    ['', '', false, 'zaten boş: olay bile sayılmaz'],
    ['uat', 'ua', false, 'geri silme, hâlâ dolu'],
    ['', 'u', false, 'yazmaya başladı'],
    ['ua', 'uat', false, 'yazmaya devam'],
  ];
  for (const [prev, next, want, why] of cases) {
    it(`"${prev}" → "${next}" = ${want} — ${why}`, () => {
      expect(isClearJump(prev, next)).toBe(want);
    });
  }

  // BAĞLANMA — aynı sürümde `shouldAutoCommit`in üç yıl boyunca
  // çağrılmadan test edildiğini ölçtük (bkz. ServicePicker.test.ts).
  // Ders tek bir fonksiyona özgü değil: saf fonksiyonu test etmek,
  // onun ÇAĞRILDIĞINI ölçmez. Mutasyon bunu burada da doğruladı —
  // çağrıyı silmek tablonun tamamını yeşil bırakıyordu.
  it('EnvPicker fonksiyonu GERÇEKTEN çağırıyor', () => {
    const src = stripTsComments(
      readFileSync(join(__dirname, 'EnvPicker.tsx'), 'utf8'));
    // Dal ile commit'i BİRLİKTE arıyoruz. İkisini ayrı ayrı aramak
    // yetmiyordu: `commit('')` dosyada blur yolunda da geçiyor, yani
    // dalın içindeki çağrıyı `setDraft('')` ile değiştiren bir mutasyon
    // (✕ kutuyu boşaltır ama filtreyi BIRAKMAZ — tam da korkulan
    // sessiz hata) kapıdan geçiyordu.
    expect(src, '✕ dalı artık commit etmiyor — kutu boşalır ama global env filtresi ÜZERİNDE KALIR')
      .toMatch(/if \(isClearJump\(prev, next\)\)\s*setTimeout\(\(\) => commit\(''\)/);
  });

  it('yalnız BOŞ hedefi temizleme sayar', () => {
    // Sağlık: kural "büyük sıçrama" değil, "boşa sıçrama". Bir env'den
    // bambaşka bir env'e sıçramak (yapıştırma) shouldAutoCommit'in
    // işi; buradan geçerse iki yol da aynı olayı commit eder.
    expect(isClearJump('prod-eu', 'uat')).toBe(false);
    expect(isClearJump('prod-eu', ' ')).toBe(false);
  });
});
