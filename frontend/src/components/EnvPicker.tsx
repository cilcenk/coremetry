import { useEffect, useRef, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { Combobox } from '@/components/Combobox';
import { shouldAutoCommit } from '@/components/ServicePicker';
import { useUrlEnv } from '@/lib/useUrlEnv';

// EnvPicker (v0.8.383 — env-separation Phase 1) — the GLOBAL
// deployment-environment filter, mounted once in the Topbar next to
// the range picker (Datadog's env tag / Dynatrace management-zone
// placement: the operator says "look at uat" and every page follows).
//
// v0.8.389 (operator-reported): the original plain <select> assumed
// the ≤~10-values rule — feature-branch envs (int-feature-*) broke
// it, and the alphabetical LIMIT 50 enumeration starved later names
// ("release" never appeared, unsearchable). Now the ServicePicker
// anatomy: debounced server search (?q=) over a count-ordered list,
// truncation labelled from the server's total, datalist pick
// auto-commits. The backend widens its scan clamp 1h→24h for
// searched lookups so quiet-but-real envs are findable by name.
//
// Selection writes `?env=` via useUrlEnv (replace:true, prev-copying,
// localStorage-mirrored) so it persists across navigations exactly
// like the range does, is shareable, and rides SavedViewsBar's
// whole-query-string snapshots. Viewer-visible — a read filter.
//
// Consumers: /traces (v0.8.383), /services + /endpoints (v0.8.385),
// /problems + /inbox + sidebar badge (v0.8.387, service-scoped),
// /logs (v0.8.400 — both backends; ES self-discovers its env field
// and reports honestly when none resolves).
//
// v0.9.864 (UX denetimi §4.3 seçenek (b), operatör onayı 2026-08-09) —
// `applies`: env yarı-uygulanan bir global filtre. Picker range verilen HER
// sayfada basılıyor ama tüketen sayfa sayısı sınırlı. Bugünkü ara durum en
// kötüsüydü: kullanıcıya UYGULANMAYAN bir filtre aktif gösteriliyordu, ve
// `env=uat` seçili bir operatör /messaging, /clusters, /hosts, /metrics
// gezerken tüm ortamların verisine bakıp env'e güveniyordu.
//
// Uygulamayan sayfada picker GİZLENMEZ, devre dışı bırakılır: gizlemek
// operatörün sticky bir env seçimi olduğunu ve burada yok sayıldığını
// göremeyeceği anlamına gelirdi — sessizce yanlış olan tam da buydu. ✕
// (temizle) çalışır kalır; bayat bir global env'i her sayfadan bırakabilmek
// tam olarak bu ekranda işe yarar.
//
// Varsayılan FALSE (opt-in): işaretlenmemiş bir sayfanın env'i uygulaMAdığı
// istatistiksel olarak da doğru, ve varsayılanın hata biçimi DÜRÜSTLÜK
// (atıl olduğu yazan atıl bir kontrol) — yalan değil. Seçenek (a) (env'i
// her API'ye gerçekten uygulamak) sayfa-başına backlog, bu sürümün dışında.
// isClearJump — tek bir onChange olayı "temizle" ANLAMINA mı geliyor?
//
// v0.9.1024: ✕ düğmesi eskiden EnvPicker'ındı ve doğrudan commit('')
// çağırıyordu. Combobox artık kendi ✕'ini çizdiği için sinyal
// dolaylı geliyor: değer TEK olayda boşa sıçrıyor. Sıçrama kuralı
// shouldAutoCommit'in aynadaki hâli — büyümede olduğu gibi
// KÜÇÜLMEDE de tek karakterlik adım "operatör yazıyor" demektir.
//
// Neden `prev.length > 1`: tek tek geri silerek boşaltan operatör
// (…, 'ua', 'u', '') son adımda prev='u' ile gelir. O yol commit
// ETMEZ — yarım silinmiş bir kutu global env filtresini düşürüp her
// sayfayı yeniden yüklemesin diye. O hâl blur'da kapanıyor (boşsa
// "tüm ortamlar"), eskisi gibi.
export function isClearJump(prev: string, next: string): boolean {
  return next === '' && prev.length > 1;
}

export function EnvPicker({ applies = false }: { applies?: boolean }) {
  const [env, setEnv] = useUrlEnv();
  // draft = what's in the input while typing; committed env only
  // changes on pick / Enter / clear so half-typed text never filters.
  const [draft, setDraft] = useState(env);
  const [q, setQ] = useState('');
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const lastValueRef = useRef(env);

  // Keep the input in sync when the env changes from outside (shared
  // link, SavedViews restore, another tab writing localStorage).
  useEffect(() => { setDraft(env); lastValueRef.current = env; }, [env]);

  const listQ = useQuery({
    queryKey: ['environments', q],
    queryFn: () => api.environments(q),
    staleTime: 120_000, // ≥ server TTL (60s) — cache-rung discipline
    refetchOnWindowFocus: false,
    retry: false,
  });
  const fetched = listQ.data?.environments ?? [];
  const total = listQ.data?.total ?? fetched.length;
  // Sticky/shared value stays selectable even when outside the
  // enumeration window (never validate a pick against a sampled
  // subset — the v0.8.265 lesson).
  const options = env && !fetched.includes(env) ? [env, ...fetched] : fetched;
  const truncated = total > fetched.length;

  // Installs without any deploy_env data get no extra chrome — but
  // never hide while a committed env or a search is active.
  if (options.length === 0 && !env && !q) return null;

  const commit = (v: string) => {
    setEnv(v);
    setDraft(v);
    lastValueRef.current = v;
  };

  const handleChange = (next: string) => {
    const prev = lastValueRef.current;
    lastValueRef.current = next;
    setDraft(next);
    if (debounceRef.current) clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(() => setQ(next.trim()), 180);
    // Pick sezgiseli (ServicePicker'ın saf fonksiyonu, v0.7.27
    // sözleşmesi): bilinen bir seçeneğe çok karakterli sıçrama =
    // listeden seçim → commit.
    if (shouldAutoCommit(prev, next, options.includes(next))) {
      setTimeout(() => commit(next), 0);
      return;
    }
    // ✕ (temizle) → "All environments", ANINDA. v0.9.1024'e dek bu
    // ayrı bir düğmeydi ve doğrudan commit('') çağırıyordu; Combobox
    // kendi ✕'ini çizdiği için sinyal artık "tek olayda boşa sıçrama"
    // biçiminde geliyor. AYNI sıçrama kuralı: tek tek geri silerek
    // boşaltmak (prev tek karakter) commit ETMEZ — yarım silinmiş bir
    // kutu global env'i düşürmemeli. O yol eskisi gibi blur'da
    // kapanıyor.
    if (isClearJump(prev, next)) setTimeout(() => commit(''), 0);
  };

  const inertTitle =
    'This page does not apply the environment filter — the data below covers ALL environments.\n' +
    (env ? `Your selection (env=${env}) stays active and applies on Traces, Services, Endpoints, Databases, Problems, Inbox and Logs.\n` : '') +
    'Clear it with ✕ if you want it gone everywhere.';

  return (
    // v0.9.980 — genişlik satır içindeydi; `@media` satır içi stili
    // YENEMEZ, dolayısıyla dar ekranda daraltmak imkânsızdı. Sınıfa
    // taşındı (globals.css `.env-pick-wrap`), değer aynı 150px.
    //
    // v0.9.1024 — native <datalist> → ev Combobox'ı. `serverFiltered`
    // burada iki kat önemli: liste YOĞUNLUK sırasında geliyor
    // (alfabetik değil) ve istemci süzgeci onu yeniden sıralanmış bir
    // alt kümeye çevirirdi. Kilitli ("env uygulanmıyor") hâlde ✕
    // çalışır kalıyor — atomun `disabled` sözleşmesi (v0.9.1022) tam
    // bu vaka için böyle yazıldı.
    <Combobox
      className="env-pick-wrap"
      value={draft}
      onChange={handleChange}
      options={options}
      serverFiltered
      disabled={!applies}
      placeholder={applies ? 'All environments' : 'env not applied'}
      ariaLabel="Environment filter"
      onEnter={() => commit(draft.trim())}
      // Blur = "alanı bıraktı": boşsa TÜM ortamlara dön, doluysa yarım
      // yazılmış metni at ve commit edilmiş env'e geri sar. Yarım bir
      // dize asla filtreye dönüşmemeli.
      onBlurCommit={v => { if (v.trim() === '') commit(''); else setDraft(env); }}
      footer={truncated
        ? `… +${total - fetched.length} more — type to search`
        : undefined}
      title={applies
        ? (truncated
            ? `Showing the ${fetched.length} busiest of ${total} environments — type to search all (last 24h).\n`
            : '') +
          'Filter by deployment environment (deployment.environment.name).\n' +
          'On Problems/Inbox the filter is service-scoped: rows whose service runs in the environment.\n' +
          'On Logs the Elasticsearch backend discovers its environment field automatically and shows a chip when none resolves.'
        : inertTitle
      }
    />
  );
}
