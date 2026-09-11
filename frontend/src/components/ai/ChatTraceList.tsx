import { Link } from 'react-router-dom';
import { traceHref } from '@/lib/traceHref';
import { tsShort } from '@/lib/utils';
import type { ChatTraceListPayload } from '@/lib/types';

// ChatTraceList — v0.10.688 (endpoint_traces.go trace_list bloğu; D6 "trace
// listesi cevabı"): LLM anlatımı yok, deterministik tablo. Satır = trace
// linki (traceHref üreticisi), altta "Daha fazla → Traces" aynı süzgeçle
// (deepLink sunucudan). Markdown tablo dili (.cm-md-table) — ikinci tablo
// görünümü olmasın.
export function ChatTraceList({ tl }: { tl: ChatTraceListPayload }) {
  if (tl.traces.length === 0) return null;
  return (
    <div className="cm-trace-list">
      <table className="cm-md-table">
        <thead>
          <tr><th>Başlangıç</th><th>Servis</th><th>İşlem</th><th className="num">Süre</th><th className="num">Span</th><th>Durum</th></tr>
        </thead>
        <tbody>
          {tl.traces.map(t => (
            <tr key={t.traceId}>
              <td className="mono">{tsShort(t.startTime)}</td>
              <td>{t.serviceName}</td>
              <td><Link to={traceHref(t.traceId)} title={t.traceId}>{t.rootName || t.traceId}</Link></td>
              <td className="num mono">{t.durationMs >= 1000 ? (t.durationMs / 1000).toFixed(2) + ' s' : t.durationMs.toFixed(0) + ' ms'}</td>
              <td className="num mono">{t.spanCount}</td>
              <td><span className={`badge ${t.hasError ? 'b-err' : 'b-ok'}`}>{t.hasError ? 'ERROR' : 'OK'}</span></td>
            </tr>
          ))}
        </tbody>
      </table>
      <div className="cm-trace-list__more">
        <Link to={tl.deepLink}>Daha fazla → Traces</Link>
        {tl.truncated && <span className="cell-hint"> · ilk {tl.traces.length} satır</span>}
      </div>
    </div>
  );
}
