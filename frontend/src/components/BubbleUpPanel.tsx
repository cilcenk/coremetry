import { useEffect, useState } from 'react';
import type { BubbleUpResult, FilterExpr } from '@/lib/types';
import { api } from '@/lib/api';
import { encodeFilters } from '@/lib/urlState';
import { Spinner } from './Spinner';
import { fmtNum } from '@/lib/utils';

// BubbleUp panel — Honeycomb's "what's special about THESE
// spans" investigator. Caller hands in:
//   • baseline filter list (the wider population)
//   • selection filter list (additional predicates that
//                            narrow it down)
//   • from/to time range
// Panel runs the divergence query and surfaces top
// over-represented attribute values per key.
//
// Performance posture: fetched lazily on first mount only —
// the parent decides when to render the panel (e.g. behind a
// "Investigate selection" button). Subsequent same-input
// remounts hit the 60s server cache.

export function BubbleUpPanel({
  baseline, selection, baselineDsl, selectionDsl, from, to, onApplyFilter,
}: {
  baseline: FilterExpr[];
  selection: FilterExpr[];
  // Optional free-form DSL predicates AND-joined with the FilterExpr lists on
  // each side. The Explore heatmap box-select passes query A's advanced DSL as
  // baselineDsl so the baseline population matches what the heatmap rendered.
  baselineDsl?: string;
  selectionDsl?: string;
  from: number;
  to: number;
  // When the operator clicks an attribute value chip, we add
  // it as a real filter to the parent's URL state. The parent
  // wires what "apply" means in its context.
  onApplyFilter?: (f: FilterExpr) => void;
}) {
  const [data, setData] = useState<BubbleUpResult | null | undefined>(undefined);

  useEffect(() => {
    setData(undefined);
    api.spanBubbleUp({
      from, to,
      filters: baseline.length ? encodeFilters(baseline) : undefined,
      selFilters: selection.length ? encodeFilters(selection) : undefined,
      dsl: baselineDsl?.trim() || undefined,
      selDsl: selectionDsl?.trim() || undefined,
    })
      .then(r => setData(r ?? null))
      .catch(() => setData(null));
  }, [
    // Stringify both sides so the deps shape is stable.
    JSON.stringify(baseline), JSON.stringify(selection), baselineDsl, selectionDsl, from, to,
  ]);

  if (data === undefined) {
    return (
      <div style={panelBox}>
        <PanelHeader />
        <div style={{ minHeight: 100, display: 'grid', placeItems: 'center' }}>
          <Spinner />
        </div>
      </div>
    );
  }
  if (data === null) {
    return (
      <div style={panelBox}>
        <PanelHeader />
        <div style={{ fontSize: 12, color: 'var(--err)' }}>
          BubbleUp query failed.
        </div>
      </div>
    );
  }
  if (data.selectionTotal === 0) {
    return (
      <div style={panelBox}>
        <PanelHeader />
        <div style={{ fontSize: 12, color: 'var(--text3)', fontStyle: 'italic' }}>
          No spans matched the selection — try a wider window or relax filters.
        </div>
      </div>
    );
  }

  return (
    <div style={panelBox}>
      <PanelHeader />
      <div style={{
        fontSize: 11, color: 'var(--text3)', marginBottom: 8,
      }}>
        Comparing <b style={{ color: 'var(--text)' }}>{fmtNum(data.selectionTotal)}</b> selected
        {' '}spans against <b style={{ color: 'var(--text)' }}>{fmtNum(data.baselineTotal)}</b> baseline.
        Score = selection % − baseline %; values over-represented in the selection
        sort to the top.
      </div>
      {/* v0.9.472 (dürüstlük A7) — örneklemeli okuma bunu SÖYLER: anahtar
          seti occurrence'a göre ilk 30, anahtar başına 6 değer, toplamı
          <5 olan değerler elenir, panel ilk 10 anahtarı çizer. Nadir ama
          açıklayıcı bir anahtar bu kapılardan geçmemiş olabilir — boş
          sonuç "hiçbir şey ayırt etmiyor" kanıtı DEĞİLDİR. */}
      <div style={{ fontSize: 10.5, color: 'var(--text3)', marginBottom: 8 }}
        title="Sınırlar sorguyu milyar-span ölçeğinde sınırlı tutmak için: en sık görülen 30 anahtar × anahtar başına en güçlü 6 değer; toplam sayısı 5'in altındaki değerler gürültü diye elenir; panel ilk 10 anahtarı gösterir. Nadir-ama-açıklayıcı bir attribute bu kapılara takılmış olabilir — kesin dışlama için Explore'da elle filtrele.">
        Örneklemeli okuma: ilk 30 anahtar · 6 değer/anahtar · toplam ≥5 · ilk 10 gösterilir
      </div>
      {data.attributes.length === 0 ? (
        <div style={{ fontSize: 12, color: 'var(--text3)', fontStyle: 'italic' }}>
          Bu örneklem kapılarında hiçbir anahtar seçimi baseline'dan ayırt etmedi — nadir bir anahtar kapılara takılmış olabilir; kesin dışlama değildir.
        </div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          {data.attributes.slice(0, 10).map(attr => (
            <AttributeBlock key={attr.key} attr={attr}
              onApply={onApplyFilter} />
          ))}
          {data.attributes.length > 10 && (
            <div style={{ fontSize: 11, color: 'var(--text3)' }}>
              +{data.attributes.length - 10} anahtar daha skorlandı ama gösterilmiyor (ilk 10)
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function PanelHeader() {
  return (
    <div style={{
      fontSize: 11, fontWeight: 600,
      letterSpacing: '0.5px', textTransform: 'uppercase',
      color: 'var(--text2)', marginBottom: 8,
    }}>
      BubbleUp · what's special about these spans
    </div>
  );
}

function AttributeBlock({ attr, onApply }: {
  attr: BubbleUpResult['attributes'][number];
  onApply?: (f: FilterExpr) => void;
}) {
  return (
    <div>
      <div style={{
        fontSize: 11, fontFamily: 'ui-monospace, monospace',
        color: 'var(--text2)', marginBottom: 4,
      }}>
        {attr.key}
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
        {attr.values.map(v => {
          const score = v.score;
          const positive = score >= 0;
          // Bar widths — selection vs baseline, normalised
          // to a fixed canvas width so the eye can compare
          // pairs of rows directly. Always show at least 2px
          // for non-zero values.
          const sel = Math.max(score > 0 ? 2 : 0, v.selectionPct * 100);
          const base = Math.max(v.baselinePct > 0 ? 2 : 0, v.baselinePct * 100);
          const onClick = onApply
            ? () => onApply({ k: attr.key, op: '=', v: [v.value] })
            : undefined;
          return (
            <button key={v.value} type="button"
              onClick={onClick}
              disabled={!onClick}
              title={onClick ? `Filter ${attr.key} = ${v.value}` : undefined}
              style={{
                display: 'grid',
                gridTemplateColumns: '1fr 90px 60px',
                alignItems: 'center', gap: 8,
                padding: '3px 8px',
                background: 'transparent',
                border: '1px solid var(--border)',
                borderRadius: 4,
                cursor: onClick ? 'pointer' : 'default',
                fontFamily: 'ui-monospace, monospace',
                fontSize: 11, color: 'var(--text)',
                textAlign: 'left',
                width: '100%',
              }}>
              {/* Value label — truncate gracefully if very long */}
              <span style={{
                overflow: 'hidden', textOverflow: 'ellipsis',
                whiteSpace: 'nowrap', minWidth: 0,
              }}>
                {v.value || <em style={{ color: 'var(--text3)' }}>(empty)</em>}
              </span>
              {/* Bar pair: selection on top, baseline below */}
              <span style={{ display: 'inline-flex', flexDirection: 'column', gap: 2 }}>
                <span title={`selection: ${(v.selectionPct * 100).toFixed(1)}% (${v.selectionCount})`}
                      style={{ height: 5, background: 'var(--accent2)',
                               width: `${sel}%`, borderRadius: 2 }} />
                <span title={`baseline: ${(v.baselinePct * 100).toFixed(1)}% (${v.baselineCount})`}
                      style={{ height: 5, background: 'var(--text3)',
                               width: `${base}%`, borderRadius: 2, opacity: 0.5 }} />
              </span>
              {/* Score */}
              <span style={{
                textAlign: 'right',
                color: positive ? 'var(--err)' : 'var(--text3)',
                fontWeight: 600,
              }}>
                {positive ? '+' : ''}{(score * 100).toFixed(1)}%
              </span>
            </button>
          );
        })}
      </div>
    </div>
  );
}

const panelBox: React.CSSProperties = {
  background: 'var(--bg1)',
  border: '1px solid var(--border)',
  borderRadius: 8,
  padding: 14,
  marginTop: 14,
};
