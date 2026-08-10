// spaLinks — Dalga 5 / MT10 kapısı (v0.9.914)
//
// Ne çiviliyor: uygulama-içi bir rotaya giden `<a href="/…">` yok.
//
// Neden önemli: SPA içinde çıplak bir `<a href>` tam sayfa YENİDEN
// YÜKLEME yapar. Görünürde "çalışır" — hedef sayfa açılır — ama bedeli
// gerçek: React Query önbelleği komple atılır, açık SSE bağlantısı
// kopar, uygulama yeniden boot eder (auth çağrısı + settings hidrasyonu
// + rota chunk'ı yeniden indirilir), URL'de taşınan state kaybolur ve
// TTFI bütçesi (<1.5s) her tıkta yeniden ödenir. Kullanıcı bunu
// "Coremetry yavaş" diye yaşar, "bu link bozuk" diye değil — bu yüzden
// hiç bildirilmez ve bu yüzden kapı gerekir.
//
// tsc bunu göremez (`<a href>` tip olarak kusursuz), eslint'in
// react-router kuralı yok, `make audit` frontend'e bakmaz.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { resolve, join } from 'node:path';

const SRC = resolve(__dirname, '..');

function walk(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    if (e.name === 'node_modules') continue;
    const p = join(dir, e.name);
    if (e.isDirectory()) walk(p, out);
    else if (e.name.endsWith('.tsx')) out.push(p);
  }
  return out;
}

// Muafiyetler GEREKÇEYE göre anahtarlı (v0.9.887 dersi: satıra bağlı
// muafiyet dosyaya bir import eklenince kayar). Gerekçe ortadan
// kalkarsa girdi ÇIKARILMALI — bayat girdi = yanlış sebeple yeşil.
const ALLOWED: { file: string; why: string }[] = [
  {
    file: 'pages/AdminAudit.tsx',
    why: 'Sunucu indirmesi (/api/admin/audit/export) — SPA rotası DEĞİL. '
      + '<Link> burada yanlış olurdu: router bu yolu tanımaz.',
  },
  {
    file: 'components/ai/ChatBubble.tsx',
    why: 'Markdown render çıktısındaki HTML dizesi; gezinme data-nav="1" '
      + 'üzerinden delege ediliyor. JSX değil, dize — <Link> yazılamaz.',
  },
  {
    file: 'components/dashboard/PanelRenderer.tsx',
    why: 'Markdown panelindeki DIŞ bağlantılar (target=_blank). Panel '
      + 'gövdesindeki iç bağlantı v0.9.914\'te <Link>e çevrildi.',
  },
  {
    file: 'components/ProblemRunbookPanel.tsx',
    why: 'Elle yazılmış preventDefault+navigate çifti — davranış zaten '
      + 'doğru. Rapor bunu yanlış pozitif olarak işaretledi; dokunmak '
      + 'davranışı değiştirmez, yalnız diff üretir.',
  },
  {
    file: 'pages/RunbookExecution.tsx',
    why: 'ProblemRunbookPanel ile aynı: preventDefault+navigate.',
  },
  {
    file: 'pages/Runbooks.tsx',
    why: 'Aynı desen — satır başlığı hem <a> semantiği (⌘-tık yeni sekme) '
      + 'hem programatik gezinme istiyor.',
  },
  {
    file: 'components/traces/MiniWaterfall.tsx',
    why: 'Aynı desen; trace linki ⌘-tık desteği için gerçek <a> kalmalı.',
  },
];

describe('MT10 — SPA rotalarına çıplak <a href> yok', () => {
  it('tarama temiz', () => {
    // İmza parçalardan kuruluyor ki bu dosyanın KENDİ düzyazısı bir
    // ihlal sanılmasın (depoda yedi kez ısıran tuzak).
    const OPEN = '<' + 'a' + '\\b[^>]*';
    const HREF = 'href=[{"\'`]+/';
    const RE = new RegExp(OPEN + HREF);
    const bad: string[] = [];
    for (const file of walk(SRC)) {
      const rel = file.slice(SRC.length + 1);
      if (ALLOWED.some(a => a.file === rel)) continue;
      readFileSync(file, 'utf8').split('\n').forEach((line, i) => {
        if (/^\s*(\/\/|\*)/.test(line)) return;
        if (RE.test(line) && !/href=["'`]\/\//.test(line)) {
          bad.push(`${rel}:${i + 1} ${line.trim().slice(0, 90)}`);
        }
      });
    }
    expect(bad).toEqual([]);
  });

  it('muafiyet dosyalarının hepsi hâlâ var', () => {
    for (const a of ALLOWED) {
      expect(() => readFileSync(join(SRC, a.file), 'utf8'), `${a.file} yok — muafiyeti ÇIKAR`).not.toThrow();
    }
  });
});
