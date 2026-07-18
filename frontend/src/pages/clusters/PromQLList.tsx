// PromQLList — etiket + <pre> sorgu listesi (README'nin PromQL kart
// idiomu; v0.9.51'de Clusters.tsx'ten çıkarıldı). Overview kartı,
// pod drawer'ı (v0.9.40) ve Service→Infra sekmesi (§8) paylaşır.
// Değerler çağıran tarafta promQuote ile kaçışlanır — display-only.
export function PromQLList({ queries }: { queries: [string, string][] }) {
  return (
    <div style={{ display: 'grid', gap: 10 }}>
      {queries.map(([label, q]) => (
        <div key={label}>
          <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 3 }}>{label}</div>
          <pre style={{
            margin: 0, padding: '7px 9px', borderRadius: 4,
            background: 'var(--bg0)', border: '1px solid var(--border)',
            fontFamily: 'ui-monospace, monospace', fontSize: 11,
            whiteSpace: 'pre-wrap', wordBreak: 'break-all', color: 'var(--text2)',
          }}>{q}</pre>
        </div>
      ))}
    </div>
  );
}
