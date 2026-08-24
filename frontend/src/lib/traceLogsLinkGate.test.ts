import { describe, it, expect } from 'vitest';
import { readdirSync, statSync, readFileSync } from 'node:fs';
import { join, resolve } from 'node:path';

// traceLogsLinkGate — v0.9.1353. The source scan that keeps `/trace` and
// `/logs` links inside their producers (lib/traceHref.ts, lib/logsUrl.ts).
//
// Why a gate at all. tsc cannot see a route inside a template literal, eslint
// has no rule for it, and `make audit` does not read the frontend. So the only
// thing standing between the repo and a 39th hand-written `/trace?id=` is a
// test that reads the source.
//
// ── LESSON 1: A GATE THAT GREPS ONE SPELLING EXEMPTS THE TWIN ────────────
//
// v0.9.1320 found FOUR spellings of the same back link ('←', 'Back to',
// <ArrowLeft/>, '›') because the gate that was supposed to cover them looked
// for one. The same trap is live here: `/trace?id=` is only ONE way to write
// this route. So the detector below matches a LIST of spellings, and
// `SPELLINGS` is covered by its own test against synthetic samples — if a new
// way to spell the route is added to the codebase, the honest fix is a new
// entry here, and the meta-test makes that entry's absence visible rather
// than silently green.
//
// ── LESSON 2: ABSENCE, NOT PRESENCE ─────────────────────────────────────
//
// v0.9.1334 shipped a gate that asserted a producer was CALLED, which
// `return null && producer(...)` satisfies while doing nothing. This gate
// asserts the opposite and stronger property: the raw route is not SPELLED
// outside its producer. There is no way to satisfy it while still emitting a
// hand-built URL, because emitting one requires spelling it.
//
// ── LESSON 3: EXEMPTIONS GO STALE ───────────────────────────────────────
//
// Every ALLOWED entry is keyed to a REASON, never a line number (v0.9.887:
// a line-keyed exemption slides the moment someone adds an import), and every
// entry gets a staleness check (v0.9.1339): an exemption whose file is now
// clean must be DELETED. The list shrinks; it never grows quietly.
const SRC = resolve(__dirname, '..');

/**
 * Every way this repo can spell a link to these two routes.
 *
 * `/trace?` deliberately does NOT match `/traces?` — the plural is a
 * different route with its own producer (tracesPivotHref). The bare-path
 * forms are pinned even though nothing uses them today: a gate that only
 * covers the spelling currently in fashion is the v0.9.1320 failure.
 */
const SPELLINGS: { route: 'trace' | 'logs'; re: RegExp; what: string }[] = [
  { route: 'trace', re: /\/trace\?/, what: 'query-string form (/trace?…)' },
  { route: 'trace', re: /pathname:\s*['"`]\/trace['"`]/, what: 'router object form ({ pathname: "/trace" })' },
  { route: 'trace', re: /['"`]\/trace['"`]\s*\+/, what: 'string concatenation ("/trace" + …)' },
  { route: 'trace', re: /`\/trace\$\{/, what: 'template interpolation (`/trace${…}`)' },
  { route: 'logs', re: /\/logs\?/, what: 'query-string form (/logs?…)' },
  { route: 'logs', re: /pathname:\s*['"`]\/logs['"`]/, what: 'router object form ({ pathname: "/logs" })' },
  { route: 'logs', re: /['"`]\/logs['"`]\s*\+/, what: 'string concatenation ("/logs" + …)' },
  { route: 'logs', re: /`\/logs\$\{/, what: 'template interpolation (`/logs${…}`)' },
];

const ALLOWED: { file: string; why: string }[] = [
  {
    file: 'lib/traceHref.ts',
    why: 'The producer itself — it is the one place allowed to spell /trace.',
  },
  {
    file: 'lib/logsUrl.ts',
    why: 'The producers themselves: logsHref and buildDocPermalink. The doc '
      + 'permalink (?doc=<tsNs>.<id>, v0.9.1248) is a SEPARATE contract that '
      + 'resolves one document by its own timestamp through /api/logs/context, '
      + 'so a window is genuinely meaningless there — it is not a logsHref '
      + 'call site that was missed.',
  },
  {
    file: 'pages/settings/TempoTab.tsx',
    why: 'Prose, not a link: a <code> block showing the operator what a '
      + 'Coremetry trace URL looks like when configuring Tempo. Converting it '
      + 'would mean generating documentation text through a link builder.',
  },
];

function sourceFiles(dir: string, rel = ''): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    const relPath = rel ? `${rel}/${entry}` : entry;
    if (statSync(full).isDirectory()) { out.push(...sourceFiles(full, relPath)); continue; }
    if (!/\.tsx?$/.test(entry) || /\.test\.tsx?$/.test(entry)) continue;
    out.push(relPath);
  }
  return out;
}

/**
 * Strip comments line-wise, then test every spelling.
 *
 * The comment rule is lifted verbatim from serviceHref.test.ts (v0.9.967),
 * including its two hard-won details: block-comment CONTINUATION lines count
 * as comments even when they are not star-prefixed, and entering a block
 * requires the TRIMMED line to START with the opener — a `/*` appearing
 * mid-line is inside a string or regex, and honouring it would let a real
 * call site hide behind a literal.
 *
 * This matters more here than it did there: half the remaining `/trace?id=`
 * occurrences in this repo are comments EXPLAINING the bug history, and a
 * scanner that reads them as call sites can only be "fixed" by deleting the
 * explanations.
 */
export function liveHits(): Map<string, string[]> {
  const hits = new Map<string, string[]>();
  for (const rel of sourceFiles(SRC)) {
    const text = readFileSync(join(SRC, rel), 'utf8');
    if (!text.includes('/trace') && !text.includes('/logs')) continue;
    const found = new Set<string>();
    let inBlock = false;
    for (const l of text.split('\n')) {
      const s = l.trim();
      const opensBlock = !inBlock && (s.startsWith('/*') || s.startsWith('{/*'));
      const commented = inBlock || opensBlock || s.startsWith('//') || s.startsWith('*');
      if (!commented) {
        for (const sp of SPELLINGS) if (sp.re.test(l)) found.add(sp.what);
      }
      if (opensBlock) inBlock = true;
      if (inBlock && s.includes('*/')) inBlock = false;
    }
    if (found.size) hits.set(rel, [...found].sort());
  }
  return hits;
}

const ALLOWED_FILES = new Set(ALLOWED.map(a => a.file));

// Files converted in v0.9.1348-1352. Re-growing a hand-rolled link in any of
// them is a REGRESSION, and it cannot be "fixed" by adding an ALLOWED entry.
const CONVERTED = [
  // /logs — v0.9.1348-1349
  'components/CorrelationContextDrawer.tsx',
  'components/InboxTriageDrawer.tsx',
  'components/ServiceMapNodeDrawer.tsx',
  'components/TracePeekDrawer.tsx',
  'components/SpanDetail.tsx',
  'components/ai/ServiceChartsExplainBody.tsx',
  'features/anomalies/AnomalyDetailDrawer.tsx',
  'features/anomalies/ProblemDetail.tsx',
  'features/anomalies/streams.tsx',
  'pages/Alerts.tsx',
  'pages/service/ServiceSignalTabs.tsx',
  // /trace — v0.9.1350-1352
  'pages/Trace.tsx',
  'pages/Traces.tsx',
  'pages/TraceCompare.tsx',
  'pages/explore/TracesResult.tsx',
  'pages/explore/RepeatsResult.tsx',
  'components/traces/ShapesView.tsx',
  'components/traces/MiniWaterfall.tsx',
  'pages/Endpoints.tsx',
  'pages/endpoints/detailSections.tsx',
  'pages/slowqueries/StmtDetailDrawer.tsx',
  'features/dependencies/DetailDrawer.tsx',
  'components/HeatmapCellExemplars.tsx',
  'components/AIAnalysisPanel.tsx',
  'components/RootCausePanel.tsx',
  'components/RootCauseRibbon.tsx',
  'components/CommandPalette.tsx',
  'components/LogTable.tsx',
  'components/ai/ChatBubble.tsx',
  'features/anomalies/AnomaliesPage.tsx',
];

describe('kaynak taraması — el-yapımı /trace ve /logs linki doğmasın', () => {
  it('yeni bir el-yapımı site DOĞMAZ', () => {
    const unexpected = [...liveHits().entries()]
      .filter(([f]) => !ALLOWED_FILES.has(f))
      .map(([f, spellings]) => `${f} → ${spellings.join(', ')}`)
      .sort();
    expect(unexpected, [
      'Bu dosyalar /trace veya /logs yolunu ELLE heceliyor.',
      '  /trace → traceHref(id, { pageRange, span, tab })  — lib/traceHref.ts',
      '  /logs  → logsHref({ window, service, q, … })      — lib/logsUrl.ts',
      'Pencere logsHref\'te ZORUNLU: /logs pencereyi yalnız ?range=\'ten',
      'okur, düşen pencere var olan loglar için "log yok" gösterir',
      '(v0.8.484 / v0.9.853 / v0.9.862).',
    ].join('\n')).toEqual([]);
  });

  it('geçirilen siteler el-yapımına DÖNMEZ', () => {
    const hits = liveHits();
    for (const f of CONVERTED) {
      expect(hits.has(f), `${f} yeniden el-yapımı link yazıyor — regresyon`).toBe(false);
      expect(ALLOWED_FILES.has(f), `${f} muafiyet listesine eklenerek "düzeltilmiş"`).toBe(false);
    }
  });

  // v0.9.1339 dersi: her muafiyet kendi bayatlık testini taşır. Gerekçesi
  // ortadan kalkmış bir muafiyet, yanlış sebeple yeşil bir kapıdır.
  it('muafiyet listesi BAYAT girdi taşımaz', () => {
    const hits = liveHits();
    const stale = ALLOWED.filter(a => !hits.has(a.file)).map(a => a.file).sort();
    expect(stale, 'Bu dosyalar artık temiz — ALLOWED\'dan sil (liste küçülür, büyümez).')
      .toEqual([]);
  });

  it('her muafiyetin gerekçesi YAZILI', () => {
    for (const a of ALLOWED) {
      expect(a.why.length, `${a.file} gerekçesiz muafiyet`).toBeGreaterThan(40);
    }
  });
});

// ── Kapının KENDİ yazım kapsaması ────────────────────────────────────────
//
// v0.9.1320'nin dersi doğrudan buraya: tek yazımı arayan kapı ikizi muaf
// tutar. Aşağıdaki testler dedektörün her yazımı gerçekten yakaladığını
// sentetik örneklerle kanıtlar — yani kapsama iddiası, kapının kendi
// testiyle ölçülüyor, yorumda verilen bir söz değil.
describe('kapının yazım kapsaması', () => {
  const matches = (line: string) => SPELLINGS.filter(s => s.re.test(line)).map(s => s.what);

  it('her bilinen yazım en az bir desenle yakalanır', () => {
    const samples: [string, string][] = [
      ['<Link to={`/trace?id=${t.traceId}`}>', 'query-string form (/trace?…)'],
      ["navigate('/trace?id=' + id)", 'query-string form (/trace?…)'],
      ["navigate({ pathname: '/trace', search })", 'router object form ({ pathname: "/trace" })'],
      ["const to = '/trace' + qs;", 'string concatenation ("/trace" + …)'],
      ['const to = `/trace${qs}`;', 'template interpolation (`/trace${…}`)'],
      ['<Link to={`/logs?service=${s}`}>', 'query-string form (/logs?…)'],
      ['navigate({ pathname: "/logs", search: p })', 'router object form ({ pathname: "/logs" })'],
      ['const to = "/logs" + qs;', 'string concatenation ("/logs" + …)'],
      ['const to = `/logs${qs}`;', 'template interpolation (`/logs${…}`)'],
    ];
    for (const [line, expected] of samples) {
      expect(matches(line), `yakalanmadı: ${line}`).toContain(expected);
    }
  });

  it('komşu rotaları YANLIŞ POZİTİF olarak yakalamaz', () => {
    // /traces, /trace-compare ve /explore kendi üreticilerine ait
    // (tracesPivotHref, repeatsExploreHref) — bu kapının konusu değiller.
    // Çoğul rotayı buraya çekmek, kapıyı sahibi olmadığı bir aileyi
    // yönetmeye zorlardı.
    for (const line of [
      '<Link to={tracesPivotHref({ window })}>',
      "navigate('/traces?service=' + s)",
      "navigate('/trace-compare?a=1&b=2')",
      "navigate('/explore?result=repeats')",
      "const label = 'open the trace list';",
    ]) {
      expect(matches(line), `yanlış pozitif: ${line}`).toEqual([]);
    }
  });

  it('yorum satırları site sayılmaz — açıklamayı silmek "düzeltme" olamaz', () => {
    // Depoda /trace?id= geçen satırların çoğu bug tarihçesini ANLATAN
    // yorumlar (LogTable, TracePeekDrawer, utils, types, api…). Onları
    // site sayan bir tarayıcı, ancak açıklamalar silinerek yeşile döner.
    const hits = liveHits();
    for (const f of [
      'lib/utils.ts', 'lib/api.ts', 'lib/types.ts',
      'components/ai/useChatThread.ts', 'pages/PublicTrace.tsx',
    ]) {
      expect(hits.has(f), `${f} yalnız yorumda geçiyor, site sayılmamalı`).toBe(false);
    }
  });
});
