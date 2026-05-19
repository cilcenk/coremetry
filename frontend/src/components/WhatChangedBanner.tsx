import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { api } from '@/lib/api';

// WhatChangedBanner — v0.5.277. Page-top ribbon under the
// Topbar showing open critical/warning problem counts +
// recent service.version transitions in the last 30 min.
// Visible on every authenticated page (mounted from AppShell);
// silently self-hides when nothing's firing + no recent
// deploys.
//
// Datadog Watchdog's global notification bar equivalent.
// Operator opens any Coremetry page and instantly knows
// "what happened today / what's broken right now" without
// switching to Inbox / Services first.
//
// 30s poll, paused on document.hidden (CLAUDE.md hard
// constraint). Server caches the response 15s so the page
// load wave from a 10-replica HA install collapses to one
// CH round-trip per quarter-minute.

interface Data {
  openProblems: { critical: number; warning: number; info: number };
  recentDeploys: { service: string; version: string; firstSeenNs: number; spanCount: number }[] | null;
}

export function WhatChangedBanner() {
  const [data, setData] = useState<Data | null>(null);

  useEffect(() => {
    let cancelled = false;
    const fetchOnce = () => {
      api.recentChanges()
        .then(d => { if (!cancelled) setData(d); })
        .catch(() => { if (!cancelled) setData(null); });
    };
    fetchOnce();
    const id = setInterval(() => {
      if (!document.hidden) fetchOnce();
    }, 30_000);
    return () => { cancelled = true; clearInterval(id); };
  }, []);

  if (!data) return null;
  const { openProblems, recentDeploys } = data;
  const crit = openProblems.critical;
  const warn = openProblems.warning;
  const deploys = recentDeploys ?? [];
  // Quiet — nothing fired, nothing deployed → hide the ribbon
  // entirely. Avoids visual noise on a healthy install.
  if (crit === 0 && warn === 0 && deploys.length === 0) return null;

  // Single dominant tone: red if any critical, yellow if any
  // warning or deploy, else hidden (handled above).
  const tone = crit > 0 ? 'critical' : 'warning';
  const palette = tone === 'critical'
    ? { bg: 'rgba(220,38,38,0.08)', border: 'rgba(220,38,38,0.30)', icon: 'var(--err)' }
    : { bg: 'rgba(250,204,21,0.07)', border: 'rgba(250,204,21,0.30)', icon: 'var(--warn, #facc15)' };

  return (
    <div style={{
      padding: '6px 12px',
      background: palette.bg,
      borderBottom: `1px solid ${palette.border}`,
      fontSize: 12,
      display: 'flex', alignItems: 'center', gap: 14,
      flexWrap: 'wrap',
    }}>
      <span style={{ color: palette.icon, fontWeight: 700 }}>●</span>
      <span style={{ color: 'var(--text2)' }}>What changed</span>

      {crit > 0 && (
        <Link to="/problems?severity=critical"
          style={{
            color: 'var(--err)', textDecoration: 'none', fontWeight: 600,
          }}
          title="Open the critical problem inbox">
          {crit} critical
        </Link>
      )}
      {warn > 0 && (
        <Link to="/problems?severity=warning"
          style={{
            color: 'var(--warn, #facc15)', textDecoration: 'none', fontWeight: 600,
          }}
          title="Open the warning problem inbox">
          {warn} warning
        </Link>
      )}

      {deploys.length > 0 && (
        <span style={{ color: 'var(--text3)' }}>
          {deploys.length} recent deploy{deploys.length === 1 ? '' : 's'}:
          {' '}
          {deploys.slice(0, 4).map((d, i) => (
            <span key={`${d.service}-${d.version}`} style={{ marginRight: 8 }}>
              <Link to={`/service?name=${encodeURIComponent(d.service)}`}
                style={{ color: 'var(--accent2)', textDecoration: 'none' }}
                title={`${d.service} shipped ${d.version} · ${ageStr(d.firstSeenNs)}`}>
                {d.service}
              </Link>
              <span style={{ color: 'var(--text3)' }}> v{d.version}</span>
              <span style={{ color: 'var(--text3)', fontSize: 11 }}> · {ageStr(d.firstSeenNs)}</span>
              {i < Math.min(deploys.length, 4) - 1 && <span style={{ color: 'var(--text3)' }}>,</span>}
            </span>
          ))}
          {deploys.length > 4 && (
            <span style={{ color: 'var(--text3)' }}>+{deploys.length - 4} more</span>
          )}
        </span>
      )}
    </div>
  );
}

function ageStr(ns: number): string {
  const sec = Math.floor((Date.now() * 1e6 - ns) / 1e9);
  if (sec < 60) return `${sec}s ago`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m ago`;
  return `${Math.floor(sec / 3600)}h ago`;
}
