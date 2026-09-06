import { useMemo, useState } from 'react';
import { Button } from '@/components/ui/Button';
import { StatementAlertModal } from './alerts/StatementAlertModal';
import { useAuth } from '@/components/AuthProvider';
import { Link, useSearchParams } from 'react-router-dom';
import { navHref } from '@/lib/navHref';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { PageShell } from '@/components/ui/PageShell';
import { usePageZoomRange } from '@/lib/chart/usePageZoomRange';
import { timeRangeToNs } from '@/lib/utils';
import { useDBStmtDetail } from '@/lib/queries';
import { decodeStmtParam } from '@/pages/slowqueries/stmtParam';
import {
  StmtText, StmtSummarySection, StmtTrendSection,
  StmtCallersSection, StmtExemplarsSection,
} from '@/pages/slowqueries/stmtDetailSections';
import type { DBStmtDetail } from '@/lib/types';

// /databases/statement — the full-page statement detail (v0.9.1374).
//
// WHY A PAGE. Operator, on prod v0.9.1369: "Bak top statements çekmece
// açıyor bir satır üzerine basınca." The row click opened a 620px
// right-side drawer, and that drawer had to hold the statement SQL, a
// six-tile summary, three sparkline rows, a six-column caller table and
// the exemplar pivots. Every one of those wants width; a drawer is the
// one surface that has none to give.
//
// Same move, same reason, one page-conversion later than its siblings:
// /endpoint (v0.9.8xx) and /database (v0.9.840) both retired their
// drawers for exactly this. The statement drawer was the last one left
// in the databases family, which is why the operator kept meeting it
// after the /database conversion looked finished.
//
// NO NEW BACKEND. One /api/databases/statements/detail read — the same
// one the drawer issued, same params, same React Query key, so a click
// through from a warm catalog is served from cache.
//
// URL CONTRACT UNCHANGED. `?stmt=<hash>|<system>` (encodeStmtParam) and
// `?stmtcmp=1` are read here verbatim, so every link that already
// existed — including ones an operator has in a bookmark — resolves to
// the same statement class. Only the DESTINATION moved; the identity
// grammar did not.
export default function StatementDetailPage() {
  const [params, setParams] = useSearchParams();
  const search = params.toString();
  const refObj = useMemo(() => decodeStmtParam(params.get('stmt')), [params]);
  const { range, setRange } = usePageZoomRange('1h');
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);

  // Compare toggle rides the URL (house rule §4): ?stmtcmp=1,
  // replace:true, foreign params preserved.
  const compare = params.get('stmtcmp') === '1';
  // v0.10.331 — alarm kuralı modalı; yalnız yazma rolü görür (viewer salt-okur).
  const { user } = useAuth();
  const canEditRules = user?.role === 'admin' || user?.role === 'editor';
  const [alertOpen, setAlertOpen] = useState(false);
  const setCompare = (v: boolean) => setParams(prev => {
    const next = new URLSearchParams(prev);
    if (v) next.set('stmtcmp', '1'); else next.delete('stmtcmp');
    return next;
  }, { replace: true });

  // `null` = sorma. Hook'un kendi sözleşmesi bu (params !== null →
  // enabled); geçersiz bir deep-link'te boş hash ile istek atmak,
  // backend'e kesin 400 olan bir soruyu sordurmak olurdu.
  const detailQ = useDBStmtDetail(refObj ? {
    hash: refObj.hash,
    ...(refObj.system ? { system: refObj.system } : {}),
    from, to,
    ...(compare ? { compare: 'prior' as const } : {}),
  } : null);
  const detail: DBStmtDetail | null | undefined =
    !refObj ? null : detailQ.isPending ? undefined : detailQ.isError ? null : detailQ.data;

  if (!refObj) {
    return (
      <>
        <Topbar title="Statement" />
        <PageShell>
          <Empty icon="⚠" title="No statement in this link">
            The URL is missing a valid <code>stmt</code> identity.
            {' '}<Link to={navHref('/databases', search)}>Back to Databases →</Link>
          </Empty>
        </PageShell>
      </>
    );
  }

  const dbSystem = detail?.summary?.dbSystem || '';
  const dbName = detail?.summary?.dbName || '';

  return (
    <>
      <Topbar title="Statement" range={range} onRangeChange={setRange} />
      <PageShell>
        {/* Geri/kırıntı linkleri pencereyi ve env'i TAŞIR (v0.9.1320
            kuralı): bir geri linkinin bağlamı değiştirmesi, geri linki
            olmaktan çıkması demek. */}
        <div style={{ fontSize: 11.5, color: 'var(--text3)', marginBottom: 8 }}>
          <Link to={navHref('/databases', search)}>Databases</Link>
          {' › '}
          <Link to={navHref('/databases/slow-queries', search)}>Top statements</Link>
          {' › statement detail'}
        </div>

        <div style={{
          display: 'flex', alignItems: 'center', gap: 8,
          flexWrap: 'wrap', marginBottom: 12,
        }}>
          <span className="badge b-gray mono" style={{ fontSize: 10 }}>{dbSystem || 'db'}</span>
          {dbName && dbName !== 'default' && (
            <span className="badge b-info mono" style={{ fontSize: 10 }}
              title="db.name this statement class runs against">
              {dbName}
            </span>
          )}
          <span style={{ fontSize: 15, fontWeight: 600 }}>Statement detail</span>
          <span className="mono" style={{ fontSize: 10, color: 'var(--text3)' }}
            title={`Persistent statement identity (stmt_hash ${refObj.hash})`}>
            #{refObj.hash.slice(0, 8)}
          </span>
          {/* v0.10.331 — bu ifade için hedefli alarm kuralı (editör/admin).
              v0.10.516 (operatör: "Alarm oluştur yazısı çok küçük") — xs → sm. */}
          {canEditRules && (
            <Button variant="secondary" size="sm" style={{ marginLeft: 'auto' }} onClick={() => setAlertOpen(true)}
              title="Bu ifade için eşik alarmı: p95/p99/max/avg süresi eşiği geçince Problem + DB sahibi/SRE maili">
              ⚠ Alarm oluştur
            </Button>
          )}
          <label style={{
            fontSize: 11, display: 'flex', alignItems: 'center', gap: 4,
            cursor: 'pointer', marginLeft: canEditRules ? undefined : 'auto', whiteSpace: 'nowrap',
          }}
            title="Compare current window against the immediately-preceding equal-length window. Adds a second backend read; off by default.">
            <input type="checkbox" checked={compare}
              onChange={e => setCompare(e.target.checked)} />
            vs prior
          </label>
        </div>

        <StmtText
          statement={detail?.statement || ''}
          sample={detail?.summary?.sampleStatement || ''} />
        {alertOpen && (
          <StatementAlertModal open onClose={() => setAlertOpen(false)}
            target={{ kind: 'db_statement', dbSystem: dbSystem || refObj.system || '', dbName: dbName === 'default' ? '' : dbName, stmtHash: refObj.hash, sample: (detail?.statement || detail?.summary?.sampleStatement || '').slice(0, 300) }} />
        )}

        {detail === undefined && <Spinner />}
        {detail === null && (
          <Empty icon="⚠" title="Detail query failed">
            The backend /api/databases/statements/detail request errored.
            {' '}<Link to={navHref('/databases/slow-queries', search)}>Back to the catalog →</Link>
          </Empty>
        )}

        {detail && (
          <>
            <StmtSummarySection detail={detail} compare={compare} />
            {/* Sayfa genişliği çekmecenin 420px'inden fazlasını veriyor —
                dönüşümün asıl kazancı bu, sabiti taşımak onu çöpe atardı. */}
            <StmtTrendSection detail={detail} sparkWidth={720} />
            <StmtCallersSection detail={detail} compare={compare} range={range} />
            <StmtExemplarsSection detail={detail} range={range} />
          </>
        )}
      </PageShell>
    </>
  );
}
