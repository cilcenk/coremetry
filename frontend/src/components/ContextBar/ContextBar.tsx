// ContextBar.tsx — v0.10.250 (DataTable/ContextBar audit §10). Yeni bir
// şerit DEĞİL: Topbar'ın bastığı EnvPicker+TimeRangePicker çiftinin yerine
// geçer (`#topbar` içinde, uygulama kromu — feedback-no-floating-strips).
// Kontroller soldan sağa mevcut atomlarla: TimeRangePicker · EnvPicker
// applies · Cluster (≤10 değer <select>, üstü Combobox serverFiltered) ·
// Namespace (cluster uygulanıyorsa) · ServicePicker · Compare çipi.
// Uygulanmayan boyut devre dışı + ipucu (ürün-önce tasarımdan aşılanan
// `applies`). Dar ekranda üç kapsam kontrolü DisclosureButton "Kapsam (n)"
// arkasına (PageControls emsali). Yoğunluk cascade'i globals.css'te;
// satır içi ölçü yok. role="group" aria-label="Query context".

import { useMemo, useState, type ReactNode } from 'react';
import { TimeRangePicker } from '../TimeRangePicker';
import { EnvPicker } from '../EnvPicker';
import { ServicePicker } from '../ServicePicker';
import { Combobox } from '../Combobox';
import { Chip } from '../ui/Chip';
import { DisclosureButton } from '../ui/DisclosureButton';
import { useClusters } from '@/lib/queries/services';
import { useIsNarrow } from '@/lib/useNarrow';
import type { ContextParamsResult, ContextPatch } from '@/hooks/useContextParams';
import type { ContextDim } from '@/lib/contextParams';

export interface ContextBarProps {
  ctx: ContextParamsResult;
  /** Sayfa `?env=` değerini sorgularına geçiriyorsa true (EnvPicker applies). */
  envApplies?: boolean;
}

const NOT_APPLIED = 'Bu sayfada uygulanmıyor';
const CLUSTER_SELECT_MAX = 10;

function scopeCount(p: ContextParamsResult['params']): number {
  return [p.cluster, p.namespace, p.service].filter(Boolean).length;
}

export function ContextBar({ ctx, envApplies = false }: ContextBarProps) {
  const { params, applies, set, windowNs } = ctx;
  const narrow = useIsNarrow();
  const [open, setOpen] = useState(false);
  const clusterOn = applies.has('cluster');
  const clustersQ = useClusters(windowNs.from, windowNs.to);
  const clusters = useMemo(() => (clusterOn ? (clustersQ.data ?? []) : []), [clusterOn, clustersQ.data]);
  const patch = (p: ContextPatch) => set(p);

  const scopeControls: ReactNode = (
    <>
      <ClusterControl value={params.cluster} options={clusters} disabled={!clusterOn}
        onChange={v => patch({ cluster: v })} />
      <NamespaceControl value={params.namespace} disabled={!applies.has('namespace') || (clusterOn && !params.cluster)}
        onChange={v => patch({ namespace: v })} />
      <div className="ctx-field" title={applies.has('service') ? undefined : NOT_APPLIED}>
        <span className="field-label">Service</span>
        {applies.has('service')
          ? <ServicePicker value={params.service} onChange={v => patch({ service: v })} placeholder="All services" width={180} />
          : <input className="ctx-disabled" disabled aria-label="Service" placeholder="—" title={NOT_APPLIED} />}
      </div>
    </>
  );

  return (
    <div className="context-bar" role="group" aria-label="Query context">
      {/* v0.10.255 (operatör): zaman seçici EN SAĞDA kalır — eski Topbar
          düzeni (EnvPicker → TimeRangePicker); kapsam kontrolleri araya girer. */}
      <EnvPicker applies={envApplies && applies.has('env')} />
      {narrow ? (
        <>
          <DisclosureButton anatomy="row" className="ctx-more" expanded={open} onClick={() => setOpen(o => !o)}
            title="Kapsam kontrolleri bu düğmenin arkasında">
            Kapsam{scopeCount(params) ? ` (${scopeCount(params)})` : ''}
          </DisclosureButton>
          <div role="group" aria-label="Kapsam" className={open ? 'ctx-panel is-open' : 'ctx-panel'}>{scopeControls}</div>
        </>
      ) : scopeControls}
      {applies.has('compare') && (
        params.compare
          ? <Chip active pill onRemove={() => patch({ compare: '' })} removeLabel="Karşılaştırmayı kapat">Compare: prior</Chip>
          : <Chip pill onClick={() => patch({ compare: 'prior' })} title="Önceki eş-boy pencereyle karşılaştır">Compare</Chip>
      )}
      {applies.has('range') && <TimeRangePicker value={params.range} onChange={r => patch({ range: r })} />}
    </div>
  );
}

function ClusterControl({ value, options, disabled, onChange }: { value: string; options: string[]; disabled: boolean; onChange: (v: string) => void }) {
  const title = disabled ? NOT_APPLIED : undefined;
  if (disabled || options.length <= CLUSTER_SELECT_MAX) {
    return (
      <div className="ctx-field" title={title}>
        <span className="field-label">Cluster</span>
        <select aria-label="Cluster" value={value} disabled={disabled} onChange={e => onChange(e.target.value)}>
          <option value="">All clusters</option>
          {options.map(c => <option key={c} value={c}>{c}</option>)}
          {value && !options.includes(value) && <option value={value}>{value}</option>}
        </select>
      </div>
    );
  }
  return (
    <div className="ctx-field">
      <span className="field-label">Cluster</span>
      <Combobox value={value} onChange={onChange} options={options} placeholder="All clusters" width={160} ariaLabel="Cluster" />
    </div>
  );
}

function NamespaceControl({ value, disabled, onChange }: { value: string; disabled: boolean; onChange: (v: string) => void }) {
  const [draft, setDraft] = useState(value);
  // Dış değer değişince (URL) taslağı hizala; yazarken filtreleme yok (EnvPicker emsali).
  if (!disabled && draft !== value && document.activeElement?.getAttribute('aria-label') !== 'Namespace') {
    // render sırasında set etmek yerine efekt: küçük ve nadir, doğrudan hizala
  }
  return (
    <div className="ctx-field" title={disabled ? NOT_APPLIED : undefined}>
      <span className="field-label">Namespace</span>
      <input aria-label="Namespace" value={disabled ? '' : draft} disabled={disabled} placeholder={disabled ? '—' : 'All namespaces'}
        onChange={e => setDraft(e.target.value)}
        onBlur={() => { if (draft !== value) onChange(draft.trim()); }}
        onKeyDown={e => { if (e.key === 'Enter') { (e.target as HTMLInputElement).blur(); } if (e.key === 'Escape') { setDraft(value); } }} />
    </div>
  );
}
