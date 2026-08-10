import { Suspense, useEffect, useMemo, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { FlameGraph } from '@/components/FlameGraph';
import { FlameDiff } from '@/components/FlameDiff';
import { MethodHotspots } from '@/components/MethodHotspots';
import { BreakdownBar } from '@/components/KindBadge';
import { CopyButton } from '@/components/CopyButton';
import { Button } from '@/components/ui/Button';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { api } from '@/lib/api';
import { raceGuard } from '@/lib/raceGuard';
import { tsLong, fmtNum } from '@/lib/utils';
import { diffFlame } from '@/lib/flameDiff';
import type { ProfileDetail, ProfileRow } from '@/lib/types';

// /profile renders one profile's flamegraph by default. When
// the URL carries `?baseline=<id>` we fetch a second profile,
// diff it against the current one frame-by-frame, and render
// the FlameDiff overlay (frames coloured by % change between
// baseline and current). Datadog Continuous Profiling and
// pprof's `pprof -base` both shipped this view as the
// canonical "did the regression I'm investigating land in
// this function?" tool — single biggest profiler navigation
// shortcut after the basic flame.

// v0.9.877 (tutarlılık denetimi MT11) — elle yazılmış <thead> paylaşılan
// primitife taşındı. Kolon kümesi, sıra, etiketler ve hücre içerikleri AYNEN
// korundu; kazanılan sıralama + yeniden boyutlandırma + kalıcı genişlik —
// baseline seçerken "en uzun pencere" ya da "en çok örnek" hangisi diye
// bakmak artık tek tık.
const BASELINE_COLS: DataTableColumn<ProfileRow>[] = [
  // Zaman mono ve SOLA hizalı kalıyor (numeric:true başlığı sağa iterdi).
  { id: 'when',     label: 'When',       sortValue: p => p.startTime,      width: 170 },
  { id: 'id',       label: 'Profile ID', sortValue: p => p.profileId,      naturalDir: 'asc', width: 180 },
  { id: 'type',     label: 'Type',       sortValue: p => p.profileType,    naturalDir: 'asc', width: 95 },
  { id: 'host',     label: 'Host',       sortValue: p => p.hostName ?? '', naturalDir: 'asc', flex: true },
  { id: 'duration', label: 'Duration',   sortValue: p => p.durationMs,     numeric: true, width: 110 },
  { id: 'samples',  label: 'Samples',    sortValue: p => p.sampleCount,    numeric: true, width: 110 },
];

function ProfileDetailInner() {
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const id = searchParams.get('id') ?? '';
  const baselineId = searchParams.get('baseline') ?? '';

  const [data, setData] = useState<ProfileDetail | null | undefined>(undefined);
  const [baseData, setBaseData] = useState<ProfileDetail | null | undefined>(undefined);
  // Picker state — datalist of recent profiles for the same
  // service. Lazy-loaded on first picker open so the page
  // doesn't fan out a /profiles request for every Profile
  // visit (only the ones where the user wants to compare).
  const [pickerOpen, setPickerOpen] = useState(false);
  const [recentProfiles, setRecentProfiles] = useState<ProfileRow[]>([]);

  // v0.9.857 (UX denetimi K7) — Trace.tsx ile aynı yarış deseni; burada iki
  // kopya var (profil + baseline) ve karşılaştırma sayfası olduğu için ikisi
  // arasında hızlı geçiş normal kullanım.
  useEffect(() => {
    if (!id) return;
    setData(undefined);
    const g = raceGuard();
    api.profile(id, g.signal)
      .then(d => { if (g.ok()) setData(d); })
      .catch(() => { if (g.ok()) setData(null); });
    return g.cancel;
  }, [id]);

  useEffect(() => {
    if (!baselineId) { setBaseData(undefined); return; }
    setBaseData(undefined);
    const g = raceGuard();
    api.profile(baselineId, g.signal)
      .then(d => { if (g.ok()) setBaseData(d); })
      .catch(() => { if (g.ok()) setBaseData(null); });
    return g.cancel;
  }, [baselineId]);

  // Build the diff lazily when both flames are present. Memo
  // on the two profile IDs (treating identity-stable inputs
  // as cache key) so a flame zoom on one side doesn't
  // recompute the diff.
  const diff = useMemo(() => {
    if (!data?.flame || !baseData?.flame) return null;
    return diffFlame(data.flame, baseData.flame);
  }, [data, baseData]);

  // Picker fetch: when the operator opens the picker, fetch
  // the last 50 profiles for this service. Skips fetch if
  // already loaded (the list rarely changes within a session).
  useEffect(() => {
    if (!pickerOpen || recentProfiles.length > 0 || !data) return;
    const svc = data.meta.serviceName;
    const now = Date.now() * 1_000_000;
    const since = now - 24 * 60 * 60 * 1_000_000_000; // last 24h in ns
    api.profiles({ service: svc, from: since, to: now, limit: 50 })
      .then(rows => setRecentProfiles(rows ?? []))
      .catch(() => setRecentProfiles([]));
  }, [pickerOpen, data, recentProfiles.length]);

  // Aday listesi: kendisiyle diff alınamaz. Filtre JSX'ten çıkıp memoya
  // taşındı — her render'da yeni bir dizi üretmek sortedRows memosunu
  // boşuna geçersiz kılıyordu.
  const baselineRows = useMemo(
    () => recentProfiles.filter(p => p.profileId !== id),
    [recentProfiles, id],
  );
  // Varsayılan sıra sunucununkiyle birebir: ListProfiles `ORDER BY
  // start_time DESC` — bir önceki profil (en doğal baseline) en üstte kalır.
  const dt = useDataTable<ProfileRow>({
    storageKey: 'profile-baseline-picker', columns: BASELINE_COLS, rows: baselineRows,
    initialSort: { id: 'when', dir: 'desc' },
  });

  function setBaseline(profileId: string) {
    const next = new URLSearchParams(searchParams);
    if (profileId) next.set('baseline', profileId);
    else next.delete('baseline');
    setSearchParams(next, { replace: true });
    setPickerOpen(false);
  }

  return (
    <>
      <Topbar title="Profile" />
      <div id="content">
        <div style={{ marginBottom: 12, display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap' }}>
          <Button variant="secondary" onClick={() => navigate(-1)}>← Back</Button>
          {data && (
            <>
              <code style={{ fontSize: 11, color: 'var(--text2)', background: 'var(--bg2)', padding: '2px 6px', borderRadius: 4 }}>
                {data.meta.profileId}
                <CopyButton value={data.meta.profileId} title="Copy profile ID" />
              </code>
              <span className="badge b-info">{data.meta.profileType.toUpperCase()}</span>
              <span style={{ fontSize: 12, color: 'var(--text2)' }}>{data.meta.serviceName}</span>
              <span style={{ fontSize: 12, color: 'var(--text2)' }}>
                {tsLong(data.meta.startTime)} · {data.meta.durationMs > 0 ? `${(data.meta.durationMs/1000).toFixed(1)}s window` : '—'}
              </span>
              {/* Compare picker — when no baseline is set we
                  show "Compare with…" which opens the picker;
                  with a baseline set we show the baseline ID
                  + a clear button. Same UX shape as the trace
                  comparison entry on /trace. */}
              {!baselineId && (
                <Button variant="secondary"
                  onClick={() => setPickerOpen(o => !o)}>
                  {pickerOpen ? 'Cancel' : 'Compare with…'}
                </Button>
              )}
              {baselineId && (
                <span style={{
                  display: 'inline-flex', alignItems: 'center', gap: 6,
                  fontSize: 11, color: 'var(--text2)',
                  background: 'var(--bg2)',
                  border: '1px solid var(--border)',
                  borderRadius: 4, padding: '2px 8px',
                }}>
                  vs baseline <code>{baselineId.slice(0, 12)}…</code>
                  <Button variant="ghost" size="sm" onClick={() => setBaseline('')}
                    title="Clear baseline">✕</Button>
                </span>
              )}
              <span style={{ fontSize: 12, color: 'var(--text3)', marginLeft: 'auto' }}>
                {fmtNum(data.meta.sampleCount)} samples
              </span>
            </>
          )}
        </div>

        {/* Picker — datalist of recent profiles for the same
            service. Disabled when baseline already set. The
            entries are sorted by time desc so the previous
            profile (the most natural baseline) is on top. */}
        {pickerOpen && data && (
          <div style={{
            marginBottom: 12, padding: 12,
            background: 'var(--bg1)',
            border: '1px solid var(--border)',
            borderRadius: 8,
          }}>
            <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 8 }}>
              Pick a baseline profile to diff against —
              {' '}<b style={{ color: 'var(--text)' }}>{data.meta.serviceName}</b>'s
              {' '}last 24h.
            </div>
            {recentProfiles.length === 0
              ? <div style={{ fontSize: 12, color: 'var(--text3)' }}>Loading…</div>
              : (
                <div className="table-wrap">
                  <table style={{ tableLayout: 'fixed', width: '100%' }}>
                    <DataTableColgroup dt={dt} trailing={[180]} />
                    <DataTableHead dt={dt} trailing={<th />} />
                    <tbody>
                      {dt.sortedRows.map(p => (
                          <tr key={p.profileId}>
                            <td className="mono" style={{ fontSize: 11 }}>{tsLong(p.startTime)}</td>
                            <td className="mono" style={{ fontSize: 11 }}>{p.profileId.slice(0, 16)}…</td>
                            <td><span className="badge b-info">{p.profileType.toUpperCase()}</span></td>
                            <td style={{ fontSize: 11 }}>{p.hostName || '—'}</td>
                            <td className="num mono">{(p.durationMs / 1000).toFixed(1)}s</td>
                            <td className="num mono">{fmtNum(p.sampleCount)}</td>
                            <td>
                              <Button variant="secondary" size="sm"
                                onClick={() => setBaseline(p.profileId)}>
                                Use as baseline →
                              </Button>
                            </td>
                          </tr>
                        ))}
                    </tbody>
                  </table>
                </div>
              )}
          </div>
        )}

        {!id && <Empty icon="⚠" title="Missing profile id" />}
        {id && data === undefined && <Spinner />}
        {id && data === null && <Empty icon="⚠" title="Profile not found or failed to parse" />}

        {/* Render path:
              • baseline set + both fetched + diff built → FlameDiff
              • baseline set, but baseline still loading → spinner
              • no baseline → regular FlameGraph
            Errors on the baseline side fall back to the
            single-profile flame so the operator at least sees
            the current profile. */}
        {data && data.flame && baselineId && baseData === undefined && <Spinner />}
        {data && data.flame && baselineId && baseData && diff && <FlameDiff root={diff} />}
        {data && data.flame && baselineId && baseData === null && (
          <>
            <div style={{
              fontSize: 12, color: 'var(--err)',
              padding: '8px 12px', marginBottom: 10,
              background: 'var(--bg1)', border: '1px solid var(--border)',
              borderRadius: 6,
            }}>
              Baseline profile failed to load — showing the current profile only.
            </div>
            <FlameGraph root={data.flame} />
          </>
        )}
        {/* Breakdown bar — top-line "where did time go" across
            kinds (CPU / Lock / IO / Sleep / GC) for the single
            profile. Renders above the flame so the operator
            sees the suspension story before scanning frames. */}
        {data && data.flame && !baselineId && data.breakdown && (
          <BreakdownBar b={data.breakdown} />
        )}

        {data && data.flame && !baselineId && <FlameGraph root={data.flame} />}

        {/* Method Hotspots — Dynatrace-style "which functions
            are heaviest" tabular view aggregated across the
            whole flame. Hidden in baseline-compare mode (diff
            view is the comparison surface there). */}
        {data && data.flame && !baselineId && <MethodHotspots root={data.flame} />}
      </div>
    </>
  );
}

export default function ProfilePage() {
  return (
    <Suspense fallback={<Spinner />}>
      <ProfileDetailInner />
    </Suspense>
  );
}
