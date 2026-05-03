'use client';
import { Suspense, useEffect, useState } from 'react';
import { useRouter, useSearchParams } from 'next/navigation';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { FlameGraph } from '@/components/FlameGraph';
import { CopyButton } from '@/components/CopyButton';
import { api } from '@/lib/api';
import { tsLong, fmtNum } from '@/lib/utils';
import type { ProfileDetail, TimeRange } from '@/lib/types';

function ProfileDetailInner() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const id = searchParams.get('id') ?? '';

  const [range, setRange] = useState<TimeRange>({ preset: '24h' });
  const [data, setData] = useState<ProfileDetail | null | undefined>(undefined);

  useEffect(() => {
    if (!id) return;
    setData(undefined);
    api.profile(id).then(setData).catch(() => setData(null));
  }, [id]);

  return (
    <>
      <Topbar title="Profile" range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{ marginBottom: 12, display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap' }}>
          <button className="sec" onClick={() => router.back()}>← Back</button>
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
              <span style={{ fontSize: 12, color: 'var(--text3)', marginLeft: 'auto' }}>
                {fmtNum(data.meta.sampleCount)} samples
              </span>
            </>
          )}
        </div>

        {!id && <Empty icon="⚠" title="Missing profile id" />}
        {id && data === undefined && <Spinner />}
        {id && data === null && <Empty icon="⚠" title="Profile not found or failed to parse" />}
        {data && data.flame && <FlameGraph root={data.flame} />}
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
