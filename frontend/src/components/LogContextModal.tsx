import { useEffect, useState, useMemo } from 'react';
import { Modal, IconButton, Button } from '@/components/ui';
import { Spinner } from '@/components/Spinner';
import { api } from '@/lib/api';
import { fmtNum, tsLong } from '@/lib/utils';
import { useUrlEnv } from '@/lib/useUrlEnv';
import type { LogRow } from '@/lib/types';
import { highlightSegments } from '@/lib/logFilters';
import { podOfLog } from '@/lib/logPod';

// LogContextModal — v0.5.402. Datadog "Context" tab for /logs.
// Operator clicks "≡ ±50" on an expanded log row → this modal
// fetches the 50 logs immediately BEFORE and 50 AFTER the pivot
// timestamp, scoped to the same service. Pivot row gets a yellow
// border so it stands out in the chronological scroll.
//
// Why: investigating a single error log is rarely enough. The
// surrounding lines almost always reveal what state the service
// was in just before failing + what it tried to do after.
// Datadog's "View in Context" is one of their most-used affordances;
// porting it here closes the same loop.
//
// Server returns: { before: LogRow[] (DESC), after: LogRow[] (ASC),
// pivotTs: number }. We re-sort the union ascending so the operator
// scrolls top→bottom in chronological order. Pivot row is found by
// (timestamp == pivotTs AND id == pivotId) — relying on ts alone
// can collide on busy services where two logs hit the same ns
// bucket.
export function LogContextModal({
  pivot, onClose, onTracePeek, highlightTerms, search,
}: {
  pivot: LogRow | null;
  onClose: () => void;
  onTracePeek?: (traceId: string) => void;
  // v0.9.1215 (Kibana paritesi, dilim 6) — pivot sorgusunun serbest-metin
  // terimleri bağlam satırlarında da vurgulanır (mesaj hücresiyle aynı
  // yardımcı + 4KB tarama tavanı; saf istemci işi).
  highlightTerms?: string[];
  // v0.9.1224 — sayfanın derlenmiş sorgusu; "⧩ sorguyu koru" anahtarı
  // açılırsa iki bağlam yarısına da taşınır (Kibana context filtreleri).
  search?: string;
}) {
  const [before, setBefore] = useState<LogRow[] | null | undefined>(undefined);
  const [after,  setAfter]  = useState<LogRow[] | null | undefined>(undefined);
  // v0.9.1218 (Kibana paritesi, dilim 2) — artımlı pencere + kapsam.
  // n yalnız TIKLAMAYLA büyür (sunucu tavanı 200; her (n, kapsam) kendi
  // 15 sn'lik sunucu cache anahtarı — ES disiplini korunur, otomatik
  // yükleme/scroll-fetch yok). scopeService=false pivotun servıs
  // filtresini düşürür: kaskad okuma ("aynı anda BAŞKA servisler ne
  // yazdı") Kibana'nın tek-index görünümünün karşılığı.
  const [n, setN] = useState(50);
  const [scopeService, setScopeService] = useState(true);
  // v0.9.1249 (Kibana paritesi) — POD kapsamı. Kalabalık bir serviste
  // ±pencere BAŞKA podların satırlarıyla dolar; incelenen olay tek
  // podun hikâyesiyken komşuluk çok-pod karışımı olur. Varsayılan
  // KAPALI (bağlam = geniş komşuluk, servis kapsamıyla aynı ruh);
  // operatör tek tıkla daraltır. Pivotun podu ÇIKARILAMAZSA düğme
  // hiç çizilmez — kapsayamayacağımız kapsamı vaat etmeyiz.
  const [scopePod, setScopePod] = useState(false);
  // v0.9.1224 — varsayılan KAPALI: bağlam normalde süzgeçsiz komşuluk
  // (Kibana varsayılanı da bu); operatör isterse aktif sorguyu taşır.
  const [keepQuery, setKeepQuery] = useState(false);
  // v0.8.400 — the global ?env= filter narrows both context halves: the
  // operator's scenario is the SAME service name deployed in several
  // environments, and context around a pivot must not interleave the
  // other envs' lines.
  const [env] = useUrlEnv();

  // Pivotun pod kimliği — paylaşılan saf çıkarım (lib/logPod.ts); alan
  // adı varyantları backend'in esPodFields listesinin aynası.
  const pivotPod = useMemo(() => podOfLog(pivot), [pivot]);

  // Pivot değişince pencere/kapsam sıfırlanır — önceki kaydın
  // büyütülmüş penceresi yeni kayda taşınmaz.
  useEffect(() => { setN(50); setScopeService(true); setKeepQuery(false); setScopePod(false); }, [pivot?.id]);

  useEffect(() => {
    if (!pivot) {
      setBefore(undefined); setAfter(undefined);
      return;
    }
    let cancelled = false;
    setBefore(undefined); setAfter(undefined);
    api.logsContext({
      ts: pivot.timestamp,
      service: scopeService ? (pivot.serviceName || undefined) : undefined,
      env: env || undefined,
      pod: scopePod && pivotPod ? pivotPod : undefined,
      n,
      search: keepQuery && search ? search : undefined,
    })
      .then(r => {
        if (cancelled) return;
        setBefore(r?.before ?? []);
        setAfter(r?.after ?? []);
      })
      .catch(() => {
        if (cancelled) return;
        setBefore(null); setAfter(null);
      });
    return () => { cancelled = true; };
  }, [pivot, env, n, scopeService, keepQuery, search, scopePod, pivotPod]);

  // Unified chronological list with pivot inserted between the two
  // halves. Both halves arrive sorted (before DESC, after ASC) so
  // we reverse `before` then concat.
  const rows = useMemo<{ pivotKey: number; rows: LogRow[] } | null>(() => {
    if (!pivot) return null;
    const b = (before ?? []).slice().sort((a, c) => a.timestamp - c.timestamp);
    const a = (after  ?? []).slice().sort((x, y) => x.timestamp - y.timestamp);
    // Pivot might already appear in `before` or `after` since the
    // window is inclusive at both ends. Dedupe by log id so we
    // don't render it twice.
    const seen = new Set<number>([pivot.id]);
    const dedupedBefore = b.filter(l => { if (seen.has(l.id)) return false; seen.add(l.id); return true; });
    const dedupedAfter  = a.filter(l => { if (seen.has(l.id)) return false; seen.add(l.id); return true; });
    return {
      pivotKey: pivot.id,
      rows: [...dedupedBefore, pivot, ...dedupedAfter],
    };
  }, [pivot, before, after]);

  if (!pivot) return <Modal open={false} onClose={onClose} />;

  return (
    <Modal
      open
      onClose={onClose}
      size="lg"
      title={
        <span style={{ fontSize: 13 }}>
          Context · ±{n}
          <span style={{ color: 'var(--text3)', marginLeft: 8, fontSize: 11 }}>
            {scopeService ? (pivot.serviceName || '(no service)') : 'tüm servisler'}
            {scopePod && pivotPod ? ` · ${pivotPod}` : ''} · {tsLong(pivot.timestamp)}
          </span>
        </span>
      }
    >
      {(before === undefined || after === undefined) && <Spinner />}
      {(before === null || after === null) && (
        <div style={{ fontSize: 12, color: 'var(--err)' }}>
          Failed to load surrounding context.
        </div>
      )}
      {rows && (
        <>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap',
            fontSize: 11, color: 'var(--text3)', marginBottom: 8 }}>
            <span>
              {fmtNum(before?.length ?? 0)} log{(before?.length ?? 0) === 1 ? '' : 's'} before
              {' · '}
              {fmtNum(after?.length ?? 0)} after
              {' · '}30 dk simetrik pencere
            </span>
            {/* v0.9.1218 — artımlı pencere + kapsam anahtarı. */}
            <Button variant="secondary" size="sm" disabled={n >= 200}
              onClick={() => setN(v => Math.min(200, v + 50))}
              title={n >= 200 ? 'Sunucu tavanı ±200' : 'Pencereyi ±50 büyüt (tavan ±200)'}>
              ±50 daha
            </Button>
            {pivot.serviceName && (
              <Button variant="secondary" size="sm"
                onClick={() => setScopeService(v => !v)}
                title={scopeService
                  ? 'Servis filtresini bırak — aynı anda TÜM servislerin satırları (kaskad okuma)'
                  : `Yalnız ${pivot.serviceName} satırlarına dön`}>
                {scopeService ? '⇲ Tüm servisler' : `⇱ Yalnız ${pivot.serviceName}`}
              </Button>
            )}
            {/* v0.9.1249 — pod kapsamı. Pivotun podu çıkarılamıyorsa
                düğme HİÇ çizilmez: uygulayamayacağımız bir kapsamı
                sunmak, boş sonuçtan beter bir yalandır. */}
            {!!pivotPod && (
              <Button variant="secondary" size="sm"
                onClick={() => setScopePod(v => !v)}
                title={scopePod
                  ? 'Pod filtresini bırak — pencerenin TÜM podları'
                  : `Yalnız ${pivotPod} podunun satırları (kalabalık serviste komşuluk çok-pod karışımıdır)`}>
                {scopePod ? '⌖ pod kapsamı AÇIK' : '⌖ Yalnız bu pod'}
              </Button>
            )}
            {/* v0.9.1224 — filtre-koruma: yalnız sayfada aktif sorgu VARSA
                çizilir; varsayılan kapalı (bağlam = süzgeçsiz komşuluk). */}
            {!!search && (
              <Button variant="secondary" size="sm"
                onClick={() => setKeepQuery(v => !v)}
                title={keepQuery
                  ? 'Sorgu filtresini bırak — pencerenin TÜM satırları'
                  : 'Sayfadaki aktif sorguyu bağlam satırlarına da uygula'}>
                {keepQuery ? '⧨ sorgu filtresi AÇIK' : '⧩ sorguyu koru'}
              </Button>
            )}
          </div>
          <div style={{
            border: '1px solid var(--border)', borderRadius: 6,
            background: 'var(--bg1)',
            maxHeight: 480, overflowY: 'auto',
          }}>
            {rows.rows.map(l => {
              const isPivot = l.id === rows.pivotKey;
              const offsetMs = (l.timestamp - pivot.timestamp) / 1e6;
              const sev = (l.severityText || '').toUpperCase();
              return (
                // v0.9.983 (D5.8 / C7) — beş sabit kolon 290px eder ve
                // 366px'lik ekranda MESAJA 76px kalırdı. Izgara sınıfa
                // taşındı; masaüstü aynı, <640px'te tek kolon.
                <div key={l.id} className="lcm-row" style={{
                  gap: 6, padding: '3px 8px',
                  fontSize: 11, fontFamily: 'ui-monospace, monospace',
                  borderBottom: '1px solid var(--bg2)',
                  alignItems: 'baseline',
                  background: isPivot ? 'rgba(250,204,21,0.10)' : 'transparent',
                  borderLeft: isPivot ? '3px solid var(--warn, #facc15)' : '3px solid transparent',
                }}>
                  <span style={{ color: 'var(--text3)', textAlign: 'right' }}>
                    {offsetMs >= 0 ? `+${offsetMs.toFixed(0)}ms` : `${offsetMs.toFixed(0)}ms`}
                  </span>
                  <span className={sevClass(sev)} style={{ fontWeight: 600 }}>
                    {sev.slice(0, 4) || '—'}
                  </span>
                  <span style={{ color: 'var(--text2)',
                    overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                  }} title={l.serviceName}>
                    {l.serviceName}
                  </span>
                  <span style={{
                    overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                  }} title={l.body}>
                    {highlightTerms && highlightTerms.length > 0
                      ? highlightSegments(l.body, highlightTerms).map((seg, i) =>
                          seg.hl ? <mark key={i}>{seg.text}</mark> : <span key={i}>{seg.text}</span>)
                      : l.body}
                  </span>
                  <span style={{ textAlign: 'right' }}>
                    {l.traceId && onTracePeek && (
                      <IconButton
                        variant="bare" size="xs" className="ib-accent"
                        onClick={() => onTracePeek(l.traceId)}
                        aria-label={`Peek trace ${l.traceId.slice(0, 12)}`}
                        title={`Peek trace ${l.traceId.slice(0, 12)}…`}
                        icon="👁" />
                    )}
                  </span>
                </div>
              );
            })}
          </div>
        </>
      )}
    </Modal>
  );
}

function sevClass(s: string): string {
  switch (s) {
    case 'FATAL':
    case 'ERROR':   return 'sev-err';
    case 'WARN':
    case 'WARNING': return 'sev-warn';
    case 'INFO':    return 'sev-info';
    default:        return 'sev-dim';
  }
}
