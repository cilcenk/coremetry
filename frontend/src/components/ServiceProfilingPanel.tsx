import { useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { api } from '@/lib/api';
import { timeRangeToNs, fmtNum } from '@/lib/utils';
import { BreakdownBar, KindBadge } from './KindBadge';
import { IconFlame } from './icons';
import type { ProfileHotspotsResponse, TimeRange } from '@/lib/types';

// ServiceProfilingPanel — Dynatrace-style "Top methods" tile on
// the Service detail Details tab. Pulls the same aggregated
// hotspots endpoint the /profiling page uses, but capped to
// top 5 and rendered as a compact card. Lets an operator
// scanning a slow service see the dominant methods + leaf-time
// kind split (CPU vs Lock vs IO) without leaving the page; a
// "→ Full hotspots" link opens the /profiling Hotspots view
// pre-filtered to this service for the deeper drill.
//
// Hides itself entirely when the backend returns zero merged
// profiles — services without profiling wired up should look
// the same as before, no empty panel taking real estate.

const SHOWN_ROWS = 5;

export function ServiceProfilingPanel({ service, range }: {
  service: string;
  range: TimeRange;
}) {
  const [data, setData] = useState<ProfileHotspotsResponse | null | undefined>(undefined);
  const rangeNs = useMemo(() => timeRangeToNs(range), [range]);

  useEffect(() => {
    if (!service) return;
    setData(undefined);
    api.profileHotspots({
      service,
      type: 'cpu',
      from: rangeNs.from,
      to: rangeNs.to,
      limit: 200,
      top: SHOWN_ROWS,
    })
      .then(r => setData(r ?? null))
      .catch(() => setData(null));
  }, [service, rangeNs.from, rangeNs.to]);

  // Hide entirely on load failure / no profiles. The
  // service-detail page already has plenty of dense panels;
  // a "no data" tile here is noise for the common case of a
  // service not yet pushing profiles.
  if (data === undefined) return null;
  if (data === null) return null;
  if (!data.profilesUsed || data.profilesUsed === 0) return null;
  if (!data.hotspots || data.hotspots.length === 0) return null;

  const totalSamples = data.totalSamples || 1;
  return (
    <div style={{
      marginTop: 14, padding: 12, borderRadius: 8,
      background: 'var(--bg1)', border: '1px solid var(--border)',
    }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 8 }}>
        <IconFlame size={14} />
        <span style={{ fontSize: 13, fontWeight: 700 }}>Top methods</span>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          {data.profilesUsed} profiles · {fmtNum(data.totalSamples)} samples
        </span>
        <Link to={`/profiling?service=${encodeURIComponent(service)}&view=hotspots`}
          style={{ marginLeft: 'auto', fontSize: 11, color: 'var(--accent2)' }}>
          Full hotspots →
        </Link>
      </div>
      <BreakdownBar b={data.breakdown} />
      <table className="ps-kv" style={{ width: '100%', fontSize: 12 }}>
        <tbody>
          {data.hotspots.slice(0, SHOWN_ROWS).map((h, i) => {
            const pct = (h.self / totalSamples) * 100;
            return (
              <tr key={i}>
                <td style={{ fontFamily: 'monospace', wordBreak: 'break-all', paddingRight: 6 }} title={h.name}>
                  {h.name}<KindBadge kind={h.kind} />
                </td>
                <td className="num mono" style={{ whiteSpace: 'nowrap', color: 'var(--text2)', width: 90 }}>
                  {pct.toFixed(1)}%
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
