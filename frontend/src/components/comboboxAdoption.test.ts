// Combobox benimseme kapısı — açılır listelerin SAYFA kolu (v0.9.1020).
//
// NE ÇİVİLİYOR: bir öneri listesinin klavyeyle gezilebilir olduğunu.
//
// Ölçülen taban: depoda öneri listesi İKİ ayrı şekilde yazılıyordu.
// Ev `Combobox` atomu (ok tuşları + Enter + Esc + vurgulanan satır) ve
// native `<datalist>`. İkincisi tarayıcıya bırakılmış bir sözleşmedir ve
// tarayıcılar aynı fikirde DEĞİL: Chrome ok tuşlarıyla listeyi gezdirir,
// Firefox yalnız yazarken filtreler, Safari'de ok tuşları listeyi
// AÇMAZ bile. Yani "klavyeyle seçebiliyorum" iddiası operatörün
// tarayıcısına göre doğru ya da yanlıştı — ve hiçbir kapı bunu görmüyordu:
// `tsc` `list=` niteliğini geçerli sayar, eslint bir a11y kuralı
// uygulamaz (datalist teknik olarak erişilebilirdir), jsdom açılır
// listeyi RENDER ETMEZ, `make audit` JSX'e bakmaz.
//
// v0.9.1023 — satır içi düzenleyici İKİLİSİ listeden ÇIKTI (API
// boşluğu v0.9.1022'de kapandı). Kalan dördü picker kolu.
//
// LİSTE DONMUŞ ve yalnız KÜÇÜLÜR. Kalan her dosya için gerekçe kayıtlı,
// ve gerekçesi olan dosya native listeyi GERÇEKTEN taşımak zorunda:
// biri dönüştürüp listeden çıkarmayı unutursa kapı bunu söyler. Aksi
// hâlde liste sessizce bayatlar ve "6 istisna" diye okunan şey aslında
// 6 ay önce kapanmış bir borç olur.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { stripTsComments } from '../styles/zLayers.test';

const SRC = resolve(__dirname, '..');
const read = (rel: string) => stripTsComments(readFileSync(join(SRC, rel), 'utf8'));

// İmza PARÇALARDAN kuruluyor. Kapı `src/` ağacını tarıyor ve bu dosya
// da o ağacın içinde: imzayı düz yazsaydım tarayıcı KENDİNİ yakalar,
// ve onu ayıklamak için eklenecek "bu dosya hariç" istisnası kapının
// tek gerçek kör noktası olurdu.
const OPEN = '<' + 'datalist';
// `list=` niteliği tüketici yarısı. `(?<![\w-])` şart: onsuz `checklist=`
// veya `playlist=` gibi bir prop kapıyı ALAKASIZ bir dosyada patlatır
// (önek çakışması — v0.9.1015 dersi).
const CONSUME = /(?<![\w-])list=[{"]/;

const carries = (src: string) => src.includes(OPEN) || CONSUME.test(src);

// ——— Dönüşen SAYFA kolu ———————————————————————————————————
const CONVERTED = [
  'pages/settings/ClustersTab.tsx',
  'pages/settings/ChannelModal.tsx',
  // v0.9.1023 — SATIR İÇİ düzenleyiciler. v0.9.1020'de "API boşluğu"
  // gerekçesiyle burada DURULMUŞTU; boşluk v0.9.1022'de kapandı
  // (autoFocus / disabled / onBlurCommit / onEscape).
  'pages/Users.tsx',
  'components/viz/MetricQueryEditor.tsx',
];

// ——— Native listeyi taşımaya DEVAM eden dosyalar ————————————
//
// Her satır bir borç DEĞİL — üçü bilinçli kapsam kararı, ikisi ölçülmüş
// bir API boşluğu.
const STILL_NATIVE: Record<string, string> = {
  // PICKER KOLU — ayrı, ölçülü bir dilim. Bu dördü sunucu tarafında
  // arayan pickerlar: öneri listesi eager bir katalog DEĞİL, her tuşta
  // yeniden gelen debounced bir sonuç kümesi. Combobox'ın filtresi
  // istemci tarafında çalıştığı için taşıma mekanik değil davranışsal
  // bir değişiklik olurdu (kim filtreliyor sorusu yer değiştirir).
  'components/ServicePicker.tsx': 'picker kolu — sunucu-taraflı arama, ayrı dilim',
  'components/OperationPicker.tsx': 'picker kolu — sunucu-taraflı arama, ayrı dilim',
  'components/MetricNamePicker.tsx': 'picker kolu — sunucu-taraflı arama, ayrı dilim',
  'components/EnvPicker.tsx': 'picker kolu — sunucu-taraflı arama, ayrı dilim',
};

function walk(dir: string, acc: string[] = []): string[] {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    if (e.name === 'node_modules' || e.name === 'dist') continue;
    const p = join(dir, e.name);
    if (e.isDirectory()) walk(p, acc);
    else if (/\.tsx?$/.test(e.name)) acc.push(p.slice(SRC.length + 1));
  }
  return acc;
}

describe('Combobox benimseme — sayfa kolu', () => {
  it('dönüşen yüzeyler atomu kullanıyor', () => {
    for (const f of CONVERTED) {
      const src = read(f);
      expect(src, `${f} Combobox'ı import etmiyor`).toMatch(/from '@\/components\/Combobox'/);
      expect(src, `${f} <Combobox> basmıyor`).toMatch(/<Combobox[\s/>]/);
    }
  });

  it('dönüşen yüzeylerde native liste KALMADI', () => {
    for (const f of CONVERTED) {
      expect(carries(read(f)), `${f} hâlâ native öneri listesi taşıyor`).toBe(false);
    }
  });

  it('serbest metin KORUNUYOR — hiçbir çağrı seçimi listeye hapsetmiyor', () => {
    // Combobox tasarımı gereği serbest metne izin verir (Enter yazılanı
    // alır). Kapı çağrı tarafını sınıyor: `value`yu geri yazan bir
    // `onChange` şart. Bir gün biri "yalnız listeden seç" diye araya
    // doğrulama koyarsa, datalist'ten taşınan davranış sessizce daralır.
    for (const f of CONVERTED) {
      const src = read(f);
      const calls = src.match(/<Combobox\b[\s\S]{0,600}?\/>/g) ?? [];
      // Pencere PARİTESİ — v0.9.1023'te ölçüldü: eski 300 karakterlik
      // pencere, Users.tsx'in yeni (uzun, çok satırlı) çağrısını hiç
      // EŞLEŞTİREMİYORDU. Eşleşme yoksa döngü boş dönüyor ve testler
      // sessizce hiçbir şey ölçmüyordu — kapı yeşil, kapsam sıfır.
      // Bu assert kapsamın sayıyla eşleşmesini zorunlu kılıyor, yani
      // yarın daha uzun bir çağrı yazılırsa kapı SUSMAK yerine düşer.
      expect(calls.length, `${f}: ${src.match(/<Combobox\b/g)?.length} çağrı var ama ${calls.length} tanesi ölçüldü — pencereyi büyüt`)
        .toBe(src.match(/<Combobox\b/g)?.length ?? 0);
      for (const call of calls) {
        expect(call, `${f}: <Combobox> value vermiyor`).toMatch(/\bvalue=/);
        expect(call, `${f}: <Combobox> onChange vermiyor`).toMatch(/\bonChange=/);
        expect(call, `${f}: <Combobox> options vermiyor`).toMatch(/\boptions=/);
      }
    }
  });

  // ——— Satır içi düzenleyicilerin ÇIKIŞ yolu ————————————————
  //
  // Mutasyonla bulundu: `onEscape`i TeamEditor'dan silmek kapıyı
  // yeşil bırakıyordu. Oysa v0.9.1022'nin tüm gerekçesi buydu — bir
  // düzenleme MODUna girip çıkamamak, taşımanın bozacağı tek şeydi.
  // Bağımsız bir alandan farkı: alan hep oradadır, moda girilmez.
  const INLINE_EDITORS: Record<string, string[]> = {
    // autoFocus: moda girince odak orada olmalı (tıkla-sonra-tıkla yok).
    // onEscape: commit ETMEDEN çıkış. onBlurCommit: bu hücrenin
    // belgelenmiş davranışı "blur'da kaydet".
    'pages/Users.tsx': ['autoFocus', 'onEscape', 'onBlurCommit'],
    // Çipin açık bir "Add" düğmesi var; blur'da EKLEMEK eski davranışı
    // değiştirirdi — onBlurCommit bilerek YOK.
    'components/viz/MetricQueryEditor.tsx': ['autoFocus', 'onEscape'],
  };

  it('satır içi düzenleyiciler odak + İPTAL yolunu taşıyor', () => {
    for (const [f, props] of Object.entries(INLINE_EDITORS)) {
      expect(CONVERTED, `${f} dönüşenler listesinde değil`).toContain(f);
      const calls = read(f).match(/<Combobox\b[\s\S]{0,600}?\/>/g) ?? [];
      expect(calls.length, `${f}: <Combobox> çağrısı bulunamadı`).toBeGreaterThan(0);
      for (const p of props) {
        expect(calls.some(c => new RegExp(`\\b${p}[=\\s/}]`).test(c)),
          `${f}: satır içi düzenleyici ${p} taşımıyor`).toBe(true);
      }
    }
  });

  it('filtre çipi blur’da EKLEMİYOR — eski davranış korunuyor', () => {
    const calls = read('components/viz/MetricQueryEditor.tsx')
      .match(/<Combobox\b[\s\S]{0,600}?\/>/g) ?? [];
    expect(calls.some(c => /\bonBlurCommit/.test(c)),
      'çip blur’da filtre eklemeye başladı — datalist sürümü bunu yapmıyordu').toBe(false);
  });

  // ——— Donmuş liste ————————————————————————————————————————
  it('native listeyi taşıyan HER dosya listede', () => {
    const stray = walk(SRC)
      .filter(f => !(f in STILL_NATIVE))
      .filter(f => carries(read(f)));
    expect(stray, `native öneri listesi listede olmayan dosyalarda: ${stray.join(', ')}`)
      .toEqual([]);
  });

  it('liste yalnız KÜÇÜLÜR — her gerekçe hâlâ gerçek', () => {
    for (const [f, reason] of Object.entries(STILL_NATIVE)) {
      expect(reason.length, `${f} gerekçesiz`).toBeGreaterThan(10);
      expect(carries(read(f)), `${f} artık native liste taşımıyor — listeden ÇIKAR`).toBe(true);
    }
  });
});
