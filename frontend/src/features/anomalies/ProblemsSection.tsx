// ProblemsSection — the "Alert rules" surface (threshold + SLO burn
// detectors the evaluator has opened) plus the ?problem=<id> full-page
// host that resolves a notification deep link.
//
// v0.9.837 (operator-reported): the section used to live inline in
// AnomaliesPage (/problems, "Exceptions"). The operator reads alert
// rules as part of the triage queue, not the exception queue, so the
// section moved to /inbox ("Problems") and this file is the shared
// home for both consumers.
//
// The ?problem= page-level host STAYS mounted on /problems as well:
// every notification e-mail links to /problems?problem=<id>
// (notify.problemURL / api.problemLink), and that contract is locked
// by problem_url_test.go / email_html_test.go / webhook_template_test.go.
//
// Imported by its module path (NOT the features/anomalies barrel):
// the barrel re-exports AnomaliesPage, which would drag the whole
// Exceptions page into the /inbox chunk.
import { useEffect, useMemo, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import EvaluatorStatus from './EvaluatorStatus';
import { useAuth } from '@/components/AuthProvider';
import { ClusterChips } from '@/components/ClusterChips';
import { RootCauseRibbon } from '@/components/RootCauseRibbon';
import { serviceHref, eventLifespanWindow } from '@/lib/serviceHref';
import { ArrowDownToLine, Users, CornerDownRight } from 'lucide-react';
import { Button } from '@/components/ui/Button';
import { IconBell, IconSparkles } from '@/components/icons';
import { useProblems, useProblemByID, useServicesMetadata, keys } from '@/lib/queries';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { fmtFixed, tsLong } from '@/lib/utils';
import { teamOptionsCI } from '@/lib/teamOptions';
import { getItem, setItem, STORAGE_KEYS } from '@/lib/storage';
import { useUrlEnv } from '@/lib/useUrlEnv';
import { useDataTable, DataTableColgroup, DataTableHead } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type { Problem } from '@/lib/types';
import { AlertProblemDetail } from './ProblemDetail';
import { withProblemParam } from './problemLink';
import { findProblemInCaches } from './problemResolve';
import { RenderedMarkdown, stripMarkdown } from '@/components/Markdown';

// Problems-specific severity + priority ordering.
const SEV_RANK: Record<string, number> = { critical: 3, warning: 2, info: 1 };
// P1 ranks above P2 ranks above P3 (lower number = more urgent).
const PRIO_RANK: Record<string, number> = { P1: 3, P2: 2, P3: 1 };

// Severity + priority filter chips render via the shared .facet
// primitive (globals.css, v0.8.38) — active = --accent-bg/--accent-
// border; the f-err/f-warn tints keep the urgency cue at rest. The
// old per-chip color-mix palette (v0.5.469) was replaced when these
// moved onto the shared facetbar in v0.8.39.

// Alert-rules columns — shared DataTable primitive, CLIENT sort: the
// section is a single capped fetch (limit 200, no pager), so the rows
// being sorted are the fully loaded set. The priority accessor keeps
// the old cmp's composite ordering — priority bucket, then severity,
// then start time — with each term scaled so it can't cross into the
// next (startedAt/1e9 stays < 1e10 until year 2286).
const PROBLEM_COLS: DataTableColumn<Problem>[] = [
  { id: 'priority', label: 'Priority',
    sortValue: p => (PRIO_RANK[p.priority ?? 'P3'] ?? 0) * 1e11
                  + (SEV_RANK[p.severity] ?? 0) * 1e10
                  + p.startedAt / 1e9,
    width: 90 },
  { id: 'severity', label: 'Severity', sortValue: p => SEV_RANK[p.severity] ?? 0, width: 90 },
  { id: 'service',  label: 'Service',  sortValue: p => p.service,   naturalDir: 'asc', width: 170 },
  { id: 'metric',   label: 'Metric',   sortValue: p => p.metric,    naturalDir: 'asc', width: 150 },
  { id: 'value',    label: 'Value',    sortValue: p => p.value,     numeric: true,     width: 110 },
  { id: 'rule',     label: 'Rule',     sortValue: p => p.ruleName,  naturalDir: 'asc', flex: true },
  { id: 'started',  label: 'Started',  sortValue: p => p.startedAt, width: 150 },
  { id: 'status',   label: 'Status',   sortValue: p => p.status,    naturalDir: 'asc', width: 100 },
];

// ProblemsSection — embeds the former /problems page table inline.
// Polls via useProblems (30s default), supports status filter +
// column sort + j/k row nav. Single section per the merged
// Exceptions page UX.
export function ProblemsSection({ serviceFilter }: { serviceFilter: string }) {
  const { user } = useAuth();
  const currentUserEmail = user?.email ?? '';
  const [searchParams, setSearchParams] = useSearchParams();
  // When arriving via ?problem=<id> deep link, broaden the
  // status pivot so the drawer can resolve the row even when
  // it's acknowledged / resolved. Default 'open' otherwise.
  const [statusFilter, setStatusFilter] = useState<'open' | 'all' | 'resolved'>(
    searchParams.get('problem') ? 'all' : 'open');
  // v0.8.428 (operator-reported): the list stays the CLASSIC sortable
  // table; only the triage surface is the Variant-B full-page detail,
  // driven by ?problem= alone (page-level host reads it; this writes).
  // v0.8.256 contract preserved: state and URL move together,
  // replace:true so triage clicks don't pile history entries.
  const openDetail = (id: string | null) => {
    setSearchParams(prev => withProblemParam(prev, id), { replace: true });
  };
  // Bulk-select state (v0.5.83). Operators can multi-select
  // problems and acknowledge them in one POST — typical
  // workflow during a fan-out incident where 20 alerts fire
  // from the same root cause and the oncall wants to mute
  // them all once they've started fixing.
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const [bulkBusy, setBulkBusy] = useState(false);
  // Severity filter — multi-select chip row above the table.
  // Persisted to localStorage so an operator who keeps the
  // "critical only" filter stays at that scope across page
  // reloads (typical incident workflow). Default: all three on.
  const [sevSet, setSevSet] = useState<Set<string>>(() => {
    const arr = getItem<string[] | null>(STORAGE_KEYS.problemsSev, null);
    if (Array.isArray(arr) && arr.length > 0) return new Set(arr);
    return new Set(['critical', 'warning', 'info']);
  });
  // Priority filter — defaults to P1+P2 so the operator's inbox
  // surfaces signal first. P3 (steady warnings) is one click
  // away. Persisted alongside the severity set.
  const [prioSet, setPrioSet] = useState<Set<string>>(() => {
    const arr = getItem<string[] | null>(STORAGE_KEYS.problemsPrio, null);
    if (Array.isArray(arr) && arr.length > 0) return new Set(arr);
    return new Set(['P1', 'P2']);
  });
  const togglePrio = (p: string) => {
    setPrioSet(prev => {
      const next = new Set(prev);
      if (next.has(p)) {
        if (next.size === 1) return prev;
        next.delete(p);
      } else {
        next.add(p);
      }
      setItem(STORAGE_KEYS.problemsPrio, [...next]);
      return next;
    });
  };
  const toggleSev = (s: string) => {
    setSevSet(prev => {
      const next = new Set(prev);
      if (next.has(s)) {
        // Don't let the operator clear all three — that
        // empties the table and looks broken. Last
        // selected stays on.
        if (next.size === 1) return prev;
        next.delete(s);
      } else {
        next.add(s);
      }
      setItem(STORAGE_KEYS.problemsSev, [...next]);
      return next;
    });
  };
  // Owner-team / SRE-team filters (v0.8.290) — mirror the Services
  // page. Plain local state (NOT URL-backed): the page's other
  // filter axes (status / severity / priority) are all local too, so
  // URL-backing only these two would diverge; only the triage drawer
  // (?problem=) is URL-backed here. Empty value = "all", passed to
  // the server which filters with the same EqualFold / empty-means-
  // all semantics as /inbox. Options come from the service catalog
  // (like Services) so the dropdown stays stable when a selection
  // narrows the server-filtered rows — deriving them from the
  // already-filtered result would collapse the list to the pick.
  const [ownerTeam, setOwnerTeam] = useState('');
  const [sreTeam, setSreTeam] = useState('');
  const [cluster, setCluster] = useState(''); // v0.9.181 — cluster filtresi (server-side, p.clusters)
  const catalogQ = useServicesMetadata();
  // v0.8.330 — case-insensitive team options (see teamOptionsCI).
  const ownerTeamOptions = useMemo(
    () => teamOptionsCI(Object.values(catalogQ.data ?? {}).map(m => m.ownerTeam)),
    [catalogQ.data]);
  const sreTeamOptions = useMemo(
    () => teamOptionsCI(Object.values(catalogQ.data ?? {}).map(m => m.sreTeam)),
    [catalogQ.data]);

  // (Pre-v0.5.80 inline "Why?" expansion lived here; the
  // same correlation panel is now embedded inside the
  // triage drawer.)

  // Sort the priority set before handing to the query so the React
  // Query key hash stays stable regardless of toggle order, and the
  // backend cache key (sorted+FNV digest) matches.
  const prioParam = useMemo(() => [...prioSet].sort(), [prioSet]);
  // Global env picker (v0.8.387 — env-separation Phase 3). SERVICE-
  // scoped on problems: the server keeps rows whose service ran in
  // the env in the last hour (+ service-less global alerts) — a
  // problem row carries no env of its own. The hint chip below spells
  // that out so the semantics aren't mistaken for a per-env value.
  const [env] = useUrlEnv();
  const problemsQ = useProblems({
    status: statusFilter === 'all' ? undefined : statusFilter,
    service: serviceFilter || undefined,
    priority: prioParam,
    ownerTeam: ownerTeam || undefined,
    sreTeam: sreTeam || undefined,
    env: env || undefined,
    cluster: cluster || undefined,
    limit: 200,
  });
  const data: Problem[] | null | undefined = problemsQ.isLoading
    ? undefined
    : problemsQ.isError
      ? null
      : (problemsQ.data?.items ?? []);
  // v0.9.455 (dürüstlük A3) — 200'lük aday tavanı dolduğunda sayfa artık
  // söylüyor; total=-1 = bilinmiyor (takım/cluster daraltması COUNT'a
  // inemiyor), o durumda şerit sayısız konuşur.
  const probTruncated = problemsQ.data?.truncated ?? false;
  const probTotal = problemsQ.data?.total ?? -1;
  // v0.9.344 — chip counts come from the UNFILTERED set, server-side.
  //
  // They used to be `data.filter(...)`, and `data` is the response to a
  // request that already carried ?priority=. So with the default P1+P2
  // selection the P3 chip read 0 forever and an operator could not discover
  // that P3 problems existed — the comment above the chips even claimed
  // "Counts reflect the unfiltered set". Same defect as v0.9.330 on the
  // merged queue, on the page that kept the old name.
  //
  // /api/problems/buckets was built for exactly this and had never been
  // called (orphaned since the priority buckets shipped): it takes no
  // priority param by design and is server-cached 5s on a shared key. It
  // mirrors every OTHER filter, so the counts describe the same population
  // the rows are drawn from.
  // v0.9.474 (dürüstlük A16) — team/cluster chip'lere de iner: yorumun
  // "mirrors every OTHER filter" iddiası bu ikisi için yanlıştı.
  const bucketsQ = useQuery({
    queryKey: ['problem-buckets', statusFilter, serviceFilter, env, ownerTeam, sreTeam, cluster],
    queryFn: () => api.problemBuckets({
      status: statusFilter === 'all' ? undefined : statusFilter,
      service: serviceFilter || undefined,
      env: env || undefined,
      ownerTeam: ownerTeam || undefined,
      sreTeam: sreTeam || undefined,
      cluster: cluster || undefined,
    }),
    staleTime: 5_000,
    refetchInterval: 30_000,
  });
  // Cluster filter options (v0.9.181) — distinct clusters across the loaded
  // problems. The narrowing itself runs server-side over the FULL set (not
  // page-limited); these options are just the picker's convenience list.
  const clusterOptions = useMemo(
    () => [...new Set((data ?? []).flatMap(p => p.clusters ?? []))].filter(Boolean).sort(),
    [data]);

  const open = data?.filter(p => p.status === 'open').length ?? 0;
  const resolved = data?.filter(p => p.status === 'resolved').length ?? 0;

  // Severity chip filter stays client-side; priority is filtered
  // server-side via the priority query param so the limit cap
  // bites the right bucket. Keeping the severity client-side
  // filter avoids a refetch on every chip toggle for that axis.
  const rows = useMemo(
    () => (data ?? []).filter(p => sevSet.has(p.severity)),
    [data, sevSet]);
  // Shared DataTable primitive, client sort (PROBLEM_COLS carries the
  // per-column accessors + natural directions the old cmp/toggleSort
  // pair encoded). Sort + widths persist under 'alert-rules'; the URL
  // param is `s_alert-rules`, namespaced so it can't collide with the
  // exception inbox's `s_exception-inbox` on the same page.
  // v0.9.929 — klavye gezinmesi. v0.9.918'de bilinçli ERTELENMİŞTİ: /inbox'ta
  // bu tablo ikinci nav'lı tablo olarak mount oluyor ve o gün j/k'nın sahibi
  // "son mount olan"dı — onOpen vermek üstteki kuyruğun gezinmesini çalardı.
  // v0.9.928 arbitrajı (sahip = son etkileşim) o riski kaldırdı.
  //
  // Enter'ı satırın KENDİ onKeyDown'u da işliyor (role="button" + tabIndex);
  // ikisi çakışmıyor çünkü isActivationTarget (v0.9.863) odaklı bir
  // role="button" üzerindeyken global Enter binding'ini susturuyor. Yani
  // odak satırdaysa satır açar, odak hiçbir yerdeyken j/k seçimi açar.
  const dt = useDataTable<Problem>({
    storageKey: 'alert-rules',
    columns: PROBLEM_COLS,
    rows,
    initialSort: { id: 'priority', dir: 'desc' },
    onOpen: (p) => openDetail(p.id),
  });
  // Preserve the tri-state contract (undefined loading / null error /
  // rows) the render below branches on.
  const sorted = data == null ? data : dt.sortedRows;

  // Counts per severity for the chip labels — operator sees
  // "critical (3)" instead of guessing how many would land.
  const sevCounts = useMemo(() => {
    const counts = { critical: 0, warning: 0, info: 0 } as Record<string, number>;
    for (const p of data ?? []) counts[p.severity] = (counts[p.severity] ?? 0) + 1;
    return counts;
  }, [data]);

  // Whole section collapses when there's nothing AND filter is
  // 'open' — no point in dead space when the operator's most-
  // common scan finds zero firing rules.
  if (statusFilter === 'open' && data && data.length === 0) {
    return (
      <div style={{ marginTop: 22, marginBottom: 12 }}>
        <SectionHeader title="Alert rules" subtitle="Threshold + SLO burn detectors" />
        <Empty icon="✓" title="No open alerts — all clear!">
          {/* v0.9.550 — buradaki eski metin "The evaluator runs once per
              minute" diyordu ve bu ÖLÇÜLMEMİŞ bir iddiaydı: evaluator ölü
              olsa da sayfa aynı cümleyi ✓ ikonuyla kurardı. Artık iddia
              yerine ölçüm var; "all clear" ancak evaluator gerçekten
              koşuyorsa güvenilir bir cümle. */}
          Built-in rules cover error rate and P99 latency.
          <EvaluatorStatus compact />
        </Empty>
      </div>
    );
  }

  return (
    <div style={{ marginTop: 22, marginBottom: 12 }}>
      <SectionHeader title="Alert rules" subtitle="Threshold + SLO burn detectors" />
      {/* v0.9.550 — dolu listede de görünür. Bayat bir evaluator'ın
          ESKİ problemleri göstermesi, boş sayfa kadar yanıltıcı:
          operatör ekranda gördüğünü "şu anki durum" sanar. */}
      <EvaluatorStatus />
      {/* One grouped facet bar (v0.8.39) — status pivot + severity +
          priority chips share the shared .facet primitive (the repo
          equivalent of the design's filter bar), replacing the old
          per-row ad-hoc inline-styled chips so the Alert-rules filters
          read with the same visual language as the rest of the app.
          Handlers + state are unchanged: status pivot single-select via
          setStatusFilter; severity/priority multi-select via toggleSev/
          togglePrio. Count + manage-rules link stay pushed right. */}
      <div className="facetbar" style={{ marginBottom: 14 }}>
        {/* Status pivot — single-select */}
        {(['open', 'resolved', 'all'] as const).map(s => (
          <span key={s} onClick={() => setStatusFilter(s)}
            className={`facet${statusFilter === s ? ' on' : ''}`}>
            {s.charAt(0).toUpperCase() + s.slice(1)}
          </span>
        ))}
        {/* Severity chip filter — multi-select toggle. Counts reflect
            the unfiltered status-tab result so the operator sees how
            many would land if they toggle a chip back on. At least one
            chip stays on at all times (toggleSev guard) — empty table
            looks broken. Severity tint (f-err/f-warn) keeps the urgency
            cue even when the chip is off. */}
        {(['critical', 'warning', 'info'] as const).map(s => {
          const on = sevSet.has(s);
          const tint = s === 'critical' ? ' f-err' : s === 'warning' ? ' f-warn' : '';
          return (
            <span key={s} onClick={() => toggleSev(s)}
              title={on ? `Hide ${s}` : `Show ${s} only — click again to add`}
              className={`facet${tint}${on ? ' on' : ''}`}>
              {s} <span className="n">{sevCounts[s] ?? 0}</span>
            </span>
          );
        })}
        {/* Priority chip filter (v0.5.210) — defaults to P1+P2 so the
            operator's first paint is signal, not noise. Click P3 to
            widen. Counts reflect the unfiltered set — and since v0.9.344
            they actually do: they come from /api/problems/buckets, which
            takes no priority param. Deriving them from `data` meant the
            deselected chip could only ever read 0. */}
        {(['P1', 'P2', 'P3'] as const).map(pp => {
          const on = prioSet.has(pp);
          const tint = pp === 'P1' ? ' f-err' : pp === 'P2' ? ' f-warn' : '';
          const count = bucketsQ.data?.priority?.[pp]
            ?? (data?.filter(d => (d.priority ?? 'P3') === pp).length ?? 0);
          return (
            <span key={pp} onClick={() => togglePrio(pp)}
              title={on ? `Hide ${pp}` : `Show ${pp}`}
              className={`facet${tint}${on ? ' on' : ''}`}>
              {pp} <span className="n">{count}</span>
            </span>
          );
        })}
        {/* Owner / SRE team filters (v0.8.290) — mirror the Services
            page. Plain <select> for these small catalog-derived sets
            (frontend-conventions §3 permits <select> for ≤~10 fixed
            values). Server resolves the pick with the same EqualFold
            / empty-means-all filter the inbox uses, so the narrowing
            is correct across the whole result, not just the loaded
            rows. Options come from the catalog so they stay stable
            when a pick narrows the list. */}
        <select value={ownerTeam}
          onChange={e => setOwnerTeam(e.target.value)}
          aria-label="Filter by owner team"
          style={{ minWidth: 130 }}>
          <option value="">All owner teams</option>
          {ownerTeamOptions.map(t => (
            <option key={t} value={t}>{t}</option>
          ))}
        </select>
        <select value={sreTeam}
          onChange={e => setSreTeam(e.target.value)}
          aria-label="Filter by SRE team"
          style={{ minWidth: 130 }}>
          <option value="">All SRE teams</option>
          {sreTeamOptions.map(t => (
            <option key={t} value={t}>{t}</option>
          ))}
        </select>
        {/* Cluster filter (v0.9.181) — narrows to problems whose service
            touched the picked cluster (server-side over the full set). Only
            shown when the loaded problems carry cluster labels. */}
        {(clusterOptions.length > 0 || cluster) && (
          <select value={cluster}
            onChange={e => setCluster(e.target.value)}
            aria-label="Filter by cluster"
            style={{ minWidth: 130 }}>
            <option value="">All clusters</option>
            {clusterOptions.map(c => (
              <option key={c} value={c}>{c}</option>
            ))}
          </select>
        )}
        {/* Env hint chip (v0.8.387) — non-interactive: the pick lives in
            the Topbar EnvPicker; this only surfaces the SEMANTICS so an
            operator doesn't read the rows as per-env values. */}
        {env && (
          <span className="badge b-info" style={{ cursor: 'help' }}
            title={`Showing problems on services seen in "${env}" during the last hour (global environment picker). Problems carry no environment of their own — a problem on a multi-env service still shows, and service-less (global) alerts always show. The exception inbox above is not env-scoped yet.`}>
            env: {env} — service-scoped
          </span>
        )}
        <span style={{ marginLeft: 'auto', color: 'var(--text3)', fontSize: 12 }}
          title={probTruncated ? 'Sayımlar görünen sayfa üzerinden — liste kırpıldı, tam envanter için aşağıdaki şeride bak' : undefined}>
          {open} open · {resolved} resolved{probTruncated ? ' (sayfada)' : ''}
        </span>
        <Link to="/alerts" className="sec" style={{
          textDecoration: 'none', padding: '5px 12px',
          border: '1px solid var(--border)', borderRadius: 6, fontSize: 12, color: 'var(--text)',
          display: 'inline-flex', alignItems: 'center', gap: 6,
        }}><IconBell /> <span>Manage alert rules</span></Link>
      </div>

      {/* v0.9.455 (dürüstlük A3) — aday tavanı dolduysa söyle: eskiden
          footer "200 open" derken sidebar rozeti gerçek COUNT'la
          çelişiyor ve hiçbir şey kırpmayı söylemiyordu. total=-1 =
          bilinmiyor (takım/cluster daraltması COUNT'a inemiyor). */}
      {probTruncated && (
        <div style={{
          margin: '8px 0', padding: '6px 10px', borderRadius: 6, fontSize: 12,
          background: 'var(--bg2)', border: '1px solid var(--border)', color: 'var(--text2)',
        }}>
          ⚠ Aday tavanı doldu — {probTotal >= 0
            ? `ilk ${data?.length ?? 0} satır gösteriliyor, bu daraltmada toplam ${probTotal.toLocaleString()} problem var`
            : `ilk ${data?.length ?? 0} satır gösteriliyor; takım/cluster süzgeci altında toplam sayı hesaplanamıyor`}.
          Daraltmayı sıkılaştır (servis, durum, öncelik) ya da sayıları /inbox rozetinden izle.
        </div>
      )}
      {data === undefined && <Spinner />}
      {data && sorted && sorted.length === 0 && (
        <Empty icon="✓" title={`No problems in "${statusFilter}"`}>
          Switch the filter above to see other states.
        </Empty>
      )}
      {sorted && sorted.length > 0 && selectedIds.size > 0 && (
        <div style={{
          padding: '8px 12px', marginBottom: 8,
          borderRadius: 6, background: 'var(--bg2)',
          border: '1px solid var(--accent2)',
          display: 'flex', alignItems: 'center', gap: 10,
          fontSize: 12,
        }}>
          <span style={{ color: 'var(--accent2)', fontWeight: 600 }}>
            {selectedIds.size} selected
          </span>
          <Button variant="secondary" size="sm" onClick={() => setSelectedIds(new Set())}>
            Clear
          </Button>
          <span style={{ flex: 1 }} />
          <Button variant="primary" disabled={bulkBusy}
            onClick={async () => {
              if (bulkBusy) return;
              setBulkBusy(true);
              try {
                await api.acknowledgeProblems([...selectedIds]);
                setSelectedIds(new Set());
                problemsQ.refetch();
              } catch {
                // toast surface lives globally; swallow here
              } finally {
                setBulkBusy(false);
              }
            }}>
            {bulkBusy ? 'Acknowledging…' : 'Acknowledge'}
          </Button>
        </div>
      )}
      {sorted && sorted.length > 0 && (
        <div className="table-wrap is-fit">
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={dt} leading={[28]} trailing={[170, 90]} />
            <DataTableHead dt={dt}
              leading={
                <th style={{ width: 28 }}>
                  <input type="checkbox"
                    checked={sorted.length > 0 && sorted.every(p => selectedIds.has(p.id))}
                    onChange={e => {
                      if (e.target.checked) {
                        setSelectedIds(new Set(sorted.map(p => p.id)));
                      } else {
                        setSelectedIds(new Set());
                      }
                    }}
                    onClick={e => e.stopPropagation()}
                    title="Select all visible" />
                </th>
              }
              trailing={<><th>Assignee</th><th>Triage</th></>} />
            <tbody>
              {sorted.map((p, i) => {
                const isAnomaly = p.ruleId?.startsWith('anomaly:');
                return (
                  <tr key={p.id}
                      {...dt.rowProps(i)}
                      onClick={() => openDetail(p.id)}
                      onKeyDown={(e) => {
                        // Keyboard accessibility — Enter/Space opens the same
                        // Variant-B full-page detail the click does (the
                        // service column keeps its own /service link).
                        if (e.key === 'Enter' || e.key === ' ') {
                          e.preventDefault();
                          openDetail(p.id);
                        }
                      }}
                      tabIndex={0}
                      role="button"
                      style={{
                        cursor: 'pointer', contentVisibility: 'auto', containIntrinsicSize: 'auto 44px',
                        // Subtle err tint on open critical firings (prototype cue).
                        background: p.status === 'open' && p.severity === 'critical'
                          ? 'color-mix(in srgb, var(--err) 7%, transparent)'
                          : undefined,
                      }}>
                      <td onClick={e => e.stopPropagation()}>
                        <input type="checkbox"
                          checked={selectedIds.has(p.id)}
                          onChange={e => {
                            setSelectedIds(prev => {
                              const next = new Set(prev);
                              if (e.target.checked) next.add(p.id);
                              else next.delete(p.id);
                              return next;
                            });
                          }} />
                      </td>
                      <td><PriorityBadge p={p.priority} reason={p.priorityReason} /></td>
                      <td><SeverityBadge s={p.severity} /></td>
                      <td>
                        {/* v0.9.966 — problemin ömrü: onset−1h → (çözüm |
                            şimdi)+10m. Aynı sınırlar ProblemDetail'in
                            logs/traces/servis pivotlarında; liste ile detay
                            farklı pencere göstermemeli. AÇIK problemde
                            pencere ŞİMDİye kadar uzuyor — onset±40dk, hâlâ
                            yanan bir olayda hiçbir şeyin bozuk olmadığı bir
                            ana bakmak demekti. */}
                        <Link to={serviceHref(p.service, { range: eventLifespanWindow(p) })}
                          onClick={e => e.stopPropagation()}
                          style={{ fontWeight: 600 }}>
                          {p.service}
                        </Link>
                        <ClusterChips clusters={p.clusters} />
                      </td>
                      <td className="mono">{p.metric}</td>
                      <td className="mono" style={{ textAlign: 'right' }}>
                        <b style={{ color: 'var(--err)' }}>{fmtFixed(p.value, 2)}</b>
                        <span style={{ color: 'var(--text3)' }}> / {fmtFixed(p.threshold, 2)}</span>
                      </td>
                      <td style={{ fontSize: 12 }}>
                        {isAnomaly && (
                          <span className="badge b-info" style={{ marginRight: 6 }}>ANOMALY</span>
                        )}
                        {p.ruleName}
                        {p.runbookUrl && (
                          <a href={p.runbookUrl} target="_blank" rel="noopener"
                            onClick={e => e.stopPropagation()}
                            title="Open team runbook"
                            className="badge b-info"
                            style={{ marginLeft: 8, textDecoration: 'none' }}>
                            Runbook ↗
                          </a>
                        )}
                        {p.recentDeploy && (
                          // Deploy correlation tag — shows the
                          // service.version that landed in the 30 min
                          // before the problem fired. The classic
                          // "regression coincided with deploy" signal
                          // in a single chip. Amber so it visually
                          // codes as "warning, look here".
                          <span className="badge b-warn"
                            onClick={e => e.stopPropagation()}
                            title={`service.version=${p.recentDeploy.version} first seen ${fmtAge(p.recentDeploy.ageSeconds)} before this problem opened`}
                            style={{ marginLeft: 8 }}>
                            <ArrowDownToLine size={11} strokeWidth={1.75} /> {p.recentDeploy.version} · {fmtAge(p.recentDeploy.ageSeconds)} before
                          </span>
                        )}
                        {p.aiSummary && (
                          // AI auto-explain chip (v0.5.254). The
                          // background problemExplainer fills this
                          // within ~30s of a critical fire; tooltip
                          // shows the full blurb so the operator
                          // gets first-look context without
                          // clicking through. The IconSparkles glyph is
                          // the "Copilot output" visual anchor, matching
                          // the existing operator-clicked Explain affordances.
                          <span className="badge b-info"
                            onClick={e => e.stopPropagation()}
                            title={stripMarkdown(p.aiSummary)}
                            style={{ marginLeft: 8, cursor: 'help' }}>
                            <IconSparkles size={11} /> AI insight
                          </span>
                        )}
                        {/* v0.6.29 — blast radius chip for open
                            problems. Lazy-fetches when the row
                            renders; only shows when callers > 0
                            so the chip is silent on a service
                            with no upstream callers. Cascade
                            count surfaces in amber. */}
                        {p.status === 'open' && p.service && (
                          <BlastRadiusChip service={p.service} />
                        )}
                        {isAnomaly && (
                          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 2 }}>
                            {p.description}
                          </div>
                        )}
                        {p.aiSummary && (
                          <div style={{
                            fontSize: 11, color: 'var(--text2)', marginTop: 4,
                            padding: 6, borderRadius: 4,
                            background: 'var(--accent-soft)',
                            borderLeft: '2px solid var(--accent)',
                          }}>
                            {/* v0.9.696 — kırpılmamış tam metin: markdown basılıyor. */}
                            <RenderedMarkdown text={p.aiSummary} />
                          </div>
                        )}
                        {/* rc #3 — in-page root-cause ribbon. Collapsed chip
                            renders from the row's persisted summary
                            (p.rootCause, joined by the /problems handler — no
                            fetch); expand reads the full /rootcause fan-out.
                            The chip's own stopPropagation keeps the row's
                            navigate-on-click intact. */}
                        <div style={{ marginTop: p.aiSummary ? 6 : 2 }}>
                          <RootCauseRibbon anchor="problem" id={p.id} summary={p.rootCause} />
                        </div>
                      </td>
                      <td className="mono">{tsLong(p.startedAt)}</td>
                      <td>
                        {p.status === 'open' && <span className="badge b-err">OPEN</span>}
                        {p.status === 'acknowledged' && <span className="badge b-warn">ACK</span>}
                        {p.status === 'resolved' && <span className="badge b-ok">RESOLVED</span>}
                      </td>
                      <td onClick={e => e.stopPropagation()} style={{ fontSize: 12 }}>
                        <AssigneeCell problem={p}
                          currentUserEmail={currentUserEmail}
                          onChanged={() => problemsQ.refetch()} />
                      </td>
                      <td onClick={e => e.stopPropagation()}>
                        {/* Triage — opens the right-side drawer
                            consolidating rule details + causal
                            correlation + AI explain + runbook
                            AI in one panel. Replaces the v0.5.x
                            inline "Why?" expansion and the
                            scattered per-cell AI buttons. */}
                        <Button variant="secondary" size="sm"
                          onClick={() => openDetail(p.id)}>
                          Triage ▶
                        </Button>
                      </td>
                    </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

// AlertProblemHost — resolves ?problem=<id> to a row and renders the
// Variant-B full-page detail.
//
// ÜÇ BASAMAKLI ÇÖZÜM, ucuzdan pahalıya:
//   1. cache — açık sayfaların halihazırda yüklü listeleri (bedava);
//   2. liste — "herhangi bir durumdan en yeni 200" (zaten pollanıyor);
//   3. by-id — tekil okuma (v0.9.825), YALNIZ ilk ikisi başarısızsa.
//
// 3. basamak neden şart: bildirim gönderilen problem çoğu zaman
// ÇÖZÜLÜR ve liste penceresinden düşer. Yani e-postadaki bağlantı tam
// da en çok gerektiği anda — "gece 3'te gelen sayfayı sabah aç" —
// "Problem not found" diyordu. Kayıt aslında duruyordu (problems
// tablosu, 90 günlük TTL); ekran olmayan bir veri kaybı bildiriyordu.
//
// Artık "bulunamadı" YALNIZ sunucu 404 döndüğünde çıkıyor — yani kayıt
// GERÇEKTEN yokken. Çözülmüş kayıt tam detayıyla, RESOLVED rozeti ve
// çözülme damgasıyla açılıyor (AlertProblemDetail).
// v0.9.837 — `backLabel` names the surface the host was opened FROM.
// Both /problems and /inbox render it now, and an "← Exceptions"
// button on the Problems queue would send the operator somewhere they
// were never standing.
export function AlertProblemHost({ id, isAdmin, onBack, backLabel = '← Exceptions' }: {
  id: string;
  isAdmin: boolean;
  onBack: () => void;
  backLabel?: string;
}) {
  const qc = useQueryClient();
  const q = useProblems({ limit: 200 });
  const cached = useMemo(() => findProblemInCaches(qc.getQueriesData({ queryKey: keys.problems.all }), id),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [qc, id, q.dataUpdatedAt]);
  const fromList = cached ?? (q.data?.items ?? []).find(x => x.id === id);

  // Tekil okuma YALNIZ liste yolu bitip satır çıkmadığında açılır.
  // Koşulsuz açmak, listeden zaten gelen her satır için bedava bir CH
  // sorgusu demek olurdu — açık problemlerin neredeyse tamamı listede.
  const listSettled = !q.isLoading && !q.isFetching;
  const byID = useProblemByID(id, { enabled: !fromList && listSettled });
  const p = fromList ?? byID.data ?? undefined;

  // Sunucu 404 dedi = kayıt GERÇEKTEN yok. null ile undefined ayrı:
  // undefined "henüz yüklenmedi" demek ve o anda "bulunamadı" yazmak
  // yanlış cümle olurdu.
  const genuinelyGone = byID.isSuccess && byID.data === null;
  const loading = !p && !genuinelyGone && (q.isLoading || byID.isLoading || byID.isFetching);
  // Liste hatası ancak tekil okuma da kurtaramadıysa hata ekranıdır.
  const failed = !p && !genuinelyGone && !loading && (q.isError || byID.isError);

  return (
    <>
      {/* Singular: this is ONE problem, reached from a notification deep
          link. Plural "Problems" is the merged queue (v0.9.323). */}
      <Topbar title="Problem" showEnv envApplies />
      {p ? (
        <AlertProblemDetail
          problem={p}
          isAdmin={isAdmin}
          onBack={onBack}
          onChanged={() => {
            void q.refetch();
            void qc.invalidateQueries({ queryKey: keys.problems.all });
          }}
        />
      ) : loading ? (
        <div id="content"><Spinner /></div>
      ) : failed ? (
        <div id="content">
          <Empty icon="⚠" title="Problem yüklenemedi">
            <Button variant="secondary" size="sm" onClick={() => { void q.refetch(); void byID.refetch(); }}>Tekrar dene</Button>{' '}
            <Button variant="secondary" size="sm" onClick={onBack}>{backLabel}</Button>
          </Empty>
        </div>
      ) : (
        <div id="content">
          <Empty icon="❓" title="Problem kaydı yok">
            Bu kimlikle bir problem bulunamadı — kayıt 90 günlük saklama
            penceresini aşmış ya da bağlantı eksik kopyalanmış olabilir.{' '}
            <Button variant="secondary" size="sm" onClick={onBack}>{backLabel}</Button>
          </Empty>
        </div>
      )}
    </>
  );
}

function SectionHeader({ title, subtitle }: { title: string; subtitle?: string }) {
  return (
    <div style={{
      display: 'flex', alignItems: 'baseline', gap: 10,
      marginBottom: 10, paddingBottom: 6,
      borderBottom: '1px solid var(--border)',
    }}>
      <span style={{ fontSize: 14, fontWeight: 700 }}>{title}</span>
      {subtitle && (
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>{subtitle}</span>
      )}
    </div>
  );
}

function SeverityBadge({ s }: { s: string }) {
  const cls = s === 'critical' ? 'b-err' : s === 'warning' ? 'b-warn' : 'b-info';
  return <span className={`badge ${cls}`}>{s.toUpperCase()}</span>;
}

// PriorityBadge — v0.5.210 triage column. P1 / P2 / P3 pill with
// a colour that matches the urgency stack (red/amber/grey).
// `reason` flows into the title attribute so an operator can
// hover and see WHY the bucket was picked ("critical + deploy
// 4m before") — the blend formula is transparent, not magic.
function PriorityBadge({ p, reason }: { p?: 'P1' | 'P2' | 'P3'; reason?: string }) {
  if (!p) return <span style={{ color: 'var(--text3)' }}>—</span>;
  const cls = p === 'P1' ? 'b-err' : p === 'P2' ? 'b-warn' : 'b-gray';
  return (
    <span className={`badge ${cls}`} title={reason ? `${p} — ${reason}` : p}>
      {p}
    </span>
  );
}

// AssigneeCell — v0.5.209 triage column. Renders the current
// assignee (team name auto-set on open from service_metadata.
// ownerTeam, OR an operator email after manual claim), with two
// inline affordances:
//   • "Take it" — PATCH self-email when the problem is
//     unassigned or assigned to a team. One click, no modal.
//   • Click-to-edit — prompt() lets the operator type any
//     value (reassign to a teammate / change team / clear).
// Kept dependency-light: no inline picker component, no
// suggestions list. v2 can promote this to a typeahead against
// the users table if the prompt() ergonomics annoy operators.
function AssigneeCell({ problem, currentUserEmail, onChanged }: {
  problem: Problem;
  currentUserEmail: string;
  onChanged: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const assignee = problem.assignee ?? '';
  const isSelf = currentUserEmail !== '' && assignee === currentUserEmail;
  const isTeam = assignee !== '' && !assignee.includes('@');

  const set = async (next: string) => {
    if (busy || next === assignee) return;
    setBusy(true);
    try { await api.setProblemAssignee(problem.id, next); onChanged(); }
    finally { setBusy(false); }
  };
  const editPrompt = () => {
    const v = window.prompt('Assignee (email or team name; empty = unassign):', assignee);
    if (v === null) return;
    void set(v.trim());
  };

  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
      {assignee
        ? (
          <span onClick={editPrompt}
            title="Click to reassign or clear"
            className={`badge ${isSelf ? 'b-ok' : 'b-info'}`}
            style={{ cursor: 'pointer' }}>
            {isTeam && <Users size={11} strokeWidth={1.75} />}{assignee}
          </span>
        )
        : <span style={{ color: 'var(--text3)' }}>—</span>}
      {currentUserEmail !== '' && !isSelf && (
        <Button variant="secondary" size="sm" disabled={busy}
          onClick={() => void set(currentUserEmail)}
          title="Claim this problem for yourself">
          Take it
        </Button>
      )}
    </span>
  );
}

// fmtAge — compact "Nm" / "Nh" / "Ns" formatter for the deploy
// correlation tag. ageSeconds is always positive (deploy was
// before problem) but be defensive.
function fmtAge(sec: number): string {
  const s = Math.max(0, Math.round(sec));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.round(s / 60)}m`;
  return `${Math.round(s / 3600)}h`;
}

// v0.6.29 — inline blast-radius chip for the /problems row.
// Lazy-fetches the per-service summary so a 100-row inbox
// doesn't fan out 100 parallel requests on first paint —
// individual chips load as their row renders. Hidden when the
// service has no upstream callers (silent on standalone
// services) so the row layout stays clean.
//
// Cascade callers (services with their own open problem)
// shift the chip to amber + the count surfaces in the tooltip
// so the operator sees "this isn't isolated — 3 downstream
// services are already firing too" without expanding the row.
function BlastRadiusChip({ service }: { service: string }) {
  const [data, setData] = useState<import('@/lib/types').BlastRadius | null>(null);
  useEffect(() => {
    let cancelled = false;
    api.serviceBlastRadius(service)
      .then(d => { if (!cancelled) setData(d); })
      .catch(() => { /* silent — chip just doesn't render */ });
    return () => { cancelled = true; };
  }, [service]);
  if (!data || data.totalCallers === 0) return null;
  const cascading = data.cascadingCallers > 0;
  const tooltipLines = [
    `Blast radius: ${data.totalCallers} caller service${data.totalCallers === 1 ? '' : 's'}, ${data.totalRps.toFixed(1)} rps`,
    cascading && `${data.cascadingCallers} caller${data.cascadingCallers === 1 ? '' : 's'} ALSO have an open problem (cascading failure)`,
    '',
    'Top callers:',
    ...data.callers.slice(0, 5).map(c =>
      `  ${c.hasOpenProblem ? '⚠ ' : '  '}${c.service} — ${c.rps.toFixed(1)} rps${c.errorRate > 1 ? ` · ${c.errorRate.toFixed(1)}% err` : ''}`,
    ),
  ].filter(Boolean).join('\n');
  return (
    <span
      title={tooltipLines}
      onClick={e => e.stopPropagation()}
      className={`badge ${cascading ? 'b-warn' : 'b-info'}`}
      style={{ marginLeft: 8, cursor: 'help' }}>
      <CornerDownRight size={11} strokeWidth={1.75} />
      {data.totalCallers} svc{data.totalCallers === 1 ? '' : 's'} · {data.totalRps.toFixed(0)} rps
      {cascading && <> · {data.cascadingCallers} cascading</>}
    </span>
  );
}
