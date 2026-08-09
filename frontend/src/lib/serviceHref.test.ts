import { describe, it, expect } from 'vitest';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { serviceHref, inboxItemWindow } from './serviceHref';
import { decodeRange } from './urlState';

// v0.9.860 — UX denetimi K1 + §4.1 sözleşmesi.
//
// Pencere URL'de KANONİK ve `custom:` pencere YALNIZ URL'de yaşıyor (v0.8.409
// custom'ı sticky localStorage kanalından bilinçli çıkardı). Dolayısıyla
// `range` düşüren bir link:
//   • relative preset'te GÖRÜNMEZ şekilde çalışır (sticky yeniden doldurur),
//   • custom pencerede sessizce pencereyi KAYBEDER.
//
// Yaşanan: gece 03:14 alarmının detayından "Blast radius" servis pill'ine
// tıklayan operatör servis sayfasını sticky "şimdi" penceresiyle görüyordu —
// alarm anının izi yok, "sorun geçmiş" yanılgısı. Pencere AYNI bileşende
// hesaplanıp logs/traces linklerine konuyor, servis linkine konmuyordu.

const params = (href: string) => new URLSearchParams(href.slice(href.indexOf('?') + 1).split('#')[0]);

describe('serviceHref', () => {
  it('adı her zaman yazar ve encode eder', () => {
    expect(params(serviceHref('checkout')).get('name')).toBe('checkout');
    expect(params(serviceHref('a b&c=1')).get('name')).toBe('a b&c=1');
  });

  it('pencereyi taşır — preset', () => {
    expect(params(serviceHref('checkout', { range: { preset: '6h' } })).get('range')).toBe('6h');
  });

  it('pencereyi taşır — mutlak ns olay penceresi (ns→ms)', () => {
    const p = params(serviceHref('checkout', {
      range: { fromNs: 1_700_000_000_000_000_000, toNs: 1_700_000_060_000_000_000 },
    }));
    expect(decodeRange(p.get('range'), { preset: '30m' }))
      .toEqual({ preset: 'custom', fromMs: 1_700_000_000_000, toMs: 1_700_000_060_000 });
  });

  it('ns→ms yuvarlaması pencereyi DARALTMAZ', () => {
    // Kırpılmış bir `to` en yeni kovayı düşürür — olay penceresinde operatörün
    // görmeye geldiği kısım tam orasıdır.
    const p = params(serviceHref('s', { range: { fromNs: 1_500_500, toNs: 2_500_500 } }));
    expect(p.get('range')).toBe('custom:1-3');
  });

  it('decodeRange\'in REDDEDECEĞİ pencereyi yazmaz', () => {
    // Yazılırsa adres çubuğu kendinden emin bir `custom:` gösterir ama sayfa
    // sessizce sticky pencereyi çizer — en zor fark edilen yanlış.
    for (const w of [
      { fromNs: -1e9, toNs: 5e9 },      // epoch öncesi (lead-in taşması)
      { fromNs: 5e9, toNs: 5e9 },       // sıfır genişlik
      { fromNs: 6e9, toNs: 5e9 },       // ters
    ]) {
      expect(params(serviceHref('s', { range: w })).has('range'), JSON.stringify(w)).toBe(false);
    }
  });

  it('zaten kodlanmış range dizgesi aynen geçer', () => {
    expect(params(serviceHref('s', { range: 'custom:100-200' })).get('range')).toBe('custom:100-200');
    expect(params(serviceHref('s', { range: '24h' })).get('range')).toBe('24h');
  });

  it('range yoksa param YAZILMAZ — hedef kendi çözer', () => {
    // Katalog satırları (Hosts/External/Clusters) için doğru davranış budur;
    // boş bir `range=` hedefi fallback'e düşürürdü.
    for (const r of [undefined, null, '']) {
      expect(params(serviceHref('s', { range: r })).has('range')).toBe(false);
    }
  });

  it('env, tab, ek paramlar ve fragment', () => {
    const href = serviceHref('checkout', {
      env: 'uat', tab: 'pods', params: { jpod: 'checkout-7d9' }, hash: 'deploys',
    });
    const p = params(href);
    expect(p.get('env')).toBe('uat');
    expect(p.get('tab')).toBe('pods');
    expect(p.get('jpod')).toBe('checkout-7d9');
    expect(href.endsWith('#deploys')).toBe(true);
  });

  it('boş opsiyonel alanlar URL\'i kirletmez', () => {
    const p = params(serviceHref('s', { env: '', tab: null, params: { jpod: '', op: undefined } }));
    expect([...p.keys()]).toEqual(['name']);
  });
});

describe('inboxItemWindow', () => {
  const NS = 1e9;
  it('başlangıçtan önce lead-in, son görülmeden sonra trail-out bırakır', () => {
    const w = inboxItemWindow({ startedAt: 1000 * NS, lastSeen: 2000 * NS })!;
    expect(w.fromNs).toBe(1000 * NS - 30 * 60 * NS);
    expect(w.toNs).toBe(2000 * NS + 10 * 60 * NS);
  });

  it('lastSeen yok/eski ise pencere TERS dönmez', () => {
    // Açık bir öğede lastSeen === startedAt gelir; bayat satırda daha eski
    // bile olabilir. Ters bir pencere decodeRange'i fallback'e düşürürdü.
    for (const lastSeen of [undefined, 0, 1000 * NS, 500 * NS]) {
      const w = inboxItemWindow({ startedAt: 1000 * NS, lastSeen })!;
      expect(w.toNs, `lastSeen=${lastSeen}`).toBeGreaterThan(w.fromNs);
    }
  });

  it('kullanılamaz satırda undefined — epoch 0 civarına pencere ÇAKMAZ', () => {
    for (const item of [undefined, null, {}, { startedAt: 0 }, { startedAt: -5 }]) {
      expect(inboxItemWindow(item as never)).toBeUndefined();
    }
  });

  it('serviceHref ile birleşince gerçek bir custom pencere üretir', () => {
    // Gerçekçi Unix ns damgası — lead-in epoch'un altına taşmasın.
    const t0 = 1_700_000_000 * NS;
    const href = serviceHref('checkout', { range: inboxItemWindow({ startedAt: t0, lastSeen: t0 + 60 * NS }) });
    const r = decodeRange(params(href).get('range'), { preset: '30m' });
    expect(r.preset).toBe('custom');
  });
});

// ─────────────────────────────────────────────────────────────────────────────
// Kaynak taraması — §4.1 kural 3.
//
// Raporun İ31 bulgusu şuydu: v0.9.839/840 iki YENİ el-yapımı `/service?name=`
// sitesi doğurdu, çünkü kuralı hatırlatan hiçbir şey yoktu. Bu test o boşluğu
// kapatır: yeni bir dosya el-yapımı link yazarsa CI kırılır.
//
// Kalan ~33 site bu dalgada DEĞİL (bilinçli: mekanik kuyruk, ayrı dalga).
// Allowlist onları dondurur — küçülebilir, BÜYÜYEMEZ.
// ─────────────────────────────────────────────────────────────────────────────

// Dosyalar `src/` köküne göre. Bir dosyayı serviceHref'e geçirdiğinde buradan
// SİL — testin ikinci yarısı bayat girdiyi yakalar, böylece liste gerçeği
// yansıtmayı sürdürür.
const HANDROLLED_ALLOWLIST = new Set([
  'components/CommandPalette.tsx',
  'components/DependenciesTable.tsx',
  'components/ServiceNeighbors.tsx',
  'components/SpanDetail.tsx',
  'components/dashboard/PanelRenderer.tsx',
  'components/topology/FocusedNeighborhood.tsx',
  'features/anomalies/AnomaliesPage.tsx',
  'features/anomalies/ProblemsSection.tsx',
  'features/anomalies/streams.tsx',
  'features/dependencies/DetailDrawer.tsx',
  'features/dependencies/panels/shared.tsx',
  'pages/Clusters.tsx',
  'pages/DatabaseDetail.tsx',
  'pages/Deploys.tsx',
  'pages/EndpointDetail.tsx',
  'pages/Endpoints.tsx',
  'pages/Events.tsx',
  'pages/External.tsx',
  'pages/Hosts.tsx',
  'pages/Incident.tsx',
  'pages/Pod.tsx',
  'pages/ServiceBacktrace.tsx',
  'pages/ServiceMap.tsx',
  'pages/Services.tsx',
  'pages/SlowQueries.tsx',
  'pages/endpoints/detailSections.tsx',
  'pages/service/EndpointPeekDrawer.tsx',
  'pages/service/OperationsTable.tsx',
  'pages/service/OverviewTables.tsx',
  'pages/service/ServiceClusterBreakdown.tsx',
  'pages/service/ServiceFlow.tsx',
  'pages/service/ServiceSignalTabs.tsx',
  'pages/slowqueries/StmtDetailDrawer.tsx',
]);

// Bu dalgada geçirilen olay-bağlamlı siteler. Bir daha el-yapımı link
// yazmaları REGRESYONDUR — allowlist'e eklenerek "düzeltilemez".
const CONVERTED = [
  'features/anomalies/ProblemDetail.tsx',
  'features/anomalies/AnomalyDetailDrawer.tsx',
  'components/InboxTriageDrawer.tsx',
  'components/RootCausePanel.tsx',
  'components/RootCauseRibbon.tsx',
  'pages/Inbox.tsx',
];

const SRC = join(__dirname, '..');

function sourceFiles(dir: string, rel = ''): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    const relPath = rel ? `${rel}/${entry}` : entry;
    if (statSync(full).isDirectory()) { out.push(...sourceFiles(full, relPath)); continue; }
    if (!/\.tsx?$/.test(entry) || /\.test\.tsx?$/.test(entry)) continue;
    if (relPath === 'lib/serviceHref.ts') continue; // the producer itself
    out.push(relPath);
  }
  return out;
}

/** Files with a hand-rolled `/service?name=` outside comments. */
function handRolledSites(): Set<string> {
  const hits = new Set<string>();
  for (const rel of sourceFiles(SRC)) {
    const text = readFileSync(join(SRC, rel), 'utf8');
    if (!text.includes('/service?name=')) continue;
    const live = text.split('\n').some(l => {
      const s = l.trim();
      return l.includes('/service?name=') && !s.startsWith('//') && !s.startsWith('*');
    });
    if (live) hits.add(rel);
  }
  return hits;
}

describe('kaynak taraması — el-yapımı /service?name= yayılmasın', () => {
  it('yeni bir el-yapımı site DOĞMAZ', () => {
    const unexpected = [...handRolledSites()].filter(f => !HANDROLLED_ALLOWLIST.has(f)).sort();
    expect(unexpected, [
      'Bu dosyalar el-yapımı `/service?name=` string\'i yazıyor.',
      'serviceHref(name, { range, env, tab }) kullan — pencereyi düşüren link',
      'custom pencerede sessizce yanlış zamanı gösterir (UX denetimi K1/İ31).',
    ].join('\n')).toEqual([]);
  });

  it('bu dalgada geçirilen 6 olay-bağlamlı site el-yapımına DÖNMEZ', () => {
    const hand = handRolledSites();
    for (const f of CONVERTED) {
      expect(hand.has(f), `${f} yeniden el-yapımı link yazıyor — regresyon`).toBe(false);
      expect(HANDROLLED_ALLOWLIST.has(f), `${f} allowlist'e eklenerek "düzeltilmiş"`).toBe(false);
    }
  });

  it('allowlist BAYAT girdi taşımaz — geçirilen dosya listeden silinsin', () => {
    const hand = handRolledSites();
    const stale = [...HANDROLLED_ALLOWLIST].filter(f => !hand.has(f)).sort();
    expect(stale, 'Bu dosyalar artık temiz — HANDROLLED_ALLOWLIST\'ten sil (liste küçülür, büyümez).')
      .toEqual([]);
  });
});
