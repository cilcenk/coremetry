import { useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Empty } from '@/components/Spinner';
import { TableSkeleton } from '@/components/Skeleton';
import { Button, Field } from '@/components/ui';
import { CopilotExplain } from '@/components/CopilotExplain';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import { api } from '@/lib/api';
import { useServicesMetadata } from '@/lib/queries';
import { teamOptionsCI } from '@/lib/teamOptions';
import type { DataTableColumn } from '@/lib/dataTable';
import type {
  DeploymentReport, ServiceReportSection, Problem, AnomalyEvent, ExceptionGroup,
} from '@/lib/types';

// /deployment-report — fleet-wide, on-demand. Given a deploy timestamp,
// shows every service that still has an open Problem which started
// after that timestamp, plus that service's still-active anomalies,
// still-open new errors, and a before/after RED comparison. No
// per-service selection (holistic by design) and no polling — this is
// a point-in-time computed view, fetched only when the operator asks
// for it via "Generate Report".
export default function DeploymentReportPage() {
  const [params, setParams] = useSearchParams();
  const sinceParam = params.get('since'); // unix ns, string
  // Owner (ug-team) / SRE (sy-team) team filter — same ?owner=/?sre=
  // URL params and semantics as the Problems inbox (v0.8.310): narrows
  // to services belonging to the picked team, resolved server-side so
  // it's correct against the full qualifying set, not just loaded rows.
  // Unlike the deploy timestamp, a team pick re-fetches immediately —
  // it's a cheap narrowing filter on an already-generated report, not
  // a new report generation.
  const ownerTeam = params.get('owner') || '';
  const sreTeam   = params.get('sre')   || '';
  const setTeam = (key: 'owner' | 'sre', v: string) => setParams(prev => {
    const next = new URLSearchParams(prev);
    if (v) next.set(key, v); else next.delete(key);
    return next;
  }, { replace: true });

  // Team dropdown options come from the service catalog, not the
  // loaded report, so a pick never collapses the list of teams to
  // choose from — same source the Problems/Services pages use.
  const catalogQ = useServicesMetadata();
  const ownerTeamOptions = useMemo(
    () => teamOptionsCI(Object.values(catalogQ.data ?? {}).map(m => m.ownerTeam)),
    [catalogQ.data]);
  const sreTeamOptions = useMemo(
    () => teamOptionsCI(Object.values(catalogQ.data ?? {}).map(m => m.sreTeam)),
    [catalogQ.data]);

  // datetime-local has no native ns precision — the input works in local
  // ms, converted to ns (matching the codebase-wide absolute-timestamp
  // convention) only when the operator submits.
  const [pendingLocal, setPendingLocal] = useState(() => {
    if (!sinceParam) return '';
    const ms = Number(sinceParam) / 1_000_000;
    const d = new Date(ms - new Date().getTimezoneOffset() * 60000);
    return d.toISOString().slice(0, 16);
  });

  const generate = () => {
    if (!pendingLocal) return;
    const ms = new Date(pendingLocal).getTime();
    const ns = Math.round(ms * 1_000_000);
    setParams(prev => {
      const next = new URLSearchParams(prev);
      next.set('since', String(ns));
      return next;
    }, { replace: true });
  };

  const sinceNs = sinceParam ? Number(sinceParam) : null;

  const [report, setReport] = useState<DeploymentReport | null | undefined>(undefined);
  useMemo(() => {
    if (sinceNs === null) { setReport(undefined); return; }
    setReport(undefined);
    api.deploymentReport(sinceNs, { ownerTeam, sreTeam })
      .then(r => setReport(r))
      .catch(() => setReport(null));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sinceNs, ownerTeam, sreTeam]);

  return (
    <>
      <Topbar title="Deployment analysis report" />
      <div id="content">
        <div style={{ color: 'var(--text2)', fontSize: 12, marginBottom: 12 }}>
          Pick the timestamp your deployment succeeded. The report shows every
          service that still has an open problem which started after that
          moment — plus its still-active anomalies, still-open new errors, and
          a before/after health comparison. Fleet-wide, no individual service
          picker — narrow by owner/SRE team instead.
        </div>

        <div className="controls" style={{ marginBottom: 16, alignItems: 'flex-end', gap: 12 }}>
          <Field
            label="Deployment succeeded at"
            type="datetime-local"
            value={pendingLocal}
            onChange={e => setPendingLocal(e.target.value)}
          />
          <Button variant="primary" size="sm" onClick={generate} disabled={!pendingLocal}>
            Generate report
          </Button>
          {/* Owner (ug-team) / SRE (sy-team) team filter — plain <select>
              for these small catalog-derived sets (frontend-conventions
              §3), resolved server-side so the narrowing is correct
              across the whole qualifying set, not just the loaded page. */}
          <select value={ownerTeam} onChange={e => setTeam('owner', e.target.value)}
            aria-label="Filter by owner team" style={{ minWidth: 130 }}>
            <option value="">All owner teams</option>
            {ownerTeamOptions.map(t => <option key={t} value={t}>{t}</option>)}
          </select>
          <select value={sreTeam} onChange={e => setTeam('sre', e.target.value)}
            aria-label="Filter by SRE team" style={{ minWidth: 130 }}>
            <option value="">All SRE teams</option>
            {sreTeamOptions.map(t => <option key={t} value={t}>{t}</option>)}
          </select>
        </div>

        {sinceNs === null && (
          <Empty icon="◷" title="Pick a deployment timestamp to generate the report" />
        )}
        {sinceNs !== null && report === undefined && <TableSkeleton cols={6} wideFirst />}
        {sinceNs !== null && report === null && (
          <Empty icon="✗" title="Failed to load the deployment report" />
        )}
        {sinceNs !== null && report && report.services.length === 0 && (
          <Empty icon="✓" title="No open problems since this deployment">
            {ownerTeam || sreTeam
              ? 'No qualifying service matches the selected team filter — try clearing it or picking another team.'
              : 'Every service is clean relative to the timestamp you picked.'}
          </Empty>
        )}
        {sinceNs !== null && report && report.services.length > 0 && (
          <ReportBody report={report} />
        )}
      </div>
    </>
  );
}

function fmtPct(n: number) { return `${n.toFixed(1)}%`; }
function fmtMs(n: number) { return `${n.toFixed(0)}ms`; }
function fmtRps(n: number) { return `${n.toFixed(2)}/s`; }
function healthBadgeClass(h: string) {
  return h === 'red' ? 'b-err' : h === 'yellow' ? 'b-warn' : h === 'green' ? 'b-ok' : 'b-gray';
}

// Flattened row shapes — one row per (service, item) pair — so each of
// the three signal tables is a single fixed useDataTable instance
// regardless of how many services qualify (React hooks can't be called
// a variable number of times per render).
type ProblemRow = Problem & { __service: string };
type AnomalyRow = AnomalyEvent & { __service: string };
type ErrorRow = ExceptionGroup & { __service: string };

const SERVICE_COLS: DataTableColumn<ServiceReportSection>[] = [
  { id: 'service', label: 'Service', sortValue: r => r.service, naturalDir: 'asc', width: 200 },
  { id: 'health', label: 'Health', sortValue: r => r.health, naturalDir: 'asc', width: 90 },
  { id: 'errBefore', label: 'Err% before', sortValue: r => r.before.errorRate, numeric: true, width: 100 },
  { id: 'errAfter', label: 'Err% after', sortValue: r => r.after.errorRate, numeric: true, width: 100 },
  { id: 'p99Before', label: 'P99 before', sortValue: r => r.before.p99Ms, numeric: true, width: 100 },
  { id: 'p99After', label: 'P99 after', sortValue: r => r.after.p99Ms, numeric: true, width: 100 },
  { id: 'thBefore', label: 'Throughput before', sortValue: r => r.before.throughput, numeric: true, width: 130 },
  { id: 'thAfter', label: 'Throughput after', sortValue: r => r.after.throughput, numeric: true, width: 130 },
];

const PROBLEM_COLS: DataTableColumn<ProblemRow>[] = [
  { id: 'service', label: 'Service', sortValue: r => r.__service, naturalDir: 'asc', width: 160 },
  { id: 'severity', label: 'Severity', sortValue: r => r.severity, naturalDir: 'asc', width: 90 },
  { id: 'priority', label: 'Priority', sortValue: r => r.priority ?? 'P3', naturalDir: 'asc', width: 80 },
  { id: 'ruleName', label: 'Rule', sortValue: r => r.ruleName, naturalDir: 'asc', width: 220 },
  { id: 'startedAt', label: 'Started', sortValue: r => r.startedAt, width: 160 },
];

const ANOMALY_COLS: DataTableColumn<AnomalyRow>[] = [
  { id: 'service', label: 'Service', sortValue: r => r.__service, naturalDir: 'asc', width: 160 },
  { id: 'kind', label: 'Kind', sortValue: r => r.kind, naturalDir: 'asc', width: 120 },
  { id: 'pattern', label: 'Pattern', sortValue: r => r.pattern, naturalDir: 'asc', width: 280 },
  { id: 'startedAt', label: 'Started', sortValue: r => r.startedAt, width: 160 },
  { id: 'currentRatio', label: 'Current ratio', sortValue: r => r.currentRatio, numeric: true, width: 110 },
];

const ERROR_COLS: DataTableColumn<ErrorRow>[] = [
  { id: 'service', label: 'Service', sortValue: r => r.__service, naturalDir: 'asc', width: 160 },
  { id: 'type', label: 'Type', sortValue: r => r.type, naturalDir: 'asc', width: 200 },
  { id: 'message', label: 'Message', sortValue: r => r.message, naturalDir: 'asc', width: 320 },
  { id: 'firstSeen', label: 'First seen', sortValue: r => r.firstSeen, width: 160 },
  { id: 'occurrences', label: 'Occurrences', sortValue: r => r.occurrences, numeric: true, width: 100 },
];

function ReportBody({ report }: { report: DeploymentReport }) {
  const problemRows: ProblemRow[] = report.services.flatMap(
    s => s.problems.map(p => ({ ...p, __service: s.service })));
  const anomalyRows: AnomalyRow[] = report.services.flatMap(
    s => s.anomalies.map(a => ({ ...a, __service: s.service })));
  const errorRows: ErrorRow[] = report.services.flatMap(
    s => s.newErrors.map(e => ({ ...e, __service: s.service })));

  const svcDt = useDataTable<ServiceReportSection>({
    storageKey: 'deployment-report-services', columns: SERVICE_COLS,
    rows: report.services, initialSort: { id: 'health', dir: 'desc' },
  });
  const problemDt = useDataTable<ProblemRow>({
    storageKey: 'deployment-report-problems', columns: PROBLEM_COLS,
    rows: problemRows, initialSort: { id: 'startedAt', dir: 'desc' },
  });
  const anomalyDt = useDataTable<AnomalyRow>({
    storageKey: 'deployment-report-anomalies', columns: ANOMALY_COLS,
    rows: anomalyRows, initialSort: { id: 'startedAt', dir: 'desc' },
  });
  const errorDt = useDataTable<ErrorRow>({
    storageKey: 'deployment-report-errors', columns: ERROR_COLS,
    rows: errorRows, initialSort: { id: 'firstSeen', dir: 'desc' },
  });

  return (
    <>
      <h3 style={{ fontSize: 13, margin: '18px 0 8px' }}>
        Affected services ({report.services.length})
      </h3>
      <div className="table-wrap">
        <table style={{ tableLayout: 'fixed', width: '100%' }}>
          <DataTableColgroup dt={svcDt} />
          <DataTableHead dt={svcDt} />
          <tbody>
            {svcDt.sortedRows.map(s => (
              <tr key={s.service}>
                <td style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>{s.service}</td>
                <td><span className={`badge ${healthBadgeClass(s.health)}`}>{s.health || 'n/a'}</span></td>
                <td className="num mono">{fmtPct(s.before.errorRate)}</td>
                <td className="num mono" style={{ color: s.after.errorRate > s.before.errorRate ? 'var(--err)' : undefined }}>
                  {fmtPct(s.after.errorRate)}
                </td>
                <td className="num mono">{fmtMs(s.before.p99Ms)}</td>
                <td className="num mono" style={{ color: s.after.p99Ms > s.before.p99Ms ? 'var(--err)' : undefined }}>
                  {fmtMs(s.after.p99Ms)}
                </td>
                <td className="num mono">{fmtRps(s.before.throughput)}</td>
                <td className="num mono">{fmtRps(s.after.throughput)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <h3 style={{ fontSize: 13, margin: '18px 0 8px' }}>
        Problems since deploy ({problemRows.length})
      </h3>
      <div className="table-wrap">
        <table style={{ tableLayout: 'fixed', width: '100%' }}>
          <DataTableColgroup dt={problemDt} trailing={[160]} />
          <DataTableHead dt={problemDt} trailing={<th>AI review</th>} />
          <tbody>
            {problemDt.sortedRows.map(p => (
              <tr key={p.id}>
                <td style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>{p.__service}</td>
                <td><span className="badge b-gray">{p.severity}</span></td>
                <td>{p.priority ?? 'P3'}</td>
                <td>{p.ruleName}</td>
                <td>{new Date(p.startedAt / 1_000_000).toLocaleString()}</td>
                <td><CopilotExplain kind="problem" id={p.id} label="AI review" /></td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <h3 style={{ fontSize: 13, margin: '18px 0 8px' }}>
        Anomalies since deploy ({anomalyRows.length})
      </h3>
      {anomalyRows.length === 0 ? (
        <Empty icon="◇" title="No active anomalies since this deploy on the affected services" />
      ) : (
        <div className="table-wrap">
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={anomalyDt} />
            <DataTableHead dt={anomalyDt} />
            <tbody>
              {anomalyDt.sortedRows.map(a => (
                <tr key={a.id}>
                  <td style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>{a.__service}</td>
                  <td><span className="badge b-gray">{a.kind}</span></td>
                  <td>{a.pattern}</td>
                  <td>{new Date(a.startedAt / 1_000_000).toLocaleString()}</td>
                  <td className="num mono">{a.currentRatio.toFixed(2)}x</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <h3 style={{ fontSize: 13, margin: '18px 0 8px' }}>
        New errors since deploy ({errorRows.length})
      </h3>
      {errorRows.length === 0 ? (
        <Empty icon="◇" title="No new errors since this deploy on the affected services" />
      ) : (
        <div className="table-wrap">
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={errorDt} />
            <DataTableHead dt={errorDt} />
            <tbody>
              {errorDt.sortedRows.map(e => (
                <tr key={e.fingerprint}>
                  <td style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>{e.__service}</td>
                  <td>{e.type}</td>
                  <td style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={e.message}>
                    {e.message}
                  </td>
                  <td>{new Date(e.firstSeen / 1_000_000).toLocaleString()}</td>
                  <td className="num mono">{e.occurrences}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}
