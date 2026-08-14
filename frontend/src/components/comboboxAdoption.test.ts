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
// boşluğu v0.9.1022'de kapandı).
// v0.9.1024 — picker ailesi de geçti: LİSTE SIFIRLANDI. Kapı artık
// bir istisna listesi değil, TAM KİLİT: frontend'de native açılır
// liste YOK. Yeni bir tane yazmanın maliyeti, gerekçe eklemek değil,
// kapıyı kırmak.
//
// Boş liste kasıtlı olarak SİLİNMEDİ: bir dosya yeniden datalist'e
// dönerse "listeye ekle" refleksi doğar; buradaki yorum o refleksin
// karşılığını (yasak, gerekçesi bu) yazılı tutuyor.
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

// comboboxCalls — bir dosyadaki <Combobox …/> çağrılarını TAM olarak
// çıkarır (süslü parantez derinliği sayarak).
//
// Neden düz regex DEĞİL: kapı önce `<Combobox[\s\S]{0,300}?/>` ile
// yazılmıştı. v0.9.1023'te Users.tsx'in çağrısı 300 karakteri aştı ve
// regex HİÇ eşleşmedi — döngü boş döndü, testler sessizce hiçbir şey
// ölçmedi. Pencereyi 600'e çıkardım; v0.9.1024'te ServicePicker'ın
// çağrısı onu da aştı. Sabit pencere yanlış araç: her büyümede kapı
// SESSİZCE kör oluyor, kırmızıya dönmüyor. Kapanmamış bir çağrı
// artık istisna fırlatır — susmaktansa düşsün.
function comboboxCalls(src: string): string[] {
  const out: string[] = [];
  let from = 0;
  for (;;) {
    const start = src.indexOf('<Combobox', from);
    if (start < 0) break;
    let depth = 0, end = -1;
    for (let j = start + 9; j < src.length; j++) {
      const c = src[j];
      if (c === '{') depth++;
      else if (c === '}') depth--;
      else if (c === '/' && src[j + 1] === '>' && depth === 0) { end = j + 2; break; }
    }
    if (end < 0) throw new Error('kapanmamış <Combobox> çağrısı — kapı ölçemez');
    out.push(src.slice(start, end));
    from = end;
  }
  return out;
}

// ——— Dönüşen SAYFA kolu ———————————————————————————————————
const CONVERTED = [
  'pages/settings/ClustersTab.tsx',
  'pages/settings/ChannelModal.tsx',
  // v0.9.1023 — SATIR İÇİ düzenleyiciler. v0.9.1020'de "API boşluğu"
  // gerekçesiyle burada DURULMUŞTU; boşluk v0.9.1022'de kapandı
  // (autoFocus / disabled / onBlurCommit / onEscape).
  'pages/Users.tsx',
  'components/viz/MetricQueryEditor.tsx',
  // v0.9.1024 — PICKER AİLESİ. Sunucu-taraflı arayanlar; `serverFiltered`
  // ile atomun istemci süzgeci kapalı.
  'components/ServicePicker.tsx',
  'components/OperationPicker.tsx',
  'components/MetricNamePicker.tsx',
  'components/EnvPicker.tsx',
];

// Sunucu ARAYAN picker'lar — listeyi sunucu belirler, atom süzmez.
const SERVER_PICKERS = [
  'components/ServicePicker.tsx',
  'components/OperationPicker.tsx',
  'components/MetricNamePicker.tsx',
  'components/EnvPicker.tsx',
];

// ——— Native listeyi taşımaya DEVAM eden dosyalar ————————————
//
// BOŞ. "Kim süzüyor" sorusu v0.9.1024'te `serverFiltered` ile
// cevaplandı: sunucu-taraflı picker'ların listesi ZATEN cevap, atom
// ona dokunmuyor. Bu olmadan taşıma joker karakterleri (`pay*`)
// kırardı — istemci alt-dize süzgeci "pay*" dizesini seçeneklerin
// İÇİNDE arar ve liste boşalırdı.
const STILL_NATIVE: Record<string, string> = {};

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
      const calls = comboboxCalls(src);
      // Kapsam PARİTESİ: çıkarıcı her `<Combobox` için bir çağrı
      // döndürmeli. Tutmuyorsa kapı bir çağrıyı ölçmüyor demektir.
      expect(calls.length, `${f}: çağrı sayısı tutmuyor — kapı bir çağrıyı ölçmüyor`)
        .toBe((src.match(/<Combobox\b/g) ?? []).length);
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
      const calls = comboboxCalls(read(f));
      expect(calls.length, `${f}: <Combobox> çağrısı bulunamadı`).toBeGreaterThan(0);
      for (const p of props) {
        expect(calls.some(c => new RegExp(`\\b${p}[=\\s/}]`).test(c)),
          `${f}: satır içi düzenleyici ${p} taşımıyor`).toBe(true);
      }
    }
  });

  it('filtre çipi blur’da EKLEMİYOR — eski davranış korunuyor', () => {
    const calls = comboboxCalls(read('components/viz/MetricQueryEditor.tsx'));
    expect(calls.some(c => /\bonBlurCommit/.test(c)),
      'çip blur’da filtre eklemeye başladı — datalist sürümü bunu yapmıyordu').toBe(false);
  });

  // ——— Sunucu-taraflı picker'lar ————————————————————————————
  it('sunucu picker’ları istemci süzgecini KAPATIYOR', () => {
    // En sinsi regresyon burada olurdu ve tsc göremezdi: `serverFiltered`
    // düşerse atom listeyi `value`ya göre alt-dize süzer. Normal
    // aramada fark edilmez (sunucu zaten eşleşenleri döndü), ama JOKER
    // sorguda (`pay*`, `*pay*`, `p?y`) liste TAMAMEN boşalır — çünkü
    // "pay*" dizesi hiçbir servis adının içinde geçmez. Yani özellik
    // yalnız onu kullanan operatör için ölür.
    for (const f of SERVER_PICKERS) {
      const calls = comboboxCalls(read(f));
      expect(calls.length, `${f}: <Combobox> çağrısı yok`).toBe(1);
      expect(calls[0], `${f}: serverFiltered düştü — joker aramalar boş liste döner`)
        .toMatch(/\bserverFiltered\b/);
    }
  });

  it('metrik picker’ı birim/tip etiketini KORUYOR', () => {
    // datalist'te bu `label` niteliğiydi ve Safari onu HİÇ
    // göstermiyordu; atomda satır içi etiket olarak her tarayıcıda
    // görünüyor. Düşerse operatör "counter mı gauge mı, saniye mi
    // milisaniye mi" sorusunu picker'da cevaplayamaz.
    expect(read('components/MetricNamePicker.tsx'), 'optionMeta düştü')
      .toMatch(/\boptionMeta=/);
  });

  it('kesinti uyarısı KORUNDU — "showing N of M" sessizce düşmedi', () => {
    // datalist sürümünde bu, `disabled` bir <option> ile taklit
    // ediliyordu. Kaybolsaydı operatör 200 satırlık bir listeyi TAM
    // katalog sanardı — 10k servisli bir kurulumda tam olarak yanlış
    // sonuç.
    for (const f of SERVER_PICKERS) {
      expect(read(f), `${f}: kesinti göstergesi (footer) düştü`).toMatch(/\bfooter=/);
      expect(read(f), `${f}: truncated hesabı düştü`).toMatch(/truncated/);
    }
  });

  // ——— TAM KİLİT ————————————————————————————————————————————
  it('tarama gerçekten bir şey buluyor', () => {
    // Sağlık assert'i: `walk` bozulursa aşağıdaki kilit BOŞ küme
    // üzerinden yeşil koşar ve hiçbir şey ölçmez.
    const all = walk(SRC);
    expect(all.length).toBeGreaterThan(200);
    expect(all).toContain('components/Combobox.tsx');
  });

  it('frontend’de native açılır liste KALMADI', () => {
    const stray = walk(SRC)
      .filter(f => !(f in STILL_NATIVE))
      .filter(f => carries(read(f)));
    expect(stray, `native öneri listesi hâlâ var: ${stray.join(', ')}`)
      .toEqual([]);
  });

  it('liste yalnız KÜÇÜLÜR — her gerekçe hâlâ gerçek', () => {
    for (const [f, reason] of Object.entries(STILL_NATIVE)) {
      expect(reason.length, `${f} gerekçesiz`).toBeGreaterThan(10);
      expect(carries(read(f)), `${f} artık native liste taşımıyor — listeden ÇIKAR`).toBe(true);
    }
  });
});
