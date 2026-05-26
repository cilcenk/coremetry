import { useEffect, useState } from 'react';
import { api } from '@/lib/api';

// EventMarkers (v0.5.478) — vertical markers overlaid on a
// time-series chart for operator events ("deploy v1.2.3", "config
// change", "incident start"). Mounts inside an absolutely-
// positioned container that spans the chart area; converts each
// event's unix-ns timestamp to a percentage of the chart's
// horizontal time range and renders a 2px-wide line at that
// position.
//
// Why not draw on the uPlot canvas directly? An overlay layer is
// simpler to reason about + reusable on non-uPlot surfaces
// (sparklines, future SVG charts). Each marker stays interactive
// (title tooltip + click-through to event detail later).

interface OperatorEvent {
  id: string;
  kind: string;
  label: string;
  time: number;     // unix ns
  service: string;
  link: string;
  owner: string;
  createdAt: number;
}

interface EventMarkersProps {
  // Chart bounds in unix ns. Marker positions are computed
  // relative to (toNs - fromNs).
  fromNs: number;
  toNs: number;
  // Optional service scope — when set, narrows /api/events to
  // events for this service OR global (no service set).
  service?: string;
}

const KIND_COLOUR: Record<string, string> = {
  deploy:      'rgba(46,160,67,0.65)',
  config:      'rgba(56,139,253,0.65)',
  incident:    'rgba(220,38,38,0.70)',
  maintenance: 'rgba(217,119,6,0.65)',
};
const DEFAULT_COLOUR = 'rgba(160,160,160,0.55)';

export function EventMarkers({ fromNs, toNs, service }: EventMarkersProps) {
  const [events, setEvents] = useState<OperatorEvent[]>([]);

  useEffect(() => {
    if (!fromNs || !toNs || toNs <= fromNs) return;
    let cancelled = false;
    api.listEvents({
      from: fromNs,
      to: toNs,
      service: service || undefined,
      limit: 100,
    })
      .then(d => { if (!cancelled) setEvents(d ?? []); })
      .catch(() => { if (!cancelled) setEvents([]); });
    return () => { cancelled = true; };
  }, [fromNs, toNs, service]);

  const range = toNs - fromNs;
  if (range <= 0 || events.length === 0) return null;

  return (
    <div style={{
      position: 'absolute', inset: 0, pointerEvents: 'none',
      // Sits ABOVE the chart canvas (uPlot draws at z 0). Below
      // the tooltip layer (which uPlot manages separately).
      zIndex: 2,
    }}>
      {events.map(e => {
        const pctX = ((e.time - fromNs) / range) * 100;
        if (pctX < 0 || pctX > 100) return null;
        const colour = KIND_COLOUR[e.kind] ?? DEFAULT_COLOUR;
        const niceTime = new Date(e.time / 1_000_000).toLocaleString();
        const title =
          `${e.kind.toUpperCase()} · ${e.label}` +
          (e.service ? `\nservice: ${e.service}` : '') +
          (e.owner ? `\nby: ${e.owner}` : '') +
          `\nat: ${niceTime}` +
          (e.link ? `\nlink: ${e.link}` : '');
        return (
          <div key={e.id} title={title}
            style={{
              position: 'absolute',
              left: `${pctX}%`,
              top: 0, bottom: 0,
              width: 2,
              marginLeft: -1,
              background: colour,
              pointerEvents: 'auto',
              cursor: e.link ? 'pointer' : 'help',
            }}
            onClick={() => {
              if (e.link) {
                window.open(e.link, '_blank', 'noopener');
              }
            }}>
            {/* Tiny diamond at the top so the marker is visible
                even when the chart's background is busy. */}
            <div style={{
              position: 'absolute', top: -3, left: -3,
              width: 8, height: 8, transform: 'rotate(45deg)',
              background: colour,
              border: '1px solid var(--bg)',
            }} />
          </div>
        );
      })}
    </div>
  );
}
