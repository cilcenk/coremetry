import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { AnnotationLane } from './AnnotationLane';

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
  return (
    <div style={{ margin: '0 0 10px' }}>
      <AnnotationLane items={items} fromNs={fromNs} toNs={toNs} onZoomTo={onZoomTo} />
      {q.data?.truncated && (
        <div style={{ fontSize: 10, color: 'var(--text3)' }}>
          ⚠ 500 olay tavanı — pencereyi daralt (kesme ifşası)
        </div>
      )}
    </div>
  );
}
