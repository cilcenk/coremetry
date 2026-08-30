// Rota erişilebilirliği — UX denetimi F1 / Ö17 (v0.9.952).
//
// ORİJİNAL BELİRTİ: /deploys UI'dan TAMAMEN ulaşılmazdı — sidebar'da
// yok, ⌘K'da yok, grafiklerdeki ▼ deploy işaretleri sayfaya link
// vermiyor. Deploy geçmişine tek yol adres çubuğuydu. /clusters ve
// /watchers sidebar'da GÖRÜNÜR ama palette'te aranamıyordu; klavyeyle
// gezinen operatör için bu "yok" demekle aynı.
//
// KATALOĞUN KENDİ YORUMU "güncel rotalara hizalandı" diyordu (v0.8.525)
// — yani iddia ile gerçek ayrışmıştı ve hiçbir şey bunu söylemiyordu.
// Bu test o ayrışmayı KALICI olarak imkânsız kılıyor: yeni bir sayfa
// rotası doğduğunda ya bir keşif yüzeyine bağlanır ya da NEDEN
// bağlanmadığı buraya yazılır. Sessiz üçüncü hâl yok.

import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

const SRC = join(__dirname, '..');
const read = (rel: string) => readFileSync(join(SRC, rel), 'utf8');

const routes = [...new Set(
  Array.from(read('App.tsx').matchAll(/<Route\s+path="(\/[^"]*)"/g), m => m[1]),
)].sort();

const paletteTargets = new Set(
  Array.from(read('components/CommandPalette.tsx').matchAll(/to: '([^']+)'/g), m => m[1]),
);

// Sidebar girdileri `href:` alanında tanımlı (nav modeli), `to=` değil.
const sidebarTargets = new Set([
  ...Array.from(read('components/Sidebar.tsx').matchAll(/to=['"](\/[^'"]+)['"]/g), m => m[1]),
  ...Array.from(read('components/Sidebar.tsx').matchAll(/href: '(\/[^']*)'/g), m => m[1]),
]);

// Keşif yüzeyine BİLEREK bağlanmayan rotalar, gerekçeleriyle.
//
// İki meşru sınıf var:
//   • DETAY — bir listeden tıklanarak açılır ve parametresiz anlamsızdır
//     (/service?name=…, /trace?id=…). Palette'e koymak parametresiz,
//     dolayısıyla boş bir sayfa vaat etmek olurdu.
//   • ÇERÇEVE — yönlendirme, giriş, herkese açık paylaşım, profil.
const INTENTIONALLY_UNLISTED: Record<string, string> = {
  '/': 'kök yönlendirme',
  '/dashboard': 'detay — /dashboards listesinden açılır',
  '/database': 'detay — /databases satırından açılır',
  '/endpoint': 'detay — /endpoints satırından açılır',
  '/entity': 'detay — /pod entity paneli zincirinden (node/namespace/workload) açılır (v0.10.135)',
  '/deployment-report': 'emekli rota, yönlendirme (→ /rollouts, v0.10.201)',
  '/deploys': 'emekli rota, yönlendirme (→ /rollouts, v0.10.209)',
  '/errors': 'emekli rota, yönlendirme',
  '/exceptions': 'detay — /problems Exceptions sekmesinden açılır',
  '/incident': 'detay — /incidents listesinden açılır',
  '/login': 'çerçeve — oturum açma',
  '/pod': 'detay — /clusters ya da servis Pods sekmesinden açılır',
  '/profile': 'çerçeve — kullanıcı menüsünden açılır',
  '/public/trace': 'çerçeve — kimliksiz paylaşım linki',
  '/runbook': 'detay — /runbooks listesinden açılır',
  '/runbook-exec': 'detay — runbook çalıştırma görünümü',
  '/service': 'detay — /services satırından açılır',
  '/service/backtrace': 'detay — servis sayfasından açılır',
  '/status': 'emekli rota, yönlendirme',
  '/topology': 'emekli rota → /service-map',
  '/trace': 'detay — trace listesinden / ⌘K trace-id ile açılır',
  '/databases/slow-queries': 'alt-sayfa — /databases üstündeki "Slow queries" linkinden ve DatabaseDetail’den açılır',
};

describe('rota erişilebilirliği (v0.9.952)', () => {
  it('her rota ya keşfedilebilir ya da gerekçesiyle listelenmiş', () => {
    const orphans = routes.filter(r => {
      if (r.includes(':')) return false;                       // parametreli alt-rota
      if (paletteTargets.has(r)) return false;
      if ([...sidebarTargets].some(s => r === s || r.startsWith(s + '/'))) return false;
      return !(r in INTENTIONALLY_UNLISTED);
    });
    expect(orphans, [
      'Bu rotalar UI’dan ulaşılamıyor: ne sidebar’da ne ⌘K’da.',
      'Ya CommandPalette PAGES’e ekleyin, ya da INTENTIONALLY_UNLISTED’a',
      'GEREKÇESİYLE yazın. /deploys tam olarak böyle kaybolmuştu (Ö17).',
    ].join('\n')).toEqual([]);
  });

  it('gerekçe listesi BAYATLAMAZ — silinen rota listede kalamaz', () => {
    const stale = Object.keys(INTENTIONALLY_UNLISTED).filter(r => !routes.includes(r));
    expect(stale, 'Bu rotalar artık App.tsx’te yok; gerekçe satırlarını da silin.').toEqual([]);
  });

  it('Ö17’nin kalan iki rotası palette’te (/deploys v0.10.209’da emekli)', () => {
    for (const r of ['/clusters', '/watchers']) {
      expect(paletteTargets.has(r), `${r} ⌘K kataloğunda yok`).toBe(true);
    }
  });

  it('palette hedefleri GERÇEK rotalara işaret eder — ölü hedef yok', () => {
    // v0.8.525 katalogda ölü hedefler bulmuştu (Home /, Topology, Errors,
    // Status). Aynı çürüme sessizce geri gelebilir.
    const dead = [...paletteTargets]
      .filter(t => t.startsWith('/'))
      .map(t => t.split('?')[0])
      .filter(t => !routes.includes(t) && !routes.some(r => t.startsWith(r + '/')));
    expect(dead, '⌘K bu hedefleri vaat ediyor ama rota yok — 404’e götürür.').toEqual([]);
  });
});
