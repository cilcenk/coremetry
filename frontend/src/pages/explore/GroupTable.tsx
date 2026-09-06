import { useMemo } from 'react';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/ui/DataTable';
import { Button } from '@/components/ui/Button';
import { IconButton } from '@/components/ui/IconButton';
import { panelsToCSV } from './exploreCsv';
import type { DataTableColumn } from '@/lib/dataTable';
import { fmtSmart, seriesColor } from '@/lib/chartFmt';
import { seriesStats, isAdditiveUnit } from '@/lib/chart/legendStats';
import { fmtNum } from '@/lib/utils';
import { useCursorTime, valueAtCursor } from '@/lib/chart/cursorBus';
import type { PanelData, PivotAffordance } from './PanelStack';
import type { PivotMode } from './pivotQuery';
import type { SourceTarget } from './sourceHref';
import type { PivotPair } from './model';

// GroupTable (explore-v2 Phase 2/3) — ONE combined per-series breakdown across
// every panel (SUMMARY_COLS clone; col0 = letter badge + group label). Row
// hover focuses that series on its panel; click toggles visibility (eye).
// Phase 3: the @imleç column reads the cursorBus and shows each series' value
// under the synced crosshair (blank when no cursor). Sort + widths persist via
// the shared primitive (storageKey).

export interface GroupRow {
  rowKey: string;            // `${letter}:${label}` — the hidden/focus key
  letter: string;
  isFormula: boolean;
  label: string;
  unit: string;
  // color — the series' chart colour. BOTH render engines derive a line's
  // colour from seriesColor(label) (the legacy TimeSeriesPanel through
  // PanelData.series[].color, the v2 CorePanel through seriesRoleColor's
  // 'data' role), so a row can be tinted to match its line without the two
  // ever drifting.
  color: string;
  last: number;
  min: number;
  avg: number;
  max: number;
  // sum — MEANINGFUL only when `additive`; a p95-latency column summed
  // across pods is a number with no referent.
  sum: number;
  additive: boolean;
  buckets: number;
  points: { time: number; value: number | null }[];  // for the @imleç lookup
  // v0.9.848 — satırdan pivot: (splitBy anahtarı, değer) çiftleri + varsa
  // "neden olmaz" cümlesi. Yokluğu = bu satırdan daraltılacak bir boyut yok
  // (splitsiz panel, formül satırı).
  pivot?: PivotAffordance;
  // v0.9.824 — önceki döneme göre yüzde değişim. null = hesaplanamaz
  // ("—" basılır): karşılaştırma kapalı, bu serinin önceki dönemde
  // karşılığı yok, ya da önceki değer SIFIR. Sıfırdan yüzde değişim
  // tanımsızdır (0'dan 5'e "sonsuz artış") — uydurulmuş bir sayı yerine
  // boşluk basmak dürüst olan.
  deltaPct: number | null;
}

// deltaPct — güncel vs önceki, BİRİM AİLESİNE göre. SAF.
//
// Toplanabilir birimde (istek/sn, adet, bayt) karşılaştırılan şey
// TOPLAMDIR: pencerede kaç istek geldi. Toplanamaz birimde (ms, %) toplam
// bir sayı değil bir sayı yığınıdır — orada karşılaştırılan ORTALAMADIR.
// Aynı ayrım GroupTable'ın "Toplam" sütununu da yönetiyor (v0.9.806,
// isAdditiveUnit); iki yerde iki farklı aile tanımı olsaydı satırın Δ'sı
// kendi Toplam hücresiyle çelişirdi.
//
// Payda MUTLAK DEĞER: önceki -20'den bugün -10'a çıkmak %+50'dir, %-50
// değil. (Explore'da negatif seri nadirdir ama bir formül/metrik pekâlâ
// üretir ve işaretin sessizce dönmesi en kötü sınıf hatadır.)
export function deltaPct(
  cur: { sum: number; mean: number } | null,
  prev: { sum: number; mean: number } | null,
  additive: boolean,
): number | null {
  if (!cur || !prev) return null;
  const c = additive ? cur.sum : cur.mean;
  const p = additive ? prev.sum : prev.mean;
  if (!isFinite(c) || !isFinite(p) || p === 0) return null;
  return (c - p) / Math.abs(p) * 100;
}

// @imleç is intentionally NOT sortable (no sortValue): its value changes every
// cursor frame, so making it a sort key would force a re-sort per mousemove.
// Omitting sortValue keeps the column resizable but inert — rows stay stable.
//
// v0.9.806 — Min + Toplam eklendi; sıra Son·Min·Maks·Ort·Toplam. Toplam'ın
// sortValue'su toplanamaz birimde NULL döner: sortRows null'ı yön ne olursa
// olsun en dibe indirir, yani "—" basan satırlar sıralamayı kirletmez.
const BASE_COLS: DataTableColumn<GroupRow>[] = [
  { id: 'series',  label: 'Seri',     sortValue: r => `${r.letter} ${r.label}`, naturalDir: 'asc', width: 320 },
  { id: 'cursor',  label: '@imleç',   numeric: true, width: 110 },
  { id: 'last',    label: 'Son',      sortValue: r => r.last,    numeric: true, width: 110 },
  { id: 'min',     label: 'Min',      sortValue: r => r.min,     numeric: true, width: 110 },
  { id: 'max',     label: 'Maks',     sortValue: r => r.max,     numeric: true, width: 110 },
  { id: 'avg',     label: 'Ort',      sortValue: r => r.avg,     numeric: true, width: 110 },
  { id: 'sum',     label: 'Toplam',   sortValue: r => (r.additive ? r.sum : null), numeric: true, width: 110 },
  { id: 'buckets', label: 'Bucket',   sortValue: r => r.buckets, numeric: true, width: 90 },
];

// v0.9.824 — Δ % sütunu YALNIZ karşılaştırma verisi VARKEN eklenir.
//
// Hep basılsaydı, varsayılan (karşılaştırma kapalı) durumda baştan aşağı
// "—" yazan ölü bir sütun olurdu; ölü sütun okuyanı "burada bir sayı olmalı,
// neden yok?" diye yanlış soruya götürür. Bedeli, useDataTable'ın kalıcı
// GENİŞLİKLERİNİN toggle'da sıfırlanmasıdır (columnLayoutSig, v0.9.695
// kolon-kümesi mührü) — bilinçli takas: sıralama (ayrı anahtar) korunuyor,
// yalnız sürüklenmiş genişlikler tazeleniyor.
const DELTA_COL: DataTableColumn<GroupRow> = {
  id: 'delta', label: 'Δ %',
  // null'lar sortRows'ta yön ne olursa olsun EN DİBE iner (Toplam
  // sütununun v0.9.806'daki kuralı) — "—" basan satırlar sıralamayı
  // kirletmez ve bir başa bir sona zıplamaz.
  sortValue: r => r.deltaPct,
  numeric: true, width: 100,
};

export function groupTableCols(hasCompare: boolean): DataTableColumn<GroupRow>[] {
  if (!hasCompare) return BASE_COLS;
  // Δ, karşılaştırdığı sayıların (Ort / Toplam) HEMEN SAĞINDA.
  return [...BASE_COLS.slice(0, -1), DELTA_COL, BASE_COLS[BASE_COLS.length - 1]];
}

export function buildGroupRows(panels: PanelData[]): GroupRow[] {
  const rows: GroupRow[] = [];
  for (const p of panels) {
    if (p.state !== 'ready') continue;   // v0.9.804 — idle/loading/error satır üretmez
    // v0.9.806 — Toplam sütununun kapısı panel BAŞINA sorulur: birim
    // paneldeki tüm serilerde aynı.
    const additive = isAdditiveUnit(p.unit);
    // v0.9.824 — önceki dönemin istatistikleri ETİKETE göre. Hayaletler
    // buildGhostSeries'ten geliyor, yani etiketleri seriesGroupLabel'in
    // AYNI türetmesinden — elle kurulmuş bir eşleşme anahtarı, bir gün
    // farklı bir seriye Δ yazdırırdı. Hayalet yoksa harita boş: her satır
    // "—" basar, sütun sessizce sıfır göstermez.
    const prevByLabel = new Map<string, { sum: number; mean: number }>();
    for (const g of p.ghosts ?? []) {
      const gs = seriesStats(g.points.map(x => x.value));
      if (!gs.count) continue;
      prevByLabel.set(g.label, { sum: gs.sum, mean: gs.mean ?? NaN });
    }
    for (const s of p.series) {
      // seriesStats — lejantla PAYLAŞILAN tek geçişli çekirdek (v0.9.103).
      // Kendi reduce/Math.max zincirimizi yazmak, aynı sayının iki yerde
      // farklı hesaplanma riskini geri getirirdi; ayrıca Math.max(...vs)
      // spread'i uzun serilerde yığın sınırına dayanır.
      const st = seriesStats(s.points.map(x => x.value));
      rows.push({
        rowKey: `${p.letter}:${s.label}`,
        letter: p.letter,
        isFormula: p.isFormula,
        label: s.label,
        unit: p.unit,
        // v0.10.510 (D7) — çizginin rengi (panel içi yuva ataması, PanelStack)
        // satırın rengidir; renk taşımayan seri (eski çağıran/test) hash'e düşer.
        color: s.color ?? seriesColor(s.label),
        // Boş seride NaN — fmtSmart bunu "—" basar (mevcut sözleşme).
        last: st.last ?? NaN,
        min: st.min ?? NaN,
        avg: st.mean ?? NaN,
        max: st.max ?? NaN,
        sum: st.count ? st.sum : NaN,
        additive,
        buckets: st.count,
        points: s.points,
        pivot: s.pivot,
        deltaPct: deltaPct(
          st.count ? { sum: st.sum, mean: st.mean ?? NaN } : null,
          prevByLabel.get(s.label) ?? null,
          additive),
      });
    }
  }
  return rows;
}

// PivotButtons (v0.9.848) — satırdan tek tıkla daraltma / hariç tutma.
//
// Sağ-tık ya da ⋯ menüsü DEĞİL: iki eylem var, ikisi de tek tık, ve bir
// menü açmak (aç → oku → seç) tek bir çipin maliyetini üçe katlardı. Jest
// dili satırın kendisiyle çakışmasın diye her ikisi de stopPropagation
// yapıyor — düz tık hâlâ "yalnız bu seriyi göster" (izole).
//
// EXCLUDE ÇOK BOYUTLU SATIRDA PASİF: iki anahtarlı bir split'te satır bir
// KOMBİNASYONDUR ve o kombinasyonun değili düz AND çiplerle yazılamaz
// (`k1!=v1 AND k2!=v2` satırdan çok fazlasını eler). Yaklaşık bir filtreyi
// doğruymuş gibi göstermektense düğme gri kalır, sebep title'da.
function PivotButtons({ pivot, onPivot }: {
  pivot: PivotAffordance;
  onPivot: (pairs: PivotPair[], mode: PivotMode) => void;
}) {
  const label = pivot.pairs.map(p => `${p.k}=${p.v}`).join(' · ');
  const excludeWhy = pivot.disabled
    ?? (pivot.pairs.length > 1
      ? 'Çok boyutlu satırda "hariç tut" düz AND filtresiyle yazılamaz — satırdan fazlasını elerdi'
      : undefined);
  const btn = (
    disabled: string | undefined, glyph: string, aria: string, title: string,
    mode: PivotMode,
  ) => (
    <IconButton
      aria-label={aria}
      title={disabled ?? title}
      disabled={!!disabled}
      variant="bare" size="xs"
      onClick={e => { e.stopPropagation(); if (!disabled) onPivot(pivot.pairs, mode); }}
      icon={glyph} />
  );
  return (
    <>
      {btn(pivot.disabled, '⊕', 'Yalnız bu değere daralt',
        `Yalnız bu değere daralt — ${label} filtre çipi olarak eklenir, boyut splitten düşer`,
        'only')}
      {btn(excludeWhy, '⊖', 'Bu değeri hariç tut',
        `Bu değeri hariç tut — ${label} için != çipi eklenir, split korunur`,
        'exclude')}
    </>
  );
}

// SourceButton (v0.9.930) — "kaynağa git". ⊕/⊖ ile AYNI ailede, üçüncü çip.
//
// Neden ayrı bir düğme ve neden düz tık DEĞİL: düz tık zaten izole ediyor
// (v0.9.757, operatör talebi) ve Ctrl/Cmd+tık gizle-göster. Üçüncü bir tık
// jesti kalmadı. Enter/o klavye yolu var ama tek başına KEŞFEDİLEMEZ olurdu:
// ekranda görünmeyen bir yetenek yok sayılır.
//
// İfade edilemeyen durumda düğme KAYBOLMUYOR, pasifleşiyor ve sebep title'da
// — v0.9.848'in ⊖ kararıyla aynı dil.
function SourceButton({ target, onOpen }: { target: SourceTarget; onOpen: () => void }) {
  return (
    <IconButton
      aria-label="Kaynağa git — bu satırı üreten spanlar"
      title={target.ok
        ? 'Kaynağa git — bu satırı üreten span listesi, aynı pencere ve aynı filtrelerle (Enter)'
        : target.why}
      disabled={!target.ok}
      variant="bare" size="xs"
      onClick={e => { e.stopPropagation(); if (target.ok) onOpen(); }}
      icon="⇥" />
  );
}

export function GroupTable({ panels, hiddenKeys, onToggleHidden, onIsolate, onFocus, onPivot, sourceTarget, onOpenSource }: {
  panels: PanelData[];
  hiddenKeys: Set<string>;
  onToggleHidden: (rowKey: string) => void;
  // v0.9.930 — satırdan KAYNAĞA iniş. Tablo hangi harften hangi çiftlerle
  // istendiğini söyler; hangi sorgunun hangi pencereyle URL'e çevrileceğini
  // SAYFA bilir (onPivot ile aynı iş bölümü). `sourceTarget` aynı kararın
  // salt-okunur hâli: düğmenin pasif olup olmayacağı ve sebebi.
  sourceTarget?: (letter: string, pairs: PivotPair[]) => SourceTarget;
  onOpenSource?: (letter: string, pairs: PivotPair[]) => void;
  // v0.9.848 — satır pivotu. Hangi sorgunun düzenleneceğini SAYFA bilir
  // (harf → BuilderQuery); tablo yalnız hangi satırdan hangi çiftlerle
  // istendiğini söyler.
  onPivot?: (letter: string, pairs: PivotPair[], mode: PivotMode) => void;
  // v0.9.757 (operatör: "bir tanesine bastığımda sadece o gözüksün") —
  // düz tık İZOLE eder (CorePanel lejantı semantiği); Ctrl/Cmd+tık eski
  // tekil gizle/göster. İzole olanı yeniden tıklamak hepsini geri açar.
  onIsolate: (rowKey: string) => void;
  onFocus: (rowKey: string | null) => void;
}) {
  const rows = useMemo(() => buildGroupRows(panels), [panels]);
  // Re-renders this table (and only this table) once per animation frame while
  // the crosshair moves — the charts stay out of React via uPlot's own sync.
  const cursorSec = useCursorTime();

  // Δ sütunu ancak ÇİZİLMİŞ bir hayalet varsa. Karşılaştırma açık ama
  // hayalet düşmüşse (çözünürlük uyuşmazlığı / önceki dönemde veri yok)
  // sütun da çıkmaz — panel şeridi sebebini zaten söylüyor.
  const hasCompare = useMemo(() => panels.some(p => p.ghosts?.length), [panels]);
  const columns = useMemo(() => groupTableCols(hasCompare), [hasCompare]);

  // v0.9.930 — satır "açmak" = KAYNAĞA GİTMEK. Bu tablonun düz tıkı zaten
  // izole ediyor, yani Enter'ın satırı "genişletmesi" diye bir şey yok;
  // açık kalan ürün kararı (v0.9.918) operatör tarafından kaynağa iniş
  // olarak cevaplandı. İfade edilemeyen satırda Enter sessizce hiçbir şey
  // yapmıyor — sebep aynı satırdaki pasif ⇥ düğmesinin title'ında yazıyor.
  const dt = useDataTable<GroupRow>({
    storageKey: 'explore-group-table',
    columns,
    rows,
    initialSort: { id: 'max', dir: 'desc' },
    onOpen: onOpenSource
      ? (r) => {
        const pairs = r.pivot?.pairs ?? [];
        if (sourceTarget && !sourceTarget(r.letter, pairs).ok) return;
        onOpenSource(r.letter, pairs);
      }
      : undefined,
  });

  if (rows.length === 0) return null;

  // v0.8.412 (Data-Explorer parity DE1) — export the full result set
  // (every series point, long format) as CSV. Client-only Blob; no
  // request, no size surprise (the rows are already in memory).
  const exportCSV = () => {
    const blob = new Blob([panelsToCSV(panels)], { type: 'text/csv;charset=utf-8' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = `coremetry-explore-${new Date().toISOString().replace(/[:.]/g, '-')}.csv`;
    a.click();
    URL.revokeObjectURL(a.href);
  };

  return (
    <div className="table-wrap is-fit" style={{ marginTop: 12 }}
      onMouseLeave={() => onFocus(null)}>
      <div style={{ display: 'flex', justifyContent: 'flex-end', padding: '6px 8px 0' }}>
        <Button variant="secondary" size="sm" onClick={exportCSV}
          title={panels.some(p => p.more > 0 || p.rowsCapped)
            ? 'Görünen serileri indirir (top-N kırpılmış küme) — kırpma CSV içinde # yorum satırıyla işaretlidir'
            : 'Download every series point (long format: query, series, unit, time, value)'}>
          ⤓ CSV
        </Button>
      </div>
      <table style={{ tableLayout: 'fixed', width: '100%' }}>
        <DataTableColgroup dt={dt} />
        <DataTableHead dt={dt} />
        <tbody>
          {dt.sortedRows.map((r, i) => {
            const hidden = hiddenKeys.has(r.rowKey);
            const target = sourceTarget?.(r.letter, r.pivot?.pairs ?? []);
            return (
              <tr key={r.rowKey}
                {...dt.rowProps(i)}
                onMouseEnter={() => onFocus(hidden ? null : r.rowKey)}
                onClick={(e) => (e.ctrlKey || e.metaKey) ? onToggleHidden(r.rowKey) : onIsolate(r.rowKey)}
                title="Tıkla: yalnız bu seri · Ctrl/Cmd+tık: gizle-göster · Enter: kaynağa git · üzerine gel: panelde vurgula"
                style={{ cursor: 'pointer', opacity: hidden ? 0.45 : 1,
                         contentVisibility: 'auto', containIntrinsicSize: 'auto 36px' }}>
                {/* v0.9.848 — hücre FLEX oldu. Pivot düğmeleri etiketin
                    SOLUNDA ve flexShrink:0: sağına konsaydı uzun bir grup
                    etiketi (ellipsis + nowrap) onları sessizce kırpardı —
                    bu depoda tekrar eden bir kesilme sınıfı. Taşan tek şey
                    etiket, ve o zaten title'da tam hâliyle duruyor. */}
                <td style={{ overflow: 'hidden' }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 2, minWidth: 0 }}>
                    {/* v0.9.806 — glif SERİNİN RENGİNDE. Tablo tek lejant ama
                        renksizdi: 10 serilik bir panelde satırı çizgiyle
                        eşleştirmek imkânsızdı. Gizli seride --text3, çünkü
                        renk "bu çizgi ekranda" demektir. */}
                    <span aria-hidden style={{
                      marginRight: 4, fontSize: 11, flexShrink: 0,
                      color: hidden ? 'var(--text3)' : r.color,
                    }}>{hidden ? '○' : '◉'}</span>
                    <span style={{
                      display: 'inline-flex', alignItems: 'center', justifyContent: 'center',
                      width: 16, height: 16, borderRadius: 3, marginRight: 4, flexShrink: 0,
                      background: r.isFormula ? 'var(--bg3)' : 'var(--accent2)',
                      color: r.isFormula ? 'var(--text2)' : 'var(--bg)',
                      fontSize: 10, fontWeight: 700,
                    }}>{r.letter}</span>
                    {onPivot && r.pivot && (
                      <PivotButtons pivot={r.pivot}
                        onPivot={(pairs, mode) => onPivot(r.letter, pairs, mode)} />
                    )}
                    {onOpenSource && target && (
                      <SourceButton target={target}
                        onOpen={() => onOpenSource(r.letter, r.pivot?.pairs ?? [])} />
                    )}
                    <b title={r.label} style={{
                      marginLeft: 4, minWidth: 0,
                      overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                    }}>{r.label}</b>
                  </div>
                </td>
                <td className="mono" style={{ textAlign: 'right', color: cursorSec == null ? 'var(--text3)' : 'var(--accent2)' }}>
                  {cursorSec == null ? '·' : fmtSmart(valueAtCursor(r.points, cursorSec), r.unit)}
                </td>
                <td className="mono" style={{ textAlign: 'right' }}>{fmtSmart(r.last, r.unit)}</td>
                <td className="mono" style={{ textAlign: 'right' }}>{fmtSmart(r.min, r.unit)}</td>
                <td className="mono" style={{ textAlign: 'right' }}>{fmtSmart(r.max, r.unit)}</td>
                <td className="mono" style={{ textAlign: 'right' }}>{fmtSmart(r.avg, r.unit)}</td>
                {/* Toplam yalnız TOPLANABİLİR birimde. ms/%/latency'de
                    pod'lar arası toplam anlamsız bir sayı olurdu — "—"
                    basıp neden olduğunu title'da söylüyoruz. */}
                <td className="mono" style={{ textAlign: 'right' }}
                  title={r.additive ? undefined
                    : `Toplam bu birimde anlamlı değil (${r.unit || 'birimsiz'})`}>
                  {r.additive ? fmtSmart(r.sum, r.unit) : '—'}
                </td>
                {/* v0.9.824 — Δ %: artış KIRMIZI değil NÖTR renk ailesinde
                    kalır. "Daha çok istek" iyi, "daha çok gecikme" kötü;
                    tabloda hangi yönün iyi olduğunu bilen tek şey birim
                    değil operatörün niyeti. Yön OKLA söylenir (↑/↓), değer
                    yargısı RENKLE söylenmez. */}
                {hasCompare && (
                  <td className="mono" style={{ textAlign: 'right' }}
                    title={r.deltaPct == null
                      ? 'Önceki dönemde bu serinin karşılığı yok ya da önceki değer sıfır — yüzde değişim tanımsız'
                      : `${r.additive ? 'Toplam' : 'Ortalama'} bazında önceki döneme göre`}>
                    {r.deltaPct == null ? '—'
                      : `${r.deltaPct >= 0 ? '↑' : '↓'} ${Math.abs(r.deltaPct).toFixed(1)}%`}
                  </td>
                )}
                <td className="mono" style={{ textAlign: 'right' }}>{fmtNum(r.buckets)}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
