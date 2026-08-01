import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { AnnotationLane, KIND_GLYPH, KIND_COLOR, KIND_LABEL_TR } from './AnnotationLane';

// ServiceAnnotationLane — v0.9.397: v0.9.395'te Service.tsx içinde doğan
// veri sarmalayıcısı ortak dosyaya çıktı; Details VE Overview aynı
// bileşeni kullanır (aynı queryKey → sekmeler arası cache paylaşımı,
// sunucu TTL'iyle hizalı 30s stale). Boş/hata görünmez-düşer: şerit
// süsleme değil sinyal — olay yoksa yer kaplamaz; şerit hatası triage'ı
// bloke etmez (RED panelleri kendi hatalarını zaten söylüyor).
export function ServiceAnnotationLane({ service, fromNs, toNs, onZoomTo }: {
  service: string; fromNs: number; toNs: number;
  onZoomTo: (fromSec: number, toSec: number) => void;
}) {
  const q = useQuery({
    queryKey: ['annotations', service, fromNs, toNs],
    queryFn: () => api.annotations(service, fromNs, toNs),
    enabled: !!service, staleTime: 30_000,
  });
  const items = q.data?.items ?? [];
  if (items.length === 0) return null;
  // v0.9.492 (operatör: "alev ve tick işaretleri anlaşılmıyor") — şeridin
  // altında yalnız GÖRÜNEN türlerin mini lejantı: 🔥 alarm · ✓ çözüldü …
  // Simge sözlüğü artık kendini açıklıyor; tür yoksa lejantta da yok.
  const presentKinds = [...new Set(items.map(it => it.kind))]
    .filter(k => KIND_GLYPH[k]);
  return (
    <div style={{ margin: '0 0 10px' }}>
      <AnnotationLane items={items} fromNs={fromNs} toNs={toNs} onZoomTo={onZoomTo} />
      <div style={{
        display: 'flex', gap: 12, alignItems: 'center', flexWrap: 'wrap',
        fontSize: 10, color: 'var(--text3)', marginTop: 2,
      }}>
        {presentKinds.map(k => (
          <span key={k}>
            <span style={{ color: KIND_COLOR[k] }}>{KIND_GLYPH[k]}</span>
            {' '}{KIND_LABEL_TR[k] ?? k}
          </span>
        ))}
        {q.data?.truncated && (
          <span style={{ marginLeft: 'auto' }}>
            ⚠ 500 olay tavanı — pencereyi daralt (kesme ifşası)
          </span>
        )}
      </div>
    </div>
  );
}
