import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { resolve, join } from 'node:path';

// v0.9.647 — v0.9.646'nın GEREKÇESİ YANLIŞTI, düzeltiliyor.
//
// Orada `.table-wrap.is-fit`'in güvenliğini "tableLayout: 'fixed' +
// width:100% yapısal bir garanti, tablo sığmaya ZORLANIYOR" diye
// gerekçelendirmiştim. DEĞİL: DataTableColgroup `<col style={{width: N}}>`
// ile PİKSEL genişlik yayıyor (DataTable.tsx:242-243) ve sabit düzen o
// değerleri aynen uyguluyor — toplamları konteyneri aşarsa tablo TAŞAR.
//
// Seçim yine de doğruydu (opt-in edilen on tablonun hepsi ölçülmüş
// ≤1150px'di), ama TEST yanlış kuralı çiviliyordu: 1634px'lik sabit
// düzenli bir tabloyu memnuniyetle geçirirdi. Yanlış gerekçe, eksik
// korumadan tehlikelidir — okuyan kişi aramayı bırakır.
//
// Gerçek kural GENİŞLİK. Bu test onu ölçüyor.

// Sığma eşiği. 1440px'lik dizüstü − ~220px sidebar − 40px #content
// padding ≈ 1180px. 1150 biraz pay bırakıyor.
//
// Bu bir VARSAYIM, garanti değil: daha dar bir pencerede sığan bir tablo
// da taşabilir. O durumda yatay kaydırma sayfaya çıkıyor ve yapışkan
// filtre barı içerikle yana kayıyor (v0.9.640'ta operatörün bildirdiği
// sızıntı). Eşiği düşürmek her zaman güvenli taraf.
const FIT_PX = 1150;

const SRC = resolve(__dirname, '../..');

function walk(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir)) {
    const p = join(dir, e);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (p.endsWith('.tsx')) out.push(p);
  }
  return out;
}

// v0.9.664 — ÖLÇÜM İKİ KÖR NOKTAYLA YAŞIYORDU. İkisi de "test yanlış
// sebeple geçiyor" sınıfı: kapı yeşildi ama tabloya hiç bakmıyordu.
//
//  1. Yalnız `const X: DataTableColumn<T>[] = [` biçimi eşleşiyordu.
//     `useMemo<DataTableColumn<T>[]>(() => [` ile tanımlanan kolonlar
//     ölçülmüyordu — TracesResult'ın ikinci tablosu böyle kaçmış.
//  2. Tarama DOSYA-YEREL'di. v0.9.660'ta USER_COLS ayrı bir modüle
//     (usersColumns.ts) taşındı ve Users.tsx o günden beri is-fit ama
//     ÖLÇÜSÜZ kaldı. Kolonlarını ayıran her sayfa aynı boşluğa düşerdi.
//
// Artık iki biçim de eşleşiyor ve dosyanın içe aktardığı modüller de
// taranıyor (bir seviye). Fazladan atıf yalnızca kuralı SIKILAŞTIRIR.
const COLS_RE = /DataTableColumn<[^>]*>\[\]\s*(?:=\s*\[|>\(\(\)\s*=>\s*\[)/g;

/** Kaynaktaki her DataTableColumn dizisinin beyan edilen genişlik toplamı. */
function tableWidths(src: string): number[] {
  const out: number[] = [];
  COLS_RE.lastIndex = 0;
  let m: RegExpExecArray | null;
  while ((m = COLS_RE.exec(src))) {
    let d = 1, j = m.index + m[0].length;
    while (j < src.length && d > 0) {
      if (src[j] === '[') d++;
      else if (src[j] === ']') d--;
      j++;
    }
    const arr = src.slice(m.index + m[0].length, j);
    const w = [...arr.matchAll(/\bwidth:\s*(\d{2,4})\b/g)].map(x => Number(x[1]));
    if (w.length) out.push(w.reduce((a, b) => a + b, 0));
  }
  return out;
}

/** Dosyanın kendi kolonları + BİR SEVİYE içe aktardığı modüllerinkiler. */
function widthsWithImports(file: string): number[] {
  const src = readFileSync(file, 'utf8');
  const out = tableWidths(src);
  for (const m of src.matchAll(/from\s+'([^']+)'/g)) {
    const spec = m[1];
    if (!spec.startsWith('.') && !spec.startsWith('@/')) continue;
    const base = spec.startsWith('@/')
      ? join(SRC, spec.slice(2))
      : resolve(file, '..', spec);
    for (const ext of ['.ts', '.tsx']) {
      try {
        const dep = readFileSync(base + ext, 'utf8');
        if (dep.includes('DataTableColumn')) out.push(...tableWidths(dep));
        break;
      } catch { /* bu uzantı değil */ }
    }
  }
  return out;
}

describe('is-fit güvenlik kuralı — GENİŞLİK', () => {
  // Test dosyaları HARİÇ: bu dosyanın kendi açıklaması 'table-wrap is-fit'
  // dizesini içeriyor ve tarama kendini yakalıyordu — bu kod tabanında
  // dört kez ısıran "kendi yorumunu eşleyen test" tuzağı.
  const users = walk(SRC)
    .filter(p => !p.endsWith('.test.ts') && !p.endsWith('.test.tsx'))
    .filter(p => readFileSync(p, 'utf8').includes('table-wrap is-fit'));

  it('kullanımlar bulunabiliyor', () => {
    expect(users.length).toBeGreaterThan(0);
  });

  // v0.9.872 (tutarlılık denetimi BT5 + BT11) — LİSTE BOŞALDI.
  //
  // Buradaki iki dosya (v0.9.644 pilotları) elle yazılmış <thead>'lerdi ve
  // kolon genişliği BEYAN ETMİYORLARDI: ölçülemiyorlardı, gözle seçilmişlerdi.
  // İkisi de useDataTable'a geçince genişlikleri beyan edildi ve ÖLÇÜME
  // GİRDİLER — dolayısıyla istisnadan çıkarıldılar.
  //
  // Bu adım atlanırsa aşağıdaki "istisna listesi bayatlamamış" testi YANLIŞ
  // SEBEPLE yeşil kalırdı: o test yalnız dosyaların hâlâ `is-fit` olduğunu
  // doğruluyor, ÖLÇÜLEMEZ olduğunu değil. Ölçülebilir bir dosya istisnada
  // kalırsa genişlik bütçesi onu sessizce atlar.
  //
  // Liste boş kalsın: yeni bir girdi, "bu tablo ölçülemiyor" demektir ve
  // gerekçesi yazılmalıdır.
  const UNMEASURED: string[] = [];

  it('ölçülebilir her is-fit tablosu eşiğin ALTINDA', () => {
    const over: string[] = [];
    for (const p of users) {
      if (UNMEASURED.some(u => p.endsWith(u))) continue;
      for (const w of widthsWithImports(p)) {
        if (w > FIT_PX) over.push(`${p.replace(SRC, '')} (${w}px)`);
      }
    }
    expect(over).toEqual([]);
  });

  it('ölçülemeyen istisna listesi bayatlamamış', () => {
    for (const rel of UNMEASURED) {
      expect(users.find(u => u.endsWith(rel)), `${rel} artık is-fit değil`).toBeTruthy();
    }
  });

  // v0.9.664 — kolonlarını ayrı modüle taşıyan sayfa ÖLÇÜLÜYOR mu?
  // Users.tsx'in kolonları usersColumns.ts'te; dosya-yerel tarama onu
  // sessizce atlıyordu ve kapı yeşil kalıyordu.
  it('kolonları ayrı modülde olan sayfa da ölçülüyor', () => {
    const users = join(SRC, 'pages/Users.tsx');
    expect(readFileSync(users, 'utf8')).toContain('table-wrap is-fit');
    expect(tableWidths(readFileSync(users, 'utf8'))).toEqual([]); // dosya-yerel: BOŞ
    expect(widthsWithImports(users).length).toBeGreaterThan(0);   // import'la: ölçülüyor
  });

  // Testin GERÇEKTEN ayırt ettiğini kanıtla: ölçüm fonksiyonu geniş bir
  // tabloyu geniş görmeli. Aksi halde yukarıdaki iddia boş geçerdi.
  it('ölçüm geniş tabloyu yakalıyor (ayırt edicilik)', () => {
    const ep = readFileSync(join(SRC, 'pages/Endpoints.tsx'), 'utf8');
    const w = tableWidths(ep);
    expect(w.length).toBeGreaterThan(0);
    expect(Math.max(...w)).toBeGreaterThan(FIT_PX);
    // Ve Endpoints is-fit DEĞİL — olsaydı taşardı.
    expect(ep).not.toContain('table-wrap is-fit');
  });
});

// v0.9.648 — ESNEK kolon, daraltmanın alternatifi.
//
// /admin/audit, /deploys ve /inbox eşiği aşıyordu. Kolon SİLMEK ya da
// genişlik KIRPMAK yerine, her birinin en geniş SERBEST METİN kolonu
// `flex: true` yapıldı: artan genişliği emiyor, sabit toplamdan
// düşüyor. Bilgi kaybı sıfır.
//
// `flex` v0.9.542'de eklenmişti ama HİÇBİR sayfa kullanmıyordu —
// bugünün tekrar eden deseni: yetenek var, kullanan yok.
describe('flex kolonu', () => {
  const FLEX_USERS = [
    'pages/AdminAudit.tsx', 'pages/Deploys.tsx', 'pages/Inbox.tsx',
  ];

  it('esnek kolon kullanan sayfalar hâlâ kullanıyor', () => {
    for (const rel of FLEX_USERS) {
      const s = readFileSync(join(SRC, rel), 'utf8');
      expect(s, `${rel} flex kolonunu kaybetmiş`).toContain('flex: true');
    }
  });

  // Esnek kolonun genişliği sabit toplama GİRMEMELİ — girerse daraltma
  // etkisi kaybolur ve tablo yine taşar.
  it('esnek kolon width BEYAN ETMİYOR', () => {
    for (const rel of FLEX_USERS) {
      const s = readFileSync(join(SRC, rel), 'utf8');
      const lines = s.split('\n').filter(l => l.includes('flex: true'));
      expect(lines.length).toBeGreaterThan(0);
      for (const l of lines) {
        expect(l, `${rel}: flex kolonu ayrıca width taşıyor`).not.toMatch(/\bwidth:\s*\d/);
      }
    }
  });
});

// v0.9.662 — KURTULUŞ YOLU.
//
// is-fit iç kaydırma güvenlik ağını kaldırıyor: sürüklenmiş bir kolon
// genişliği tabloyu taşırırsa yatay kaydırma sayfaya çıkar (v0.9.640'ta
// operatörün bildirdiği sızıntı) ve genişlik localStorage'da KALICI —
// v0.9.660'a kadar geri dönüş yolu yoktu.
//
// Çözüm hatanın yapıldığı yerde: tutamağın kendisi. Orada olmasının
// sebebi kapsam — tutamak TEK paylaşılan bileşen, yani useDataTable
// kullanan HER tablo kapsanıyor.
describe('sürüklenmiş genişlikten çıkış', () => {
  const dt = readFileSync(join(SRC, 'components/DataTable.tsx'), 'utf8');

  it('resize tutamağı çift tıkla düzeni sıfırlıyor', () => {
    const i = dt.indexOf('export function ColResizeHandle');
    expect(i).toBeGreaterThan(-1);
    const body = dt.slice(i, i + 800);
    expect(body).toContain('onDoubleClick');
    expect(body).toContain('resetLayout()');
  });

  // Keşfedilemeyen bir kurtuluş yolu yok sayılır.
  it('tutamağın title\'ı bunu söylüyor', () => {
    expect(dt).toContain('double-click to reset all column widths');
  });

  // resetLayout gerçekten TEMİZLEMELİ — kısmi sıfırlama taşmayı sürdürür.
  it('resetLayout tüm genişlikleri siliyor', () => {
    expect(dt).toContain('resetLayout = useCallback(() => setColWidths({})');
  });
});
