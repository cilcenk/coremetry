import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { DBTrend, DBDetail, MessagingDetail } from '@/lib/types';

// v0.10.576 — /databases ve /messaging'in PAYLAŞTIĞI iki okuma.
//
// İkisi de çıplak `useEffect` + `let live = true` kalıbındaydı: React Query'ye
// taşınmalarının sebebi kozmetik değil, iptal. Eski kalıpta `live` bayrağı
// yalnız SONUCU yok sayıyordu — istek ClickHouse'ta sonuna kadar koşuyordu
// (v0.9.617'nin ölçtüğü sınıfın aynısı). Operatör aralığı değiştirdiğinde ya
// da çekmeceyi kapattığında sunucu tarafı iş şimdi gerçekten kesiliyor.
//
// Bu dosya `databases.ts`/`messaging.ts` yerine AYRI: iki hook da `kind` ile
// dallanıyor ve iki sayfaya birden hizmet ediyor; birinin dosyasına koymak
// diğerini yanlış yere bağımlı kılardı.
//
// staleTime = sunucu TTL (30 s, dört ucun dördü de) — daha kısası sunucunun
// zaten önbellekte tuttuğu cevabı boşuna tekrar isterdi.

// useDepTrends — satır-içi trend sütunu (sparkline). `enabled=false` iken
// sorgu HİÇ kurulmaz: /messaging tarafında sütun çizilmiyor bile.
export function useDepTrends(p: {
  kind: 'db' | 'queue'; fromNs: number; toNs: number; enabled?: boolean;
}) {
  return useQuery<DBTrend[] | null>({
    queryKey: ['deps', 'trends', p.kind, p.fromNs, p.toNs],
    queryFn: ({ signal }) => (p.kind === 'db'
      ? api.dbTrends(p.fromNs, p.toNs, signal)
      : api.msgTrends(p.fromNs, p.toNs, signal)),
    enabled: p.enabled ?? true,
    staleTime: 30_000,
  });
}

// useDepDetail — çekmece yükü. Kimlik ÜÇLÜ ve görünen etiketten bağımsız
// (v0.9.821): db tarafında (system, instance, dbName), queue tarafında
// (system, cluster, destination). Anahtar bu üçlüyü OLDUĞU GİBİ taşır —
// gevşek anahtar farklı cluster'daki aynı destination'ı ezerdi.
export function useDepDetail(p: {
  kind: 'db' | 'queue';
  system: string; cluster: string; name: string;
  instance?: string; dbName?: string;
  fromNs: number; toNs: number; enabled?: boolean;
}) {
  return useQuery<DBDetail | MessagingDetail | null>({
    queryKey: ['deps', 'detail', p.kind, p.system, p.cluster, p.name,
      p.instance ?? '', p.dbName ?? '', p.fromNs, p.toNs],
    queryFn: ({ signal }) => (p.kind === 'db'
      ? api.databaseDetail(p.system, p.instance ?? p.name, p.dbName ?? '', p.fromNs, p.toNs, signal)
      : api.messagingDetail(p.system, p.cluster, p.name, p.fromNs, p.toNs, signal)),
    enabled: (p.enabled ?? true) && !!p.system && !!p.name,
    staleTime: 30_000,
  });
}
