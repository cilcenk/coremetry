import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { GoDuration } from '@/lib/utils';
import { keys } from './keys';
import type { Problem, EvaluatorHealth, BlastRadius } from '@/lib/types';

// /api/problems — the open-incident inbox feeding /problems,
// /anomalies, the sidebar badge, and several deep-link
// drill-downs. With React Query the same data is shared across
// all consumers — when the sidebar's 30s poll fetches, the
// /problems page that's also mounted gets the new data without
// its own request. Single source of truth, single network call.
//
// `service` filter is part of the key, so /problems?service=foo
// caches separately from the global list — switching back and
// forth between the two doesn't refetch.
export function useProblems(filter: {
  status?: 'open' | 'all' | 'resolved';
  service?: string;
  priority?: string[];
  ownerTeam?: string;
  sreTeam?: string;
  // v0.8.387 — the global ?env= picker; service-scoped on problems
  // (rows whose service ran in the env in the last hour + global
  // service-less alerts). Part of the key via the filter object.
  env?: string;
  // Cluster filter (v0.9.181) — narrows to problems whose service touched
  // this cluster (server-side, post-enrich on p.clusters). Part of the key
  // via the filter object.
  cluster?: string;
  limit?: number;
  // v0.9.528 — `enabled`, useOpenCriticalCount'un deseninin aynısı.
  // Varsayılan açık, yani mevcut çağıranların hiçbiri değişmez. CoSRE
  // karşılaması bunu KAPALI tutar: sorgu yalnız sohbet penceresi
  // açıkken ve henüz soru sorulmamışken koşsun — aksi hâlde her
  // sayfada arka planda 30s'lik bir poll daha açılırdı.
}, opts?: { enabled?: boolean }) {
  // v0.9.455 — zarf: {items,total,truncated}; null gövde boş-dürüst
  // zarfa normalize edilir (truncated:false, total:-1 = bilinmiyor).
  return useQuery<{ items: Problem[]; total: number; truncated: boolean }>({
    queryKey: keys.problems.list(filter),
    queryFn: async () => {
      const res = await api.problems(filter);
      return res ?? { items: [], total: -1, truncated: false };
    },
    refetchInterval: 30_000,
    staleTime: 25_000,
    enabled: opts?.enabled ?? true,
  });
}

// useProblemByID (v0.9.825) — bildirim derin linkinin YEDEK yolu.
//
// AlertProblemHost önce listeden çözer (sıfır maliyet: açık
// problemlerin neredeyse tamamı zaten yüklü olan sayfalardadır).
// Bulamazsa bu hook devreye girer. Sıra bilinçli — her derin link için
// koşulsuz bir tekil okuma açmak, listeden zaten gelen satırlar için
// bedava bir CH sorgusu demek olurdu.
//
// `enabled` çağıranda: listede BULUNAMADIĞINDA açılır. Yani sorgu ancak
// gerçekten gerekince koşar.
//
// data === null → kayıt gerçekten yok (404). undefined → henüz
// yüklenmedi. İkisi ayrı: "bulunamadı" ekranını yalnız null çizmeli,
// yoksa yükleme anında yanlış cümle kurulur.
export function useProblemByID(id: string, opts?: { enabled?: boolean }) {
  return useQuery<Problem | null>({
    queryKey: keys.problems.byID(id),
    queryFn: () => api.problem(id),
    enabled: (opts?.enabled ?? true) && !!id,
    // Tek kayıt okuması; sunucu 15 sn cache'liyor. Daha sık sormak yeni
    // bir şey öğretmez — bu ekran bir ARŞİV görünümü, canlı bir liste değil.
    staleTime: 15_000,
    // Yok olan bir kimlik yeniden denemekle var olmaz.
    retry: (count, err) => (err instanceof Error && err.message.startsWith('HTTP 404') ? false : count < 2),
  });
}

// Open-problem count for the sidebar badge. v0.5.398 — switched
// from fetching limit=200 rows + counting the array to a
// dedicated /api/problems/count endpoint. The old approach
// capped the displayed badge at 200 silently on installs with
// >200 open problems; the new path returns the true count via
// a single COUNT(*) on the server.
// env (v0.8.387) — the sidebar passes the global picker's value so
// the badge agrees with the env-filtered /problems list (same
// ProblemFilter.Env conjunct server-side; still one COUNT query per
// poll — the env→services map is 60s-cached on the server).
export function useOpenProblemCount(env?: string) {
  return useQuery<{ count: number }, Error, number>({
    queryKey: ['problems', 'count', { status: 'open', env: env || '' }],
    queryFn: async () =>
      (await api.problemsCount({ status: 'open', env: env || undefined })) ?? { count: 0 },
    select: (r) => r.count,
    refetchInterval: 30_000,
    staleTime: 25_000,
  });
}

// useOpenCriticalCount (v0.9.169) — open CRITICAL count for the AI launcher's
// proactive dot. Deliberately narrower than useOpenProblemCount (all-open,
// which drives the sidebar /problems badge): the AI nudge fires ONLY on open
// critical problems, so the FAB dot means "something needs attention NOW" —
// not "a P3 lingers". Same 30s cadence; React Query pauses the poll while the
// tab is hidden. `enabled:false` keeps it dormant when the copilot is off.
export function useOpenCriticalCount(opts?: { env?: string; enabled?: boolean }) {
  return useQuery<{ count: number }, Error, number>({
    queryKey: ['problems', 'count', { status: 'open', severity: 'critical', env: opts?.env || '' }],
    queryFn: async () =>
      (await api.problemsCount({ status: 'open', severity: 'critical', env: opts?.env || undefined })) ?? { count: 0 },
    select: (r) => r.count,
    refetchInterval: 30_000,
    staleTime: 25_000,
    enabled: opts?.enabled ?? true,
  });
}

// v0.9.550 — evaluator kalp atışı.
//
// Operatör: "bazen sanki takıldığını hissediyorum." Bu hook o hissi
// ölçüye çevirir: boş bir Problems sayfasının "sorun yok" mu yoksa
// "evaluator ölü" mü olduğunu ayırt eder — öncesinde ikisi ekranda
// birebir aynı görünüyordu.
//
// 30s poll: evaluator dakikada bir tik atar, daha sık sormak yeni bir
// şey öğretmez. Redis'ten tek GET, sunucuda ek maliyeti yok.
export function useEvaluatorHealth() {
  return useQuery<EvaluatorHealth>({
    queryKey: ['problems', 'evaluator-health'],
    queryFn: async () =>
      (await api.evaluatorHealth()) ?? {
        // Ağ hatası = ÖLÇEMEDİK. 'ok' varsaymak, bu sürümün
        // düzelttiği hatayı istemci tarafında yeniden kurardı.
        status: 'unknown', reason: 'Sağlık okunamadı (ağ hatası).',
        ageSec: -1, durationMs: 0, rules: 0, opened: 0, resolved: 0,
      },
    refetchInterval: 30_000,
    staleTime: 25_000,
  });
}

// v0.10.260 (perf §7 madde 4, F3 ⭐) — inbox'ın açık problem satırları için
// TEK blast-radius isteği. Anahtar sıralı servis kümesi; staleTime = sunucu
// TTL (60 s); boş kümede fetch yok; signal ile iptal (cancellation.test.ts).
export function useBlastRadiusBatch(services: string[], since: GoDuration = '1h') {
  return useQuery<{ items: Record<string, BlastRadius>; since: string }>({
    queryKey: keys.problems.blastRadius(services, since),
    queryFn: ({ signal }) => api.blastRadiusBatch(services, since, signal),
    staleTime: 60_000,
    enabled: services.length > 0,
  });
}
