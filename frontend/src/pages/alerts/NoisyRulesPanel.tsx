import { useCallback, useEffect, useState } from 'react';
import { Button, useConfirm } from '@/components/ui';
import { Spinner } from '@/components/Spinner';
import { IconSparkles } from '@/components/icons';
import { QueryErrorInline } from '@/components/QueryError';
import { useUpdateAlertRule, useDisableAlertRule } from '@/lib/queries';
import { api } from '@/lib/api';
import { tsLong } from '@/lib/utils';
import type { AlertRule, NoisyRule } from '@/lib/types';
import { AIFeedbackButtons } from '@/components/ai/AIFeedbackButtons';
import { RenderedMarkdown } from '@/components/Markdown';

// NoisyRulesPanel — surfaces rules that have opened problems most
// often in the last 24h with a one-click "Apply" affordance that
// pre-fills the edit form with the suggested dampening values, plus
// a bulk-apply / bulk-disable selection path. Hidden when no rule has
// a suggestion (the report fetches the top N and we filter to those
// with a non-empty suggestion).
//
// Split out of the Alerts.tsx monolith (v0.8.252 refactor). Owns its
// own noisy/selected/bulk state + the two mutation hooks; the single
// cross-cutting concern — opening the parent's edit form pre-filled —
// rides the onEditFromSuggestion callback so behaviour is unchanged.
export function NoisyRulesPanel({ rules, onEditFromSuggestion }: {
  // The (kind-filtered) rules list — used to look up the base rule a
  // suggestion targets. Passed through verbatim so the base-lookup
  // scope matches the pre-refactor behaviour (a rule filtered out of
  // the view can't be found → the suggestion silently no-ops).
  rules: AlertRule[] | undefined;
  onEditFromSuggestion: (draft: Partial<AlertRule>, ruleId: string) => void;
}) {
  const confirm = useConfirm();
  // Noisy-rules report (v0.5.131). 24h window by default; server
  // caches the heavy GROUP BY for 5 min so a fleet of operators
  // hitting /alerts at the same time shares one round-trip.
  // v0.9.866 (tutarlılık denetimi MT1) — durum şekli değişti: başlangıç
  // `null` idi, `.catch` de `null` yazıyordu. Yani "henüz yüklenmedi" ile
  // "rapor alınamadı" AYNI DEĞERDİ ve ikisi de `return null` ile sessizce
  // gizleniyordu. Gürültülü kural raporu 500'lediğinde operatör panelin hiç
  // olmadığını sanıyordu. Tri-state: undefined = yükleniyor, null = hata.
  const [noisy, setNoisy] = useState<NoisyRule[] | null | undefined>(undefined);
  // Bulk-apply selection set (v0.5.151). One operator complaint we
  // kept hitting: 5+ rules need the same flap-suppression treatment
  // and clicking Apply → save → close for each one is annoying.
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [bulkBusy, setBulkBusy] = useState(false);
  // v0.9.1080 (F3.3) — ✨ tek-atış gürültü anlatımı. Fetch yalnız
  // tıklamayla (LLM maliyet disiplini); panel Shift emsali.
  // v0.9.1121 (Faz 0.3b) — xid: cevabın ai_calls kimliği; 👍/👎 buna asılı.
  const [ai, setAi] = useState<{ busy: boolean; text: string | null; err: string | null; xid?: string }>({ busy: false, text: null, err: null });
  const explainNoise = async () => {
    setAi({ busy: true, text: null, err: null });
    try {
      const r = await api.explainAlertNoise();
      setAi({ busy: false, text: r.explanation, err: null, xid: r.exchangeId });
    } catch (e) {
      setAi({ busy: false, text: null, err: e instanceof Error ? e.message : 'Anlatım alınamadı' });
    }
  };
  const updateRule = useUpdateAlertRule();
  const disableRule = useDisableAlertRule();
  // Tek okuma yolu — hem ilk yükleme hem Retry buradan geçer, böylece hata
  // dalı iki yerde ayrı ayrı yazılmak zorunda kalmıyor (MT1'i mümkün kılan
  // şey guard'ın elle çoğaltılmasıydı).
  const load = useCallback(() => {
    setNoisy(undefined);
    api.alertTuningNoisyRules('24h', 10)
      .then(r => setNoisy((r?.rules ?? []).filter(n => n.suggestion !== '')))
      .catch(() => setNoisy(null));
  }, []);
  useEffect(load, [load]);
  const applySuggestion = (n: NoisyRule) => {
    const base = (rules ?? []).find(r => r.id === n.ruleId);
    if (!base) return;
    onEditFromSuggestion({
      ...base,
      forSec:      n.suggestedForSec      ?? base.forSec      ?? 0,
      minSamples:  n.suggestedMinSamples  ?? base.minSamples  ?? 0,
      cooldownSec: n.suggestedCooldownSec ?? base.cooldownSec ?? 0,
    }, n.ruleId);
  };
  // Toggle one row in / out of the bulk selection. Two actions
  // operate on this set: Apply (only rules with concrete knob
  // suggestions are touched) and Disable (every selected rule is
  // flipped off, regardless of suggestion shape). v0.5.154 widened
  // the checkbox eligibility so threshold-only hints can still be
  // bulk-disabled.
  const toggleSelected = (id: string) => {
    setSelected(prev => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id); else next.add(id);
      return next;
    });
  };
  // Rules that have ≥1 concrete dampening value to apply — the
  // "tighten threshold" suggestion sets nothing actionable but the
  // row is still selectable for bulk-disable.
  const actionable = (noisy ?? []).filter(
    n => (n.suggestedForSec ?? 0) > 0
      || (n.suggestedMinSamples ?? 0) > 0
      || (n.suggestedCooldownSec ?? 0) > 0,
  );
  const actionableSelectedCount = actionable.filter(n => selected.has(n.ruleId)).length;
  const allNoisyIDs = (noisy ?? []).map(n => n.ruleId);
  const allSelected = allNoisyIDs.length > 0
    && allNoisyIDs.every(id => selected.has(id));
  const toggleAll = () => {
    setSelected(allSelected ? new Set() : new Set(allNoisyIDs));
  };
  // Bulk-apply path. Builds a patch per rule that ONLY touches the
  // knob the suggestion targets, preserving any operator-set value
  // the suggestion doesn't address. Patches run in parallel since
  // each rule is independent — the React Query mutation invalidates
  // the list eagerly so the table refreshes once all settle.
  const applySelected = async () => {
    if (actionableSelectedCount === 0 || !rules) return;
    setBulkBusy(true);
    try {
      const patches: Promise<unknown>[] = [];
      for (const n of actionable) {
        if (!selected.has(n.ruleId)) continue;
        const base = rules.find(r => r.id === n.ruleId);
        if (!base) continue;
        const patch: Partial<AlertRule> = {
          forSec:      (n.suggestedForSec      ?? 0) || base.forSec      || 0,
          minSamples:  (n.suggestedMinSamples  ?? 0) || base.minSamples  || 0,
          cooldownSec: (n.suggestedCooldownSec ?? 0) || base.cooldownSec || 0,
        };
        patches.push(updateRule.mutateAsync({ id: n.ruleId, patch }));
      }
      await Promise.allSettled(patches);
      // Drop the selection set + refetch the noisy-rules report
      // (a freshly-tightened rule should drop off the list within
      // the server cache window, but a refetch makes the UI feel
      // responsive in the meantime).
      setSelected(new Set());
      api.alertTuningNoisyRules('24h', 10)
        .then(r => setNoisy((r?.rules ?? []).filter(n => n.suggestion !== '')))
        .catch(() => {});
    } finally {
      setBulkBusy(false);
    }
  };
  // Bulk-disable (v0.5.154) — flips `enabled` to false on every
  // selected rule. Reuses deleteAlertRule which is already wired as
  // a soft-disable (SetAlertRuleEnabled false), so re-enabling from
  // the main table works as it did before. One-step confirm because
  // the action affects N rules at once and an operator's misclick
  // would silence everything.
  const disableSelected = async () => {
    if (selected.size === 0) return;
    const ids = Array.from(selected);
    if (!await confirm({
      title: 'Seçili kurallar devre dışı bırakılsın mı?',
      body: <><b>{ids.length}</b> alert kuralı susturulacak. Bu YUMUŞAK bir
        kapatma: tanımlar duruyor, aşağıdaki listeden tek tıkla geri
        açılabilir.</>,
      confirmLabel: `${ids.length} kuralı kapat`,
      danger: true,
    })) {
      return;
    }
    setBulkBusy(true);
    try {
      await Promise.allSettled(ids.map(id => disableRule.mutateAsync(id)));
      setSelected(new Set());
      api.alertTuningNoisyRules('24h', 10)
        .then(r => setNoisy((r?.rules ?? []).filter(n => n.suggestion !== '')))
        .catch(() => {});
    } finally {
      setBulkBusy(false);
    }
  };

  // İlk boyamada hata banner'ı ÇAKMAMALI: yükleniyorken panel sessiz kalır
  // (bu bir öneri paneli, sayfanın gövdesi değil). Hata dalı QueryErrorInline
  // — <Empty> değil, çünkü boş-durum anlatısı tam da kaçınmaya çalıştığımız
  // yanlış mesaj. Gerçekten öneri yoksa panel eskisi gibi kendini gizler.
  if (noisy === undefined) return null;
  if (noisy === null) {
    return (
      <div style={{ marginBottom: 14 }}>
        <QueryErrorInline
          text="Noisy-rules report could not be loaded — tuning suggestions are unavailable, not absent."
          onRetry={load} />
      </div>
    );
  }
  if (noisy.length === 0) return null;

  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 8, padding: 14, marginBottom: 14,
    }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 8 }}>
        <span style={{ fontSize: 13, fontWeight: 700 }}>⚡ Noisy rules (last 24h)</span>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          {noisy.length} rule{noisy.length === 1 ? '' : 's'} could be tightened
        </span>
        <Button variant="secondary" size="sm" onClick={explainNoise} disabled={ai.busy}
          title="Son 24 saatin gürültü paketini AI anlatır: baskın desen + önce sıkılacak vida">
          <IconSparkles /> Gürültüyü anlat
        </Button>
        {selected.size > 0 && (
          <span style={{ marginLeft: 'auto', display: 'inline-flex', gap: 6 }}>
            <Button variant="primary" size="sm" onClick={applySelected}
              disabled={bulkBusy || actionableSelectedCount === 0}
              title={actionableSelectedCount === 0
                ? 'None of the selected rows ship a concrete value to apply'
                : `Apply the suggested dampening values to ${actionableSelectedCount} rule${actionableSelectedCount === 1 ? '' : 's'} in one shot`}>
              {bulkBusy
                ? 'Working…'
                : `Apply ${actionableSelectedCount} suggestion${actionableSelectedCount === 1 ? '' : 's'}`}
            </Button>
            {/* v0.9.1006 (M4/O6) — toplu-eylem şeridinde dolu accent
                "Apply N"in yanında ikinci dolgu vardı. */}
            <Button variant="ghost-danger" size="sm" onClick={disableSelected}
              disabled={bulkBusy}
              title="Disable (soft-delete) every selected rule. Re-enable from the list below.">
              Disable {selected.size} rule{selected.size === 1 ? '' : 's'}
            </Button>
          </span>
        )}
      </div>
      {/* Faz 0.5 — ham metin yerine markdown. Bkz. Shift.tsx'teki aynı
          düzeltme; pre-wrap kalkıyor çünkü RenderedMarkdown kendi blok
          düzenini kuruyor. */}
      {(ai.busy || ai.text || ai.err) && (
        <div style={{
          padding: '10px 12px', marginBottom: 10, borderRadius: 6,
          background: 'var(--bg2)', border: '1px solid var(--border)',
          fontSize: 12.5, lineHeight: 1.55,
        }}>
          <div style={{ fontSize: 10.5, fontWeight: 700, letterSpacing: 0.4, color: 'var(--text2)', marginBottom: 6 }}>
            AI GÜRÜLTÜ ANLATIMI
          </div>
          {ai.busy && <Spinner />}
          {ai.err && <span style={{ color: 'var(--err)' }}>{ai.err}</span>}
          {ai.text && <RenderedMarkdown text={ai.text} />}
          {/* v0.9.1121 (Faz 0.3b) — 👍/👎; kimlik yoksa çizilmez. */}
          <div><AIFeedbackButtons exchangeId={ai.xid} /></div>
        </div>
      )}
      <div className="table-wrap">
        <table>
          <thead><tr>
            <th style={{ width: 28 }}>
              {(noisy ?? []).length > 0 && (
                <input type="checkbox"
                  checked={allSelected}
                  onChange={toggleAll}
                  title={allSelected ? 'Clear selection' : 'Select all'} />
              )}
            </th>
            <th>Rule</th>
            <th className="num">Fires/24h</th>
            <th className="num">Median dur.</th>
            <th>Last fired</th>
            <th>Suggestion</th>
            <th></th>
          </tr></thead>
          <tbody>
            {noisy.map(n => {
              const hasKnob = (n.suggestedForSec ?? 0) > 0
                || (n.suggestedMinSamples ?? 0) > 0
                || (n.suggestedCooldownSec ?? 0) > 0;
              return (
              <tr key={n.ruleId}>
                <td>
                  <input type="checkbox"
                    checked={selected.has(n.ruleId)}
                    onChange={() => toggleSelected(n.ruleId)}
                    title={hasKnob
                      ? 'Include in bulk-apply OR bulk-disable'
                      : 'Threshold-only hint — Apply has nothing to set, but Disable still works'} />
                </td>
                <td><b>{n.ruleName}</b></td>
                <td className="num mono">{n.openCount}</td>
                <td className="num mono">
                  {n.medianDurSec >= 60
                    ? `${(n.medianDurSec / 60).toFixed(1)} min`
                    : `${n.medianDurSec.toFixed(0)} s`}
                </td>
                <td className="mono" style={{ fontSize: 11, color: 'var(--text3)' }}>
                  {tsLong(n.lastFiredNs)}
                </td>
                <td style={{ fontSize: 12, color: 'var(--text2)' }}>{n.suggestion}</td>
                <td>
                  <Button variant="secondary" size="sm"
                    onClick={() => applySuggestion(n)}
                    title="Open edit form with the suggested dampening values pre-filled">
                    Apply →
                  </Button>
                </td>
              </tr>
            );})}
          </tbody>
        </table>
      </div>
    </div>
  );
}
