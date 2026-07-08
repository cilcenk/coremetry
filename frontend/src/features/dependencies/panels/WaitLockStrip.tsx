import { useEffect, useState } from 'react';
import { Spinner } from '@/components/Spinner';
import { api } from '@/lib/api';
import { fmtNum, timeRangeToNs } from '@/lib/utils';
import type { TimeRange, DBWaitLock } from '@/lib/types';
import { WaitClassesBar } from './shared';

// WaitLockStrip (v0.8.391, Stage-2 D3) — the cross-engine waits &
// locks strip on the /databases detail drawer. One COMMON model
// (wait classes + lock stats) rendered identically for every
// engine, fed by whatever the engine's OTel receiver actually
// emits. The strip is honest per engine: a family the receiver
// never shipped in the window renders "no lock telemetry from this
// receiver", never a fabricated zero. Renders for span-derived rows
// too — an honest empty there tells the operator the receiver isn't
// wired (or tags a different instance name), which is the actionable
// signal.

// Per-engine receiver knowledge — presentation only (the backend
// spec in chstore/db_waitlock.go is the source of truth for the
// metric families; this mirrors it for the honest empty copy).
const ENGINE_INFO: Record<string, { label: string; families: string; emitsWaitClasses: boolean }> = {
  oracle:     { label: 'Oracle',     families: 'wait classes, row-lock waits and deadlock counters', emitsWaitClasses: true },
  postgresql: { label: 'PostgreSQL', families: 'lock counts by mode and deadlocks',                  emitsWaitClasses: false },
  mysql:      { label: 'MySQL',      families: 'row-lock waits, row-lock time and table-lock rates', emitsWaitClasses: false },
};

// isWaitLockEngine gates the strip in DetailDrawer — only engines
// whose receivers have ANY wait/lock family. Redis (no lock
// concept) and unknown engines render no strip at all.
export function isWaitLockEngine(system: string): boolean {
  switch (system.toLowerCase()) {
    case 'oracle': case 'oracledb':
    case 'postgresql': case 'postgres':
    case 'mysql': case 'mariadb':
      return true;
  }
  return false;
}

export function WaitLockStrip({ system, instance, range }: {
  system: string;
  instance: string;
  range: TimeRange;
}) {
  const [data, setData] = useState<DBWaitLock | null | undefined>(undefined);
  useEffect(() => {
    setData(undefined);
    const { from, to } = timeRangeToNs(range);
    api.dbWaitLock(system, instance, from, to)
      .then(r => setData(r ?? null))
      .catch(() => setData(null));
  }, [system, instance, range]);

  // Defense in depth — the isWaitLockEngine gate should prevent
  // this, but a backend that says "unsupported" wins.
  if (data && !data.supported) return null;

  const info = data ? ENGINE_INFO[data.system] : undefined;
  const locks = data?.locks ?? {};
  const byMode = locks.byMode ?? [];
  const waitClasses = data?.waitClasses ?? [];
  const hasAny = waitClasses.length > 0
    || locks.waitsPerSec !== undefined
    || locks.timeSec !== undefined
    || locks.deadlocksPerSec !== undefined
    || byMode.length > 0;

  return (
    <div style={{ marginBottom: 14 }}>
      <div style={{ fontSize: 10, fontWeight: 700, color: 'var(--text3)',
                    textTransform: 'uppercase', letterSpacing: '.5px', marginBottom: 4 }}>
        Waits &amp; locks{info ? ` · ${info.label}` : ''}
      </div>

      {data === undefined && <Spinner />}
      {data === null && (
        <div style={{ fontSize: 12, color: 'var(--err)' }}>Wait/lock query failed.</div>
      )}

      {data && !hasAny && (
        <div style={{ fontSize: 12, color: 'var(--text3)' }}>
          No wait/lock telemetry from the {info?.label ?? data.system} receiver in
          this window.
          {info && (
            <> When wired, it emits {info.families}.</>
          )}
        </div>
      )}

      {data && hasAny && (
        <>
          {/* Lock stats as chips — present families only. Tones
              mirror the engine panels' paging thresholds. */}
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, alignItems: 'center',
                        marginBottom: waitClasses.length > 0 ? 8 : 0 }}>
            {locks.waitsPerSec !== undefined && (
              <LockChip label="row-lock waits"
                value={`${locks.waitsPerSec.toFixed(2)}/s`}
                tone={locks.waitsPerSec > 1 ? 'err' : locks.waitsPerSec > 0.2 ? 'warn' : 'ok'} />
            )}
            {locks.timeSec !== undefined && (
              <LockChip label="lock time"
                value={`${locks.timeSec.toFixed(1)} s`}
                tone={locks.timeSec > 1 ? 'warn' : 'ok'} />
            )}
            {locks.deadlocksPerSec !== undefined && (
              <LockChip label="deadlocks"
                value={`${locks.deadlocksPerSec.toFixed(3)}/s`}
                tone={locks.deadlocksPerSec > 0.1 ? 'err' : locks.deadlocksPerSec > 0 ? 'warn' : 'ok'} />
            )}
            {byMode.map(m => (
              <LockChip key={m.mode} label={m.mode}
                value={m.unit === '/s' ? `${m.value.toFixed(2)}/s` : fmtNum(m.value)} />
            ))}
          </div>

          {waitClasses.length > 0 && (
            <WaitClassesBar waits={waitClasses} />
          )}

          {/* Honest cross-engine note: don't let a bar-less strip
              read as "this DB never waits". PG/MySQL receivers have
              no wait-class family; Oracle's just wasn't seen. */}
          {waitClasses.length === 0 && info && (
            <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 2 }}>
              {info.emitsWaitClasses
                ? 'No wait-class telemetry from this receiver in this window.'
                : `Wait-class breakdown: not emitted by the ${info.label} receiver.`}
            </div>
          )}
        </>
      )}
    </div>
  );
}

// LockChip — one lock stat as a compact badge (the PG "Locks by
// mode" chip look, plus an optional tone for paging thresholds).
function LockChip({ label, value, tone }: {
  label: string; value: string; tone?: 'ok' | 'warn' | 'err';
}) {
  const fg = tone === 'err' ? 'var(--err)' : tone === 'warn' ? 'var(--warn)' : 'var(--text2)';
  const bg = tone === 'err' ? 'rgba(248,81,73,0.12)'
           : tone === 'warn' ? 'rgba(245,179,67,0.12)'
           : 'var(--bg3)';
  return (
    <span style={{
      fontSize: 11, padding: '3px 8px', borderRadius: 3,
      background: bg, color: fg,
      fontFamily: 'ui-monospace, SFMono-Regular, monospace',
    }}>
      {label} <span style={{ fontWeight: 600 }}>{value}</span>
    </span>
  );
}
