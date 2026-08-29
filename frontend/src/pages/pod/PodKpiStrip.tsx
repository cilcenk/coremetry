// PodKpiStrip — v0.10.160 (A anatomisi §2). Pencere toplamları RED serilerinden
// (windowTotals — ayrı uç YOK) + Thanos anlık CPU/Mem/Restarts/Ağ (/api/clusters/pods
// satırı). Her tile'ın altında kaynak/sınır beyanı: KSM yoksa restarts '—'
// (0 değil), Ağ serisi yoksa tile hiç çizilmez, servis eşlenmemişse RED üçlüsü
// '—' ve sebebi yazar. StatTile atomu (v0.9.1375) — Pod'un yerel Stat kopyası
// (StatTile.tsx yorumundaki "6 kopya"nın biri) bu sürümde SİLİNDİ.
import { StatTile } from '@/components/ui/StatTile';
import { fmtNum, fmtBytes } from '@/lib/utils';
import { fmtCores } from '@/pages/clusters/thresholds';
import { THANOS_MAX_WINDOW_LABEL } from '@/lib/thanosWindow';
import type { ClusterPodRow } from '@/lib/types';
import type { WindowTotals } from './podPage';

export type RedState = 'off' | 'loading' | 'error' | 'ready';

function Sub({ children }: { children: string }) {
  return <span className="pod-kpi-sub" title={children}>{children}</span>;
}

function ms(v: number): string {
  return v < 10 ? `${v.toFixed(1)} ms` : `${Math.round(v)} ms`;
}

export function PodKpiStrip({ totals, redState, scopeLabel, row, rowPending, clamped }: {
  totals: WindowTotals | null;
  redState: RedState;
  /** "payments-orchestrator" ya da "bu pod'dan geçen tüm servisler" */
  scopeLabel: string;
  row?: ClusterPodRow;
  rowPending: boolean;
  clamped: boolean;
}) {
  const redSub = redState === 'off' ? 'servis eşlenmedi' : redState === 'error' ? 'metrikler yüklenemedi' : 'seçili pencere';
  const redVal = (v: string | null) => (redState === 'loading' ? '…' : redState !== 'ready' || v === null ? '—' : v);
  const calls = totals?.calls ?? null;
  const errPct = totals?.errPct ?? null;
  const avgMs = totals?.avgMs ?? null;
  const thanosSub = rowPending ? 'Thanos…' : row ? 'Thanos anlık' : 'Thanos\'ta seri yok';
  const errTone = errPct !== null && errPct >= 5 ? 'err' : errPct !== null && errPct >= 1 ? 'warn' : undefined;
  return (
    <div className="pod-sec">
      <div className="pod-kpis">
        <StatTile label="Çağrı">{redVal(calls === null ? null : fmtNum(Math.round(calls)))}<Sub>{redState === 'ready' && calls === null ? 'pencere kısa (tek nokta)' : redSub}</Sub></StatTile>
        <StatTile label="Hata %" tone={errTone}>{redVal(errPct === null ? (calls === 0 ? '—' : null) : `${errPct.toFixed(2)} %`)}<Sub>{redState === 'ready' && calls === 0 ? 'trafik yok' : redSub}</Sub></StatTile>
        <StatTile label="Ort. süre">{redVal(avgMs === null ? null : ms(avgMs))}<Sub>{redState === 'ready' ? 'messaging.system ≠ kafka' : redSub}</Sub></StatTile>
        <StatTile label="CPU">{row ? fmtCores(row.cpuCores) : rowPending ? '…' : '—'}<Sub>{row?.cpuPctOfReq !== undefined ? `%${row.cpuPctOfReq.toFixed(0)} of request${row.cpuLimitCores !== undefined ? ` · limit ${fmtCores(row.cpuLimitCores)}` : ''}` : thanosSub}</Sub></StatTile>
        <StatTile label="Mem">{row ? fmtBytes(row.memBytes) : rowPending ? '…' : '—'}<Sub>{row?.memPctOfReq !== undefined ? `%${row.memPctOfReq.toFixed(0)} of request${row.memLimitBytes !== undefined ? ` · limit ${fmtBytes(row.memLimitBytes)}` : ''}` : thanosSub}</Sub></StatTile>
        <StatTile label="Restarts" tone={row && !row.restartsUnknown && (row.restarts ?? 0) > 0 ? 'warn' : undefined}>
          {row ? (row.restartsUnknown ? '—' : String(row.restarts ?? 0)) : rowPending ? '…' : '—'}
          <Sub>{row ? (row.restartsUnknown ? 'KSM serisi yok' : row.lastTermReason ? `son: ${row.lastTermReason}` : 'KSM toplam sayaç') : thanosSub}</Sub>
        </StatTile>
        {row && row.netInBps !== undefined && (
          <StatTile label="Ağ">{`${fmtBytes(row.netInBps)}/s`}<Sub>{`↓ ${fmtBytes(row.netInBps)}/s · ↑ ${fmtBytes(row.netOutBps ?? 0)}/s`}</Sub></StatTile>
        )}
      </div>
      <div className="pod-cap">
        Çağrı / hata / süre: <b>{scopeLabel}</b>, seçili pencere (spanMetricBatch; toplamlar grafik serilerinden) · CPU / Mem / Restarts: Thanos anlık (<code className="mono">/api/clusters/pods</code>), KSM yoksa —
        {clamped && <> · Altyapı/JVM eksenleri son {THANOS_MAX_WINDOW_LABEL} ile kelepçeli</>}
      </div>
    </div>
  );
}
