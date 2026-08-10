// loadingLabels — MB3 kapısı (v0.9.931)
//
// Ne çiviliyor: bir `<Button>`ın çocuklarında ELLE yazılmış yükleme etiketi
// olmadığı. Yani `{busy ? 'Saving…' : 'Save'}` değil, `loading={busy}`.
//
// Neden kapı gerekiyor: bu dönüşüm iki turda yapıldı (v0.9.925 on iki site,
// v0.9.931 kalan kırk bir) ve elle yazım HER ZAMAN daha kolay olanı — bir
// sonraki yeni buton yine ternary ile gelir. tsc bunu göremez (iki dal da
// string), eslint göremez, `make audit`in kuralı yok.
//
// Elle yazımın neyi kaybettirdiği somut: atom `loading` iken etiketi KORUYUP
// yanına spinner koyuyor, `disabled` basıyor (çift gönderimi yutar) ve
// `aria-busy` ile "kullanılamaz"ı "şu an çalışıyor"dan ayırıyor. Ternary
// bunların ÜÇÜNÜ de kaybediyor; ekranda görünen tek fark etiketin değişmesi
// olduğu için kayıp fark edilmiyor.
//
// KAPSAM DAR ve bilinçli: yalnız (a) bir "iş sürüyor" bayrağı ve (b) bir
// İLERLEME etiketi. `{copied ? 'Copied' : 'Copy'}`, `{expanded ? '▼' : '▶'}`
// gibi DURUM geçişleri MB3 değil — onlar iki eşit durumun etiketi, biri
// diğerinin "yükleniyor" hâli değil.
import { describe, it, expect } from 'vitest';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { resolve, join } from 'node:path';

const SRC = resolve(__dirname, '..');

function walk(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir)) {
    const p = join(dir, e);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (p.endsWith('.tsx') && !p.endsWith('.test.tsx')) out.push(p);
  }
  return out;
}

// Yorumları BOŞALT, silme: satır numaraları raporlanıyor. Bu depoda "test
// kendi düzyazısını kural sanıyor" tuzağı yedi kez ısırdı — dönüştürülen
// dosyaların yorumları eski ternary'yi tarihsel kayıt olarak alıntılıyor.
function stripComments(src: string): string {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, ' '))
    .replace(/^\s*\/\/.*$/gm, '');
}

// İmza ÇALIŞMA ANINDA parçalardan kuruluyor: bu dosyanın kendi düzyazısı
// hiçbir taramaya yem olmasın.
const BUSY_FLAG = new RegExp(['busy', 'sav' + 'ing', 'import' + 'ing',
  'isPend' + 'ing', 'isFetch' + 'ing', 'ack' + 'ing'].join('|'), 'i');
const PROGRESS_LABEL = /(…|\.\.\.|ing\b)/;
const BUTTON_BLOCK = /<Button\b[^>]*>[\s\S]{0,320}?<\/Button>/g;
const TERNARY_LABEL = /\{\s*([A-Za-z_$][\w$.]*)\s*\?\s*(['"`])([^'"`]{0,60})\2\s*:\s*(['"`])([^'"`]{0,80})\4\s*\}/;

describe('MB3 — yükleme durumu atomda', () => {
  it('hiçbir <Button> elle yükleme etiketi yazmıyor', () => {
    const offenders: string[] = [];
    for (const file of walk(SRC)) {
      const src = stripComments(readFileSync(file, 'utf8'));
      for (const m of src.matchAll(BUTTON_BLOCK)) {
        const t = TERNARY_LABEL.exec(m[0]);
        if (!t) continue;
        const [, flag, , trueLabel] = t;
        if (!BUSY_FLAG.test(flag) || !PROGRESS_LABEL.test(trueLabel)) continue;
        const line = src.slice(0, m.index).split('\n').length;
        offenders.push(`${file.slice(SRC.length + 1)}:${line} — {${flag} ? '${trueLabel}' : …}`);
      }
    }
    expect(offenders, `loading={…} kullan:\n${offenders.join('\n')}`).toEqual([]);
  });

  it('kapı GERÇEKTEN yakalıyor (mutasyon kontrolü)', () => {
    // Kapının kendi imzasını sınıyoruz: yeşil kalması "kural tutuyor" değil
    // "tarama kör" anlamına da gelebilirdi.
    const sample = "<Button variant=\"primary\" disabled={busy}>{busy ? 'Sav' + \"ing…' : 'Save'}</Button>"
      .replace("'Sav' + \"ing…'", "'Saving…'");
    const t = TERNARY_LABEL.exec(sample);
    expect(t).not.toBeNull();
    expect(BUSY_FLAG.test(t![1])).toBe(true);
    expect(PROGRESS_LABEL.test(t![3])).toBe(true);
  });

  it('DURUM geçişleri kapsam DIŞI kalıyor', () => {
    // `{copied ? 'Copied' : 'Copy'}` bir yükleme değil, iki eşit durum.
    const sample = "<Button>{copied ? 'Copied' : 'Copy'}</Button>";
    const t = TERNARY_LABEL.exec(sample);
    expect(t).not.toBeNull();
    expect(BUSY_FLAG.test(t![1])).toBe(false);
  });
});
