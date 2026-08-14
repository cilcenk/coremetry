import { describe, it, expect } from 'vitest';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';

// v0.9.864 — UX denetimi §4.3 seçenek (b), operatör onayı 2026-08-09.
//
// Env bugün YARI-uygulanan bir global filtre: picker `range` verilen HER
// sayfada basılıyor (Topbar.tsx), ama `?env=`i kendi sorgusuna geçiren sayfa
// sayısı sınırlı. Ara durum en kötüsüydü — kullanıcıya UYGULANMAYAN bir filtre
// AKTİF gösteriliyordu: `env=uat` seçili operatör /messaging, /clusters,
// /hosts, /metrics, /slow-queries, /deploys gezerken tüm ortamların verisine
// bakıp env'e güveniyordu. Adres çubuğu `env=uat` gösterdiği için fark etmesi
// neredeyse imkânsızdı (K9/K8).
//
// Seçenek (a) — env'i her API'ye gerçekten uygulamak — sayfa-başına backlog ve
// bu sürümün KAPSAMI DIŞINDA. Bu sürüm yalnız dürüstlüğü kuruyor.
//
// Testin koruduğu şey: bayrak İKİ YÖNDE de yalan söyleyemez.
//   • envApplies veren bir sayfa env'i gerçekten OKUMAK zorunda;
//   • env okuyan her sayfa ya "uyguluyor" ya da gerekçesiyle "link-only"
//     olarak SINIFLANDIRILMIŞ olmak zorunda.
// İkinci yarısı asıl risk: birisi soluk picker'ı görüp filtreyi eklemek yerine
// bayrağı ekleyerek "düzeltebilir" — o an yalan geri gelir.

const SRC = join(__dirname, '..');

function tsxFiles(dir: string, rel = ''): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    const relPath = rel ? `${rel}/${entry}` : entry;
    if (statSync(full).isDirectory()) { out.push(...tsxFiles(full, relPath)); continue; }
    if (!entry.endsWith('.tsx') || entry.endsWith('.test.tsx')) continue;
    out.push(relPath);
  }
  return out;
}

const topbarPages = tsxFiles(SRC)
  .map(rel => ({ rel, text: readFileSync(join(SRC, rel), 'utf8') }))
  // v0.9.1019 — SÖZCÜK SINIRI şart. Düz `includes('<Topbar')` bir ÖNEK
  // eşleşmesiydi ve `<TopbarSearch />` eklenir eklenmez Topbar.tsx'in
  // KENDİSİ "topbar basan sayfa" sayıldı: kapı, bileşenin prop TİPİNİ
  // (`envApplies?: boolean`) bir sayfa İDDİASI sanıp iki iddiada birden
  // patladı. Bundan sonra `<Topbar` yalnız ardından boşluk, `/` veya
  // `>` gelirse sayılıyor — yani gerçek bir JSX kullanımıysa.
  .filter(f => /<Topbar[\s/>]/.test(f.text));

/** Pages that CLAIM to apply the env filter. `envApplies={false}` is a claim of the opposite. */
const claimsApplies = (text: string) => /envApplies(?!=\{false\})/.test(text);
/** Pages that read the global env at all. */
const readsEnv = (text: string) => text.includes('useUrlEnv');

// Env'i kendi SORGULARINA geçiren sayfalar — koddan doğrulandı 2026-08-09.
const APPLIES = new Set([
  // v0.9.941 (B1/K8) — /problems Exceptions sekmesi. Seçici bu sayfada
  // GÖRÜNÜYOR ama uygulanmıyordu (`envApplies={false}`): operatör prod'u
  // seçip int/uat exception'larını listede görmeye devam ediyordu.
  // Sunucu artık env'i üye servislere çözüp `service IN (…)` ile
  // daraltıyor, yani daraltma limit/offset'ten ÖNCE ısırıyor.
  'features/anomalies/AnomaliesPage.tsx',
  'features/anomalies/ProblemsSection.tsx',
  'pages/Databases.tsx',
  // v0.9.942 (B2/K9) — /explore. Sayfa `env` sözcüğünü hiç tanımıyordu:
  // picker basılıyor, `?env=` adres çubuğunda duruyor, sorgu TÜM
  // ortamları okuyordu. Daraltma artık fetch'e giden BuilderState
  // kopyasına çip olarak enjekte ediliyor (pages/explore/globalScope.ts)
  // — çip satırında görünmez (silinemez), ama sayfa üstünde "Kapsam:"
  // rozetiyle GÖRÜNÜR.
  'pages/Explore.tsx',
  'pages/EndpointDetail.tsx',
  'pages/Endpoints.tsx',
  'pages/Inbox.tsx',
  'pages/Logs.tsx',
  'pages/Services.tsx',
  'pages/Traces.tsx',
]);

// Env'i OKUYAN ama kendi verisine UYGULAMAYAN sayfalar. Bunlar en kolay
// yanlış sınıflandırılacak dosyalar, o yüzden gerekçeleriyle yazılı:
//
//   • Service.tsx — env'i yalnız /endpoints drill linkine geçirir; sayfanın
//     kendi bundle'ı (api.serviceBundle) env almaz, yani sayfadaki RED
//     sayıları TÜM ortamları kapsar.
//   • DatabaseDetail.tsx — env'i yalnız "geldiğin liste bu env'e daraltılmıştı"
//     rozetini basmak için okur; detay sorguları env almaz.
//
// Rapor ikisini de "env sayan sayfalar" arasında listeliyordu; kod aksini
// söylüyor. Bu satırlar o farkı kalıcı kılar.
const READS_BUT_DOES_NOT_APPLY = new Set([
  'pages/Service.tsx',
  'pages/DatabaseDetail.tsx',
]);

describe('envApplies — §4.3 (b) dürüstlük sözleşmesi', () => {
  it('bayrağı veren sayfa env\'i gerçekten OKUR', () => {
    const lying = topbarPages
      .filter(f => claimsApplies(f.text) && !readsEnv(f.text))
      .map(f => f.rel).sort();
    expect(lying, 'envApplies veriyor ama useUrlEnv çağırmıyor — picker aktif görünür, filtre yoktur.')
      .toEqual([]);
  });

  it('bayrağı veren sayfa kümesi koddan doğrulanmış listeyle AYNI', () => {
    const actual = topbarPages.filter(f => claimsApplies(f.text)).map(f => f.rel).sort();
    expect(actual, [
      'Bir sayfa env filtresini gerçekten uygulamaya başladıysa APPLIES\'a ekle;',
      'uygulamayı bıraktıysa hem bayrağı hem satırı kaldır. Liste kod-doğrulamalı.',
    ].join('\n')).toEqual([...APPLIES].sort());
  });

  it('env OKUYAN her sayfa sınıflandırılmıştır — sessiz üçüncü hâl yok', () => {
    const unclassified = topbarPages
      .filter(f => readsEnv(f.text))
      .map(f => f.rel)
      .filter(rel => !APPLIES.has(rel) && !READS_BUT_DOES_NOT_APPLY.has(rel))
      .sort();
    expect(unclassified, [
      'Bu sayfalar global env\'i okuyor ama uygulayıp uygulamadıkları yazılı değil.',
      'Sorguya geçiyorsa <Topbar envApplies /> + APPLIES; yalnız link/rozet içinse',
      'READS_BUT_DOES_NOT_APPLY\'a gerekçesiyle ekle.',
    ].join('\n')).toEqual([]);
  });

  it('link-only sayfalar bayrağı ELE GEÇİREMEZ', () => {
    // Soluk picker'ı "düzeltmenin" kolay ve yanlış yolu: filtreyi eklemek
    // yerine bayrağı eklemek. O an yalan geri gelir.
    for (const rel of READS_BUT_DOES_NOT_APPLY) {
      const f = topbarPages.find(p => p.rel === rel);
      expect(f, `${rel} artık Topbar basmıyor — sınıflandırmayı güncelle`).toBeTruthy();
      expect(claimsApplies(f!.text), `${rel} env'i yalnız link/rozet için okuyor; envApplies YALAN olur.`).toBe(false);
    }
  });

  it('Exceptions listesi env\'i GERÇEKTEN uyguluyor (K8 kapandı)', () => {
    // v0.9.941 — bu test v0.9.864'te TERSİNİ pinliyordu: backend
    // `/api/exception-groups` env parametresi kazanana dek AÇIK reddetme
    // (bayrağı açıkça kapatmak) sessiz varsayılandan daha iyi belgeydi.
    // Sidebar rozeti env-scoped olduğu için rozet "3" derken sayfa 40
    // satır gösteriyordu — o tutarsızlık artık yok.
    //
    // Uç parametreyi kazandı; dolayısıyla pinin YÖNÜ de dönüyor. Eski
    // beklentiyi bırakmak, kapanmış bir kusuru sonsuza kadar "açık"
    // olarak zorunlu kılmak olurdu.
    const page = topbarPages.find(f => f.rel === 'features/anomalies/AnomaliesPage.tsx');
    expect(page).toBeTruthy();
    expect(page!.text.includes('envApplies={false}'),
      'AÇIK reddetme geri gelmiş — uç artık env destekliyor').toBe(false);
    expect(claimsApplies(page!.text)).toBe(true);
    // Bayrak yalnız işaret; asıl sözleşme parametrenin İSTEĞE girmesi.
    expect(page!.text).toContain('env: env || undefined,');
  });

  it('picker atıl hâlde GİZLENMEZ, devre dışı kalır', () => {
    // Gizlemek, operatörün sticky bir env seçimi olduğunu ve burada yok
    // sayıldığını göremeyeceği anlamına gelirdi — sessizce yanlış olan tam
    // da buydu. ✕ (temizle) çalışır kalır.
    const picker = readFileSync(join(SRC, 'components/EnvPicker.tsx'), 'utf8');
    expect(picker).toMatch(/disabled=\{!applies\}/);
    // v0.9.1024 — `aria-disabled` DÜŞTÜ, sözleşme düşmedi. Picker
    // native <input> yerine Combobox atomunu basıyor ve atom `disabled`
    // niteliğini gerçek input'a geçiriyor; native `disabled` zaten
    // "aria-disabled=true" semantiğini taşır. İkisini birlikte yazmak
    // ARIA-in-HTML kuralının açıkça uyardığı çift beyandır. Kapı
    // ölçtüğü ŞEYİ (atıl ama GÖRÜNÜR kontrol) yeni yazılışta ölçmeye
    // devam ediyor — sayıyı düşürmek yerine kapsamı taşıdık.
    expect(picker).toMatch(/<Combobox[\s\S]{0,600}?disabled=\{!applies\}/);
    // Atıl hâlde erken return YOK: bileşen yine render eder.
    expect(picker).not.toMatch(/if\s*\(!applies\)\s*return null/);
    // ✕ (temizle) kilitliyken de çalışmalı — atomun sözleşmesi
    // (v0.9.1022) ve comboboxEsc.test.tsx'te canlı mount ile çivili.
    expect(readFileSync(join(SRC, 'components/Combobox.tsx'), 'utf8'),
      'Combobox kilitli alanda ✕ düğmesini kaldırdı — bayat bir global env her sayfadan bırakılabilmeli')
      .toMatch(/\{value \? \(/);
  });

  it('Topbar varsayılanı opt-in — işaretlenmemiş sayfa "uygulanmıyor" der', () => {
    const topbar = readFileSync(join(SRC, 'components/Topbar.tsx'), 'utf8');
    expect(topbar).toMatch(/envApplies\?: boolean/);
    // Picker'a HER İKİ dalda da geçirilir (range'li ve showEnv-only).
    expect((topbar.match(/<EnvPicker applies=\{envApplies\} \/>/g) ?? []).length).toBe(2);
  });
});
