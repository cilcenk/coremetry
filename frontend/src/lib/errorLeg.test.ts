import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// errorLeg.test.ts — v0.9.867.
//
// Kaynak: tutarlılık denetimi (docs/audit/frontend-consistency-audit.md) MT1
// ve UX denetimi K6. Aynı sınıf iki denetimde birden çıktı ve iki dalgada
// kapatıldı: v0.9.858 (12 yüzey), v0.9.865-867 (13 site).
//
// SEMPTOM: okuma hatası BOŞ DURUM olarak sunuluyordu. /services "No services
// yet — point your OTLP exporter at the collector" basıyor, yani backend
// arızası INSTRUMENTATION eksikliği gibi görünüyordu; /problems exception
// listesi bomboş kalıyor, "exception yok, temiziz" diye okunuyordu;
// Settings→Maintenance aktif bakım penceresi varken "No maintenance windows"
// diyor, operatör deploy'a girip alarmları fan-out ettiriyordu.
//
// NEDEN BU TEST VAR: bu sınıfı yakalayan BAŞKA HİÇBİR KAPI YOK. Bozuk
// guard'lar tip-doğru olduğu için `tsc --noEmit` sessiz; `make audit`'te
// kural yok; jsdom testleri bu yüzeyleri render etmiyor. Denetimin kendi
// ifadesiyle "mevcut testlerin yakaladığı: HİÇBİRİ".
//
// NE ÇİVİLİYOR: dönüştürülen her yüzeyin bir HATA DALI taşıdığı. Kimse
// dosyayı yeniden yazarken üç durumu ikiye düşüremesin. Kasıtlı olarak
// dosya-seviyesi bir iddia — satır/biçim değil — ki normal düzenlemeler
// testi kırmasın, dalın SİLİNMESİ kırsın.
//
// NE ÇİVİLEMİYOR (bilinçli): `!x || x.length === 0` idiom'unun kendisi
// depoda 69 yerde geçiyor ve BÜYÜK ÇOĞUNLUĞU doğru — saf yardımcılarda
// (`!n.children || n.children.length === 0`) tri-state diye bir şey yok.
// İdiom'u küresel yasaklamak 60 satırlık bir izin listesi doğururdu ve
// izin listesi bayatladığı an test yanlış sebeple yeşil kalır. Bu yüzden
// yasak YALNIZ dönüştürülmüş dosyalarla sınırlı: orada değişken gerçekten
// tri-state ve idiom gerçekten hata.

const SRC = resolve(__dirname, '..');

// Bu sınıf için dönüştürülmüş yüzeyler. Yeni bir yüzey dönüştürüldüğünde
// BURAYA EKLENİR — liste dalganın hafızası.
const CONVERTED: Array<[file: string, release: string, what: string]> = [
  // ── v0.9.858 (UX denetimi K6) ────────────────────────────────────────────
  ['pages/Services.tsx',                     'v0.9.858', 'OTLP onboarding mesajı hata durumunda basılıyordu'],
  ['pages/Alerts.tsx',                       'v0.9.858', '"No alert rules" + "+ New rule" CTA'],
  ['pages/Slos.tsx',                         'v0.9.858', '"No SLOs defined"'],
  ['pages/Monitors.tsx',                     'v0.9.858', '"No monitors yet"'],
  ['pages/Metrics.tsx',                      'v0.9.858', 'metrik kataloğu "No metrics match"'],
  ['pages/Traces.tsx',                       'v0.9.858', 'aggregate: BOŞ EKRAN (spinner yok, mesaj yok)'],
  ['pages/Service.tsx',                      'v0.9.858', 'sayfanın sessizce soyulması'],
  ['pages/settings/ChannelsTab.tsx',         'v0.9.858', '"No channels yet"'],
  ['components/ServiceCharts.tsx',           'v0.9.858', 'RED grafikleri boş çiziliyordu ("trafik yok")'],
  ['features/anomalies/AnomaliesPage.tsx',   'v0.9.858', 'exception listesi BOŞ EKRAN'],
  ['features/anomalies/ProblemDetail.tsx',   'v0.9.858', 'occurrence + sample panelleri'],
  // ── v0.9.865 (tutarlılık denetimi MT1, mekanik) ──────────────────────────
  ['pages/Incidents.tsx',                    'v0.9.865', 'açık olay varken "No incidents"'],
  ['pages/Runbooks.tsx',                     'v0.9.865', '?? [] → boş katalog'],
  ['pages/Runbook.tsx',                      'v0.9.865', 'Executions + Audit sekmeleri'],
  ['pages/Users.tsx',                        'v0.9.865', '"No users yet / create the first user"'],
  ['pages/settings/MaintenanceTab.tsx',      'v0.9.865', 'aktif pencere varken "No maintenance windows"'],
  // v0.9.1366 — DOSYA TAŞINDI, SÖZLEŞME TAŞINMADI. Top statements bölümü
  // `pages/DatabaseDetail.tsx`ten `pages/databases/detailSections.tsx`e
  // çıkarıldı; hata dalı AYNEN gitti. Kapı dosya adına bağlı olduğu için
  // taşınma onu kırdı — muhafızın dilim adına bağlanma sınıfı. Girdi
  // SİLİNMİYOR (sözleşme yaşıyor), YENİ EVE yönlendiriliyor.
  ['pages/databases/detailSections.tsx',     'v0.9.865', 'Top statements: hata dalı HİÇ YOKTU (v0.9.1366\'da bu dosyaya taşındı)'],
  // ── v0.9.866 (tutarlılık denetimi MT1, özel) ─────────────────────────────
  ['pages/AdminAudit.tsx',                   'v0.9.866', 'yasak ≠ hatalı; null hiçbir dala girmiyordu'],
  ['pages/alerts/NoisyRulesPanel.tsx',       'v0.9.866', 'başlangıç null == hata null, sessiz gizlenme'],
  ['components/ServiceAttrsPanel.tsx',       'v0.9.866', 'panel tamamen yok oluyordu'],
  // ── v0.9.867 (tutarlılık denetimi MT1, Explore çifti) ────────────────────
  ['pages/explore/TracesResult.tsx',         'v0.9.867', 'sonuç alanı bomboş'],
  ['pages/explore/RepeatsResult.tsx',        'v0.9.867', 'sonuç alanı bomboş ("N+1 yok")'],
];

// Hata dalının imzası: yüzey <QueryError> ya da <QueryErrorInline> BASMALI.
//
// Kasıtlı olarak DAR. `=== null` veya `isError` aramak cazip ama işe yaramaz:
// ikisi de alakasız gerekçelerle her dosyada geçiyor, yani test hata dalı
// silinse bile yeşil kalırdı — v0.9.660'ın "yazılmış-ama-bağlanmamış kod"
// dersinin test sürümü. Bileşenin ADI ise yalnız bu sözleşme için var.
//
// 2026-08-10'da doğrulandı: dönüştürülmüş 22 dosyanın 22'si de bu imzayı
// taşıyor, yani daraltma hiçbir gerçek dalı dışarıda bırakmıyor.
const ERROR_LEG = /<QueryError(Inline)?\b/;

function read(rel: string): string {
  return readFileSync(resolve(SRC, rel), 'utf8');
}

describe('errorLeg — hata=boş maskesi (tutarlılık denetimi MT1 / UX denetimi K6)', () => {
  it.each(CONVERTED)('%s (%s) hata dalını koruyor — %s', (file, _release, _what) => {
    const src = read(file);
    expect(ERROR_LEG.test(src), `${file}: <QueryError>/<QueryErrorInline> yok. Okuma hatası BOŞ DURUM olarak sunuluyor olabilir — MT1/K6 sınıfının nüksü.`).toBe(true);
  });

  it('dönüştürülen yüzeylerde null-yutan guard geri gelmedi', () => {
    // `!x || x.length === 0` — `!x` null için de doğru olduğundan hata boş
    // dala düşer. Bu sınıfın KÖK YAZIM ŞEKLİ; yalnız dönüştürülmüş
    // dosyalarda yasak (gerekçe için dosya başlığına bkz).
    const swallow = /!\s*([A-Za-z_$][\w$]*)\s*(?:\|\||&&)?\s*\|\|\s*\1\s*\.length\s*===\s*0/;
    // İzin listesi — HER GİRDİ 2026-08-10'da tek tek koddan doğrulandı.
    // Kural: idiom yalnız TRI-STATE BİR OKUMA üzerinde hatadır. Buradaki
    // değişkenler okuma değil, istemci tarafında TÜRETİLMİŞ dizilerdir
    // (asla null olamazlar), dolayısıyla `!x` dalı ulaşılamaz.
    //
    // ANAHTAR `dosya:DEĞİŞKEN`, `dosya:satır` DEĞİL (v0.9.887'de düzeltildi).
    // Satır anahtarı bir kez ısırdı: Services.tsx'e TEK bir import satırı
    // eklenince 357/377 → 358/378 kaydı ve muafiyet bayatlayıp testi alakasız
    // bir dosyada kırmızıya çevirdi. Muafiyetin GEREKÇESİ zaten satırla değil
    // değişkenle ilgili ("`sorted` bir useMemo çıktısı, asla null olamaz"),
    // dolayısıyla doğru anahtar da o. Daralma değil, gerekçeye hizalama.
    const ALLOW = new Set([
      // `sorted` = filtrelenmiş+sıralanmış servis listesi (useMemo çıktısı).
      // Okuma değil; tri-state'i `svcs` taşıyor ve onun hata dalı ayrı.
      'pages/Services.tsx:sorted',
    ]);
    const offenders: string[] = [];
    for (const [file] of CONVERTED) {
      const src = read(file);
      src.split('\n').forEach((line, i) => {
        const code = line.split('//')[0];
        const m = swallow.exec(code);
        if (m && !ALLOW.has(`${file}:${m[1]}`)) offenders.push(`${file}:${i + 1}: ${line.trim()}`);
      });
    }
    expect(offenders, `null-yutan guard geri geldi:\n${offenders.join('\n')}`).toEqual([]);
  });

  it('readState sözleşmesi elle yeniden yazılmadı', () => {
    // readState'in varlık gerekçesi guard'ı elle yazmanın hatayı mümkün
    // kılmasıydı. Kopyası doğarsa iki sözleşme olur ve biri bayatlar.
    // Yorumları AT: readState.ts'in başlığı bozuk guard'ı ÖRNEK olarak
    // gösteriyor, ham metinde sıralama iddiası ona takılır.
    const src = read('lib/readState.ts')
      .replace(/\/\*[\s\S]*?\*\//g, '')
      .split('\n').map(l => l.replace(/\/\/.*$/, '')).join('\n');
    expect(src).toMatch(/if\s*\(v\s*===\s*null\)\s*return\s*'error'/);
    // null testi HER ZAMAN length/truthiness testinden ÖNCE gelmeli —
    // her bulunan vakada guard önce `.length`e uzanıp null'ı boşa düşürüyordu.
    expect(src.indexOf('=== null')).toBeLessThan(src.indexOf('.length'));
  });
});
