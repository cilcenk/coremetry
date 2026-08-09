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
  .filter(f => f.text.includes('<Topbar'));

/** Pages that CLAIM to apply the env filter. `envApplies={false}` is a claim of the opposite. */
const claimsApplies = (text: string) => /envApplies(?!=\{false\})/.test(text);
/** Pages that read the global env at all. */
const readsEnv = (text: string) => text.includes('useUrlEnv');

// Env'i kendi SORGULARINA geçiren sayfalar — koddan doğrulandı 2026-08-09.
const APPLIES = new Set([
  'features/anomalies/ProblemsSection.tsx',
  'pages/Databases.tsx',
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

  it('Exceptions listesi bayrağı AÇIKÇA reddeder (K8)', () => {
    // Sayfa picker'ı açıkça istiyor (showEnv) ama liste sorgusu env geçirmiyor;
    // sidebar rozeti ise env-scoped. Rozet "3" derken sayfa 40 satır gösteriyordu.
    // Backend `/api/exception-groups`'a env parametresi kazanana dek AÇIK
    // reddetme, sessiz varsayılandan daha iyi belgedir.
    const page = topbarPages.find(f => f.rel === 'features/anomalies/AnomaliesPage.tsx');
    expect(page).toBeTruthy();
    expect(page!.text.includes('envApplies={false}')).toBe(true);
    expect(claimsApplies(page!.text)).toBe(false);
  });

  it('picker atıl hâlde GİZLENMEZ, devre dışı kalır', () => {
    // Gizlemek, operatörün sticky bir env seçimi olduğunu ve burada yok
    // sayıldığını göremeyeceği anlamına gelirdi — sessizce yanlış olan tam
    // da buydu. ✕ (temizle) çalışır kalır.
    const picker = readFileSync(join(SRC, 'components/EnvPicker.tsx'), 'utf8');
    expect(picker).toMatch(/disabled=\{!applies\}/);
    expect(picker).toMatch(/aria-disabled=\{!applies\}/);
    // Atıl hâlde erken return YOK: bileşen yine render eder.
    expect(picker).not.toMatch(/if\s*\(!applies\)\s*return null/);
  });

  it('Topbar varsayılanı opt-in — işaretlenmemiş sayfa "uygulanmıyor" der', () => {
    const topbar = readFileSync(join(SRC, 'components/Topbar.tsx'), 'utf8');
    expect(topbar).toMatch(/envApplies\?: boolean/);
    // Picker'a HER İKİ dalda da geçirilir (range'li ve showEnv-only).
    expect((topbar.match(/<EnvPicker applies=\{envApplies\} \/>/g) ?? []).length).toBe(2);
  });
});
