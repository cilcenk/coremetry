# Audit — Trace Kiosk Modu (chrome'suz şelale + log, yeni pencere)

**Tarih:** 2026-09-11 · **Taban:** v0.10.669 · **Durum:** ONAY BEKLİYOR — kod
değişikliği YOK, bu belge yalnız keşif + plan.
**İstek:** trace listesinden / trace detayından **yeni pencerede**, sidebar-
topbar-asistan olmadan, tam ekran trace şelalesi + log görünümü. Kimlik
doğrulama aynen (oturum çerezi + mevcut `AuthProvider`); ek erişim modeli
yok. Kapsam dışı bırakılan konulara bu belgede değinilmez.

Satır numaraları v0.10.669 ağacında elle doğrulandı (`file:line`); sembol
adları kaymaz, önce sembolü grep'le.

---

## 0. Yönetici özeti

| # | Soru | Kısa cevap |
|---|---|---|
| 1 | Rota / kabuk / auth kapısı | Rota `/trace?id=` (path param değil). Her sayfa `<Route element={<AppShell/>}>` içinde; kapı iki parça: `AuthProvider` effect'i `/login`'e atar, `AppShell` `!user` iken `null` çizer. Topbar kabukta DEĞİL, sayfanın kendi JSX'inde. |
| 2 | Bileşen imzaları | `TraceWaterfall` saf veri bileşeni (router/auth/api importu yok) → **olduğu gibi** kullanılır. `LogTable` chrome prop'ları isteğe bağlı, Trace sayfası zaten hiçbirini vermiyor → olduğu gibi. Chrome `Trace.tsx`'in kendisinde (Topbar, breadcrumb, eylem şeridi, AI, dış linkler). Refactor bileşenlerde değil **sayfa düzeyinde** (bkz. §2.4). |
| 3 | `?kiosk=1` mi ayrı path mi | **`?kiosk=1` zaten var** (v0.9.779, Dashboard TV modu): `AppShell` her rotada okuyor ama yalnız CSS ile sidebar+duyuru gizliyor. Aynı bayrağı `/trace` için **render dalına** yükseltmek en ucuz ve tutarlı yol; ayrı path custom-role eşlemesi + ikinci href üreticisi ister (§3.3). |
| 4 | SSE / canlı sağlayıcılar | Tek gerçek `EventSource` kabukta (`/api/events`, lider seçimi ile origin başına 1). Sidebar 5 s health + 30 s inbox poll. Bugünkü `?kiosk=1` (CSS) bunların **hepsini mount bırakır** → kiosk için kabukta çıplak `<Outlet/>` dalı şart (§4). |
| 5 | Login dönüşü | `?next=` YOK; hedef `sessionStorage`'a `pathname+search+hash` olarak yazılıyor (v0.8.367). Trace derin bağlantısı **hash değil `?span=`** — hash sorunu bu yüzeyde yok; olsa da istemci tarafında zaten çözülü. |
| 6 | 401'de satır-içi kart | 401 tek yerde ele alınıyor (`setUnauthorizedHandler`, tek kaydedici `AuthProvider`). `ErrorBoundary` render hatası içindir, 401 için uygun DEĞİL. Kart `AuthProvider` state'i + `AppShell` kiosk dalıyla, global davranış değişmeden yapılabilir; **iki** yönlendirme noktası var, ikisi de kapılanmalı (§6). |
| 7 | Endpoint sayısı / bundle | Bugün 3 uç, 2 seviyeli seri waterfall (span → [logs ∥ oracle]); `?tab=logs` derin bağlantısında log isteği **iki kez**, ilki zaman-sınırsız. Bundle: 1 istek, 1 RTT, penceresiz istek yok; sunucu içinde span→log bağımlılığı kalır. Kazanç ölçülebilir ve gerçek, mutlak ms olarak mütevazı (§7.4). |
| 8 | Kayıt kalıbı | `internal/api/trace_bundle.go` + `init(){ registerRoutesExtra("trace-bundle", …) }`; birebir emsal `trace_routes.go` (aynı aile) + `oracle_logs_routes.go` (saf anahtar). `api.go` sıfır satır (§8). |
| 9 | Log limiti / daha fazla | FE sabit 500, cursor/"daha fazla" YOK; sunucu tavanı ES/CH/Oracle üçünde 1000. Öneri: bundle `logLimit` varsayılan 500, tavan 1000; "daha fazla" = limit'i 1000'e çıkarıp yeniden çek; ötesi için `/logs?traceId=` bağlantısı (§9). |

---

## 1. Rota, kabuk hiyerarşisi, auth kapısı

**Sonuç:** Kiosk için yeni rota gerekmez; kabuk kararı tek dosyada
(`AppShell.tsx`), auth kararı tek dosyada (`AuthProvider.tsx`) alınıyor.

Hiyerarşi (BrowserRouter → sayfa):

```
main.tsx:165-170   StrictMode > QueryClientProvider > BrowserRouter > <App/>
App.tsx:135-146    ErrorBoundary > ConfirmProvider > AuthProvider > Suspense > Routes
App.tsx:146          <Route element={<AppShell/>}>          ← TÜM sayfalar bu layout'ta
App.tsx:157            <Route path="/trace" element={<Trace/>}/>   (lazy)
pages/Trace.tsx:51     const id = searchParams.get('id') ?? '';   ← path param DEĞİL
```

- `AppShell.tsx:78` `isPublic = isPublicPath(pathname)`; `:135-138` public
  dal → yalnız `<ErrorBoundary><Outlet/></ErrorBoundary>`; `:149-152` `!user`
  → `return null` (AuthProvider yönlendirme ortasında); `:153-196` kimlikli dal
  → `Sidebar + #main(AnnouncementBanner + Outlet) + ShortcutsHelp +
  CommandPalette + GlobalShortcuts + CopilotChat + Toaster`.
- **Topbar kabukta değil:** `Trace.tsx:475` `<Topbar title="Trace Detail" …/>`
  (id yokken `:388`). Kabuk çıplak `<Outlet/>` çizse bile Topbar sayfa
  tarafında kalır → kiosk'ta sayfa da Topbar'ı atlamalı.
- Auth kapısı (sırayla):
  1. `AuthProvider.tsx:44-51` mount'ta `api.me()`.
  2. `AuthProvider.tsx:54-61` `!user && !isPublicPath(path)` →
     `savePostLoginRedirect(pathname+search+hash)` + `navigate('/login')`.
  3. `AuthProvider.tsx:30-41` `setUnauthorizedHandler` — herhangi bir API
     çağrısı 401 verirse `setUser(null)` + aynı kayıt + `/login`.
  4. `AppShell.tsx:149-152` `!user` iken hiçbir şey çizmez.
- Custom-role kapısı: `AppShell.tsx:34`
  `if (p === '/traces' && pathname.startsWith('/trace')) return true;` —
  `/trace` bu kuralla izinli; **ayrı bir path bu kuralın dışında kalır** (§3.3).

---

## 2. Şelale ve log paneli — prop imzaları, chrome vs veri

### 2.1 `TraceWaterfall` — `components/TraceWaterfall.tsx:175-235` (957 satır)

| Tür | Prop |
|---|---|
| Veri | `spans: SpanRow[]`, `selectedId: string\|null`, `defaultCollapsed?`, `groupSimilar?`, `criticalPathIds?: Set`, `evidenceIds?: Set`, `linkedSpanIds?: ReadonlySet`, `analysis?: TraceAnalysis`, `revealSpanId?`, `matchIds?: Set`, `logSignals?: Map`, `focusIds?: Set` |
| Geri çağrı | `onSelect: (id)=>void` (zorunlu), `onGroupSimilarChange?` (yoksa "×N grupla" toggle'ı çizilmez), `onLogsClick?` (yoksa log çipi tıklanmaz) |

- Import listesi (`:1-9`): react-virtual, scrollParent, TraceMinimap, semconv,
  utils. **react-router / auth / react-query / api yok.** Kaydırma kabı
  `findScrollParent` ile bulunur, bulunamazsa pencere kipine düşer → `#main`
  olmadan da çalışır.
- **Sonuç: refactor yok.** Kiosk `onGroupSimilarChange`/`onLogsClick` vermeden
  çağırır → salt-okunur görünüm kendiliğinden.

### 2.2 `LogTable` — `components/LogTable.tsx:175-262` (651 satır)

- Veri: `logs: LogRow[]`, `hideTraceColumn?`, `wrap?`, `columns?`,
  `highlightTerms?`, `expandedIds?`, `extraExpanded?`.
- Chrome (hepsi isteğe bağlı): `onRemoveColumn?`, `nav?`, `onToggleExpand?`,
  `onFilterAdd?`, `onFilterExclude?`, `onToggleColumn?`, `onTracePeek?`,
  `onContextOpen?`, `permalink?`.
- Trace sayfası bugün `<LogTable logs={…} hideTraceColumn />` (`Trace.tsx:936`,
  `:989`) — chrome prop'larının hiçbiri verilmiyor → zaten salt-okunur.
- Tek bağ: `LogTable.tsx:4` `import { Link } from 'react-router-dom'` → Router
  bağlamı ister. Kiosk `BrowserRouter` içinde kaldığı için sorun değil.
- Logs sekmesinin sarmalayıcısı `TraceLogsPanel` **dosya-yerel**
  (`Trace.tsx:885-900`): `logs, degraded?, logsTotal?, eventRows, oracleRows,
  oracleError?, hiddenGrpcMsgs, showGrpcMsgs, onToggleGrpcMsgs, traceServices`.
  Kiosk ayrı bir sayfa bileşeni olacaksa bu panel `pages/trace/` altına
  taşınıp export edilmeli (~120 satır taşıma, davranış aynı).

### 2.3 `SpanDetail` — `components/SpanDetail.tsx:39` (749 satır)

`span, links?, onSelectSpan?, onClose, logsFrom?, logsTo?, serviceLinks=true,
traceSpans?, pageRange?`. Chrome ağır: `Link` (`:3`) ile `/endpoint`, `/trace`,
`/logs`, `/profile`, entity sayfalarına bağlantılar; içinde `AIExplainButton`;
seçilince `profilesForSpan`, `spanHotspots`, `api.logs(limit 50)`,
`serviceOperations`, `/api/entities/clusters`, `useStackFrameLinks` istekleri.
`serviceLinks={false}` prop'u var ama diğer linkleri kapatmaz. **Kiosk v1'de
span paneli dahil edilecekse "linksiz" bir varyant gerekir** → §11 soru 2.

### 2.4 Chrome `Trace.tsx`'in kendisinde (1607 satır)

Kiosk'ta atlanacak bloklar: `:475` Topbar · `:482-486` breadcrumb `Link` ·
`:431-470` eylem şeridi (Compare `Link`, `DrillButton` → `/logs`,
`SharePopover` [`useConfirm`+`useAuth`], Export JSON) · `:578`
`<AIExplainButton subject={{kind:'trace',id}}>` (içeriği kabuktaki
`CopilotChat` çizer — kabuk yokken **işlevsiz kalır**, gizlenmeli) · `:589`
`<ExternalLinkButtons>` (`/api/external-links` + `/api/traces/{id}/link-identity`).

Kabuk-bağımsız kalanlar (kiosk'ta sorunsuz): `useQuery`'ler (QueryClient
`main.tsx`'te, kabuk üstü) · `useShortcuts` (`:265`, document dinleyicisi) ·
`useUrlRange` (`?range=` + sessionStorage) · `useAiEvidence/useAiFocus`
window CustomEvent köprüsü (dinleyici yoksa zararsız) · `toast` yok.

**İki yol:**
- **A — `Trace.tsx` içinde `kiosk` bayrağı:** chrome bloklarını `{!kiosk && …}`
  ile sar. +~40 satır, tek dosya; ama 1607 satırlık sayfaya yeni bir eksen
  girer ve her chrome eklemesi bayrağı hatırlamak zorunda.
- **B — ayrı `pages/TraceKiosk.tsx`:** aynı rota (`/trace`), `Trace.tsx`'in
  varsayılan export'u `kiosk ? <TraceKiosk/> : <TraceDetailInner/>` diye
  dallanır (~5 satır). `TraceWaterfall` + taşınmış `TraceLogsPanel` + bundle
  hook'u ile ~200 satır yeni dosya; `Trace.tsx` davranışı **değişmez**.
  **Öneri B** — regresyon yüzeyi sıfıra yakın, kiosk'un kendi düzeni (şelale
  üstte, loglar altta, sekmesiz) serbest.

---

## 3. `?kiosk=1` vs ayrı path

### 3.1 Mevcut bayrak (v0.9.779)

```ts
// AppShell.tsx:111-117
const kiosk = new URLSearchParams(search).get('kiosk') === '1';
useEffect(() => { const el = document.documentElement;
  if (kiosk) el.setAttribute('data-kiosk', '1'); else el.removeAttribute('data-kiosk');
  return () => el.removeAttribute('data-kiosk'); }, [kiosk]);
```
```css
/* globals.css:3546-3547 */
[data-kiosk="1"] #sidebar { display: none; }
[data-kiosk="1"] .announcement-banner { display: none; }
```

- `AppShell.tsx:99-110` yorumu: bilinçli olarak **yalnız görsel**; kiosk
  `PUBLIC_PATHS`'e eklenmemiş, kimlikli kalır — istekle birebir uyumlu.
- Tüketici yalnız `Dashboard.tsx:85-96` (toggle), `:130` (Esc çıkışı);
  `dashboardUrl.ts:24` whitelist. Başka `mode`/`embed`/`print` bayrağı yok.
- **Bugün `/trace?id=…&kiosk=1` açılırsa:** sidebar gizli, **Topbar var**
  (sayfa çiziyor), `CopilotChat`/`Toaster`/`GlobalShortcuts` **mount**,
  `useEventStream` **açık**, Sidebar `display:none` ama hook'ları (health 5 s,
  inbox 30 s) **çalışıyor**. Yani CSS katmanı istek için yetersiz.

### 3.2 Trace sayfasının param disiplini bayrağı korur

- URL yazıcısı `Trace.tsx:208-217` ham `history.replaceState` ile **yalnız**
  `span/tab/xn`'e dokunur, `new URL(window.location.href)` üst küme →
  `kiosk=1` span seçiminde/sekme değişiminde **silinmez**.
- `useUrlRange` de `window.location.search`'ten tohumlar → korunur.
- Derin bağlantı üreticisi tek: `lib/traceHref.ts:117`
  `traceHref(id, opts: TraceHrefOpts)`; tüketiciler `Traces.tsx:790/946/1458`,
  `LogTable.tsx:525`. `opts.kiosk?: boolean` eklemek 6 satır + test.

### 3.3 Ayrı path (`/kiosk/trace/:id`) neden daha pahalı

1. `AppShell.tsx:34` custom-role eşlemesi yalnız `/trace*`'i kapsar; yeni path
   custom-role kullanıcısını `:131-132` ile ilk izinli sayfaya ışınlar → ikinci
   kural gerekir.
2. `<Route element={<AppShell/>}>` dışına konursa `:140-152` loading/`!user`
   kapısı kaybolur (sayfa `user=null` iken bir kare çizer).
3. `traceHref.ts` tek üretici + "DEAD PARAM" kapısı; ikinci üretici + ikinci
   test.
4. Kromu **karar veren** yer zaten `AppShell.tsx:135-139` (`isPublic` → çıplak
   `<Outlet/>`); kimlikli-ama-kromsuz üçüncü dal aynı satırlara eklenir. Ayrı
   path bu kararı değiştirmez, sadece dolaylar.

**Karar önerisi:** `/trace?id=…&kiosk=1`. Kabukta üçüncü dal: `user && kiosk &&
pathname === '/trace'` → `<ErrorBoundary><Outlet/></ErrorBoundary>` (Sidebar,
CopilotChat, Toaster, GlobalShortcuts yok) ve `useEventStream(!!user &&
!isPublic && !kioskBare)`. Dalı `/trace` ile sınırlamak Dashboard TV kiosk'unun
bugünkü davranışını (SSE + asistan mount) **değiştirmez** → §11 soru 1.

---

## 4. Uygulama geneli SSE / canlı-akış / polling envanteri

| Sağlayıcı | Nerede | Ne yapar | Kabuksuz dalda |
|---|---|---|---|
| `useEventStream` | `AppShell.tsx:90` `useEventStream(!!user && !isPublic)`; `lib/queries/eventStream.ts:115/:143` `new EventSource('/api/events')` | `problem.*`, `anomaly.*`, `rollout` → RQ invalidate; Web Locks lider + BroadcastChannel; primitif yoksa sekme başına EventSource | **mount olmaz** (bayrak false) |
| `useHealth` | `Sidebar.tsx:198`; `lib/queries/health.ts:14` `refetchInterval: 5_000` | `/api/health` | mount olmaz (Sidebar yok) |
| `useInboxCount` | `Sidebar.tsx:209`; `lib/queries/inbox.ts:117` `refetchInterval: 30_000` | inbox sayaçları | mount olmaz |
| `AnnouncementBanner` | `AppShell.tsx:159`; tek fetch, `staleTime 5m`, poll yok | duyuru | mount olmaz |
| `CopilotChat` | `AppShell.tsx:189`; mount'ta istek atmaz, sohbet SSE'si yalnız gönderimde (`api.ts` POST fetch-stream) | asistan | mount olmaz (→ `AIExplainButton` gizlenmeli, §2.4) |
| `useBranding` | `AppShell.tsx:83`; tek fetch, modül cache | marka | Login'de de çağrılıyor; kabuksuz dalda da tek fetch kalabilir |
| CommandPalette / GlobalShortcuts / ShortcutsHelp / Toaster | `AppShell.tsx:174-195`; mount'ta fetch yok | — | mount olmaz |
| `AuthProvider` `api.me()` | `AuthProvider.tsx:44-51` | oturum doğrulama (tek istek) | her durumda |
| RUM boot | `main.tsx:85-108` `api.health()` bir kez + tarayıcı OTel `/v1/traces` | öz-telemetri | her durumda (kısa istekler; stream değil) |
| Topbar çocukları | `Trace.tsx:475` → `EnvPicker` `api.environments` | env listesi | kiosk sayfası Topbar çizmediği sürece yok |
| Logs canlı-tail | `pages/Logs.tsx:602` `EventSource('/api/logs/stream')` | yalnız `/logs` | ilgisiz |

Trace sayfasında `refetchInterval` yok (sayfa düzeyinde poll yok).

**Olay kaydı (HTTP/1.1 havuz tükenmesi / ~6 s stall):** `docs/INCIDENTS.md`'de
girdi YOK (grep negatif). Kaynaklar: `lib/queries/eventStream.ts:5-24` başlığı
(v0.8.529 — "browser caps HTTP/1.1 connections at ~6 PER HOST … at ~6 windows
every slot was consumed by idle-but-open streams") ve
`docs/audit/sse-http2-multitab-audit.md:1-8` (2026-07-17, durum "ONAY
BEKLİYOR"; `:25-33` router wildcard-cert'te HAProxy h2 pazarlamaz — prod
route'u bugün bu durumda olabilir, teyit edilmedi).

**Kiosk etkisi:** kabuklu açılsaydı yeni pencere Web Locks **takipçisi** olur
(ek SSE yok) — yani v0.8.529 sonrası "her pencere bir stream" problemi zaten
yok; ama Sidebar poll'ları + Topbar istekleri + CopilotChat yüklenirdi.
Kabuksuz dalda **sıfır stream, sıfır poll**; pencere origin'in 6'lık h1
kotasını yalnız ilk yükleme istekleriyle kullanır → istek sayısı önemli (§7.4).

---

## 5. Login yönlendirme akışı — `?next=` ve hash

**Sonuç:** `?next=`/`returnTo` param'ı YOK; hedef URL istemci tarafında
`sessionStorage`'a yazılıp login sonrası `navigate()` ile geri okunuyor
(v0.8.367). Hash dahil korunuyor; trace sayfası zaten hash kullanmıyor.

- Kayıt (İKİ nokta, ikisi de `pathname + search + hash`):
  `AuthProvider.tsx:30-41` (401 handler, `window.location.*`) ve `:54-61`
  (rota kapısı, router `location`).
- Geri dönüş `AuthProvider.tsx:66-71`: `user && path === '/login'` →
  `navigate(consumePostLoginRedirect() ?? '/')`; `user && path === '/'` →
  varsa hedef (OIDC dönüşü buraya düşer: sunucu callback'i `/`'a yönlendirir).
- Depo `lib/postLoginRedirect.ts:13` `KEY='coremetry-post-login-redirect'`;
  `:20-26` `sanitizeRedirect` yalnız `/` ile başlayan, `//` olmayan,
  `/login*` ve `/public/*` olmayan yolları kabul eder →
  `/trace?id=…&kiosk=1&span=…&tab=logs` **geçer**.
- **Hash:** `Trace.tsx`'te `location.hash` / `#span` kullanımı YOK (grep
  negatif). Span derin bağlantısı `?span=` (`Trace.tsx:72`, `:355-357`;
  üretici `traceHref.ts`, `LogTable.tsx:525`). İstekteki "hash sunucuya gitmez"
  endişesi bu yüzeyde **konu dışı**; bir gün hash kullanılsa bile kayıt zaten
  istemci tarafında (`window.location.hash` dahil) yapılıyor, sunucuya
  taşınması gerekmiyor.
- `sessionStorage` sekme kapsamlıdır ve OIDC tam-sayfa turunu atlatır
  (`postLoginRedirect.ts:8-11`). Kayıt **yeni pencerenin kendi**
  `AuthProvider`'ında yapılır → `window.open(…, 'noopener')` ile açılsa da
  (opener'ın sessionStorage'ı kopyalanmaz) akış bozulmaz.
- Login sayfası kendisi yönlendirme yapmaz (`Login.tsx:56-67` `login()` →
  AuthProvider effect'i); yani kiosk için Login'e dokunulmaz.

**Kiosk için ek iş: yok.** Yalnız `postLoginRedirect.test.ts`'e kiosk URL'sinin
süzgeçten geçtiğini pinleyen bir vaka (3 satır).

---

## 6. 401'de satır-içi "oturum sonlandı" kartı

**Sonuç:** 401 işleme merkezî ve tek kaydedicili; kart, `api.ts`'e dokunmadan
`AuthProvider` + `AppShell` kiosk dalında yapılabilir. `ErrorBoundary` uygun
değil.

- `lib/api.ts:66-73` `UnauthorizedError` + `setUnauthorizedHandler`;
  `request()` `:344` `if (r.status === 401) { onUnauthorized?.(); throw … }`;
  aynı desen `explainStream` `:148`, `insightStream` `:255`. `EventSource`
  401'i ayırt etmez (kiosk'ta mount değil, ilgisiz).
- Tek kaydedici `AuthProvider.tsx:30-41`; başka `setUnauthorizedHandler` /
  `instanceof UnauthorizedError` yok (grep).
- `components/ErrorBoundary.tsx` render-hata sınırı (bayat chunk → reload;
  Reload / Home / Report düğmeleri). 401 promise içinde fırlar, sayfalar
  `.catch` ile yutar (`Trace.tsx:196-197` → `Empty "Failed to load trace"`).
  ErrorBoundary'ye hiç ulaşmaz → **kart buradan çıkamaz.**
- **Bugün:** 401 → handler `setUser(null)` + `/login`; `AppShell:149-152`
  `null` çizer → sayfa kaybolur.

**Tasarım (global davranış değişmeden):**
1. `AuthProvider` yeni state `sessionEnded: boolean` (+ context'e).
2. Handler (`:30-41`): `kioskBare && hadUser` ise `setUser(null)` yerine
   `setSessionEnded(true)` — kullanıcı nesnesini düşürmeden (aksi hâlde `:54-61`
   rota kapısı da `/login`'e atar — **ikinci yarı**; iki noktada da kapı).
   `hadUser=false` (pencere hiç kimliksiz açıldı) → normal akış (`/login` +
   kayıtlı dönüş) — ilk açılış UX'i bozulmaz.
3. `AppShell` kiosk dalı: `sessionEnded` → sayfanın üstüne satır-içi kart
   ("Oturum sonlandı — yeniden giriş yap" → `navigate('/login')`, dönüş zaten
   `savePostLoginRedirect` ile) — donmuş şelale altta kalır mı, yerini mi alır
   → §11 soru 7.
4. Diğer sayfalar: `kioskBare=false` → handler bugünkü satırları koşar.

Yeni bileşen `components/SessionEndedCard.tsx` (~40 satır, `<Empty>` +
`<Button>` atomları; hand-roll yok).

---

## 7. Backend — bugünkü uçlar ve bundle değerlendirmesi

### 7.1 Üç uç

| Uç | Kayıt | Cache | Sınırlar | Yanıt |
|---|---|---|---|---|
| `GET /api/traces/{id}` | `trace_routes.go:18-22` (defter, `"trace"`) | `"trace:v3:"+id`, 30 s (`:35-36`) | Tempo-önce 3 s bütçe (`trace_resolve.go:62`); CH `GetTrace` `repo.go:4602`: `trace_summary_5m` pencere (24h/7d/90d, `max_execution_time=3`), çözülemezse `traceScanFloor=31d` (`:4548`); ana sorgu `:4676-4686` `FROM spans WHERE … ORDER BY time ASC LIMIT 50000 SETTINGS max_execution_time = 20` | `{traceId, spans, source, spanCapped?, spanTotal?, analysis, stub?}` (`:49-88`); FE `TraceDetailResponse` |
| `POST /api/logs/search` (trace dalı) | `logs_routes.go` (defter, `"logs"`) | `logsSearchKey` hash-all-inputs (`api_logs.go:279-290`), 15 s (`:422-424`) | `Filter{TraceID, SpanID, Limit(vars. 100), Offset, Cursor, WantCursor: paging=1\|\|after≠""}` (`:377-402`); `logstore.SearchWithTimeout(ctx, s.logs, f, 0)` (`:435`) → `PivotTimeout = 3 s` (`logstore.go:597`); `ErrBackendSlow` → **HTTP 200 `{degraded:true}`** 15 s cache'li (`:428-441`) | `{total, logs, nextCursor?, degraded?, reason?, …}` |
| `GET /api/oracle/errors?trace_id&from&to&limit` | `oracle_logs_routes.go:38-42` (defter, `"oracle-logs"`) | `oracleLogsKey` (`:72-74`, `cacheBucket` 30 s grid), 30 s | `from/to` **zorunlu** (400), limit vars. 200 / tavan 1000 (`:65-68`); `oracle_error_log FINAL WHERE trace_id=? AND time>=? AND time<? LIMIT n SETTINGS max_execution_time=5` | `{enabled, logs, total=len(out)}` |

ES arka ucu (`internal/logstore/elasticsearch.go`): limit `:1307-1310`
(`<=0 || >1000 → 100`); **trace/span kapsamlı arama pencereyi kırpmaz**
(`:1316-1323` — "a trace link can be older than any default slice"); `size:
limit`, `track_total_hits` tavanlı, ilk sayfada `terminate_after`. CH arka ucu
(`repo.go:5029` `GetLogs`): vars. 100, tavan `logsMaxLimit=1000`; zaman şartı
**yalnız From/To doluysa**; liste `LIMIT ? OFFSET ? … max_execution_time=25` +
ayrı sayaç (`LogsCountCap=100000`).

Auth: üçünde de rol kapısı yok; küresel `s.auth.Middleware` kimliksizi 401'ler.
Ölçüm: yalnız span ucu için var — `docs/perf/perf-budget-2026-08-28.md:54`
`GET /api/traces/{id}` **0.32 s** medyan (0.25–1.15), TTFB 15–95 ms, 8–13 KB
(lokal). Log ve Oracle uçları için ölçülmüş gecikme **yok**.

### 7.2 İstemci waterfall'u bugün

```
t0  api.trace(id)                     Trace.tsx:184-192   (düz useEffect, RQ değil)
t0  useQuery(['trace-links', id])     Trace.tsx:76-81     ∥ (paralel)
t1  spans geldi → logWin = traceLogWindow(spans)   Trace.tsx:109; hooks.ts:45 (±60 s)
t1  useCorrelatedLogs(id, {limit:500, from, to})   Trace.tsx:141-143  (yalnız tab==='logs')
t1  useOracleTraceLogs(id, {from, to})             Trace.tsx:147      ∥ (from&&to şart, hooks.ts:117)
```

- **`?tab=logs` derin bağlantısı (kiosk'un varsayılanı olacak):**
  `useCorrelatedLogs` `enabled: !!traceId` (`hooks.ts:97`), pencere query
  key'de → t0'da **penceresiz** ilk istek (ES kırpmaz, CH zaman şartı eklemez
  = trace_id üzerinden tüm-retention tarama), t1'de pencereli ikinci istek.
  Koddan çıkarım; canlıda gözlenmedi (§11).
- Yeni pencere = ayrı SPA örneği → RQ cache paylaşımı yok, her açılış soğuk.
- Emsal: `TracePeekDrawer.tsx:64-67` `api.trace` + `api.logs({traceId,
  limit:500})` aynı anda ("Fire both in parallel") — ama penceresiz.

### 7.3 Bundle ucu — `GET /api/traces/{id}/bundle?logLimit=&oracleLimit=`

Sunucu içinde:
```
resolveTraceSpans(ctx, id)            (Tempo-önce / CH; mevcut fonksiyon, dokunulmaz)
  └─ window = spanWindow(spans, ±60 s) (saf Go; hooks.ts:45'in aynası, tablo testi)
       ├─ logstore.SearchWithTimeout(ctx, s.logs, Filter{TraceID, From, To, Limit}, 0)   ─┐ WaitGroup
       └─ s.store.OracleErrorsByTrace(ctx, id, from, to, oracleLimit)                    ─┘ bağımsız hata yuvaları
→ {traceId, source, spans, analysis, spanCapped?, spanTotal?, stub?,
   logs:{items, total, degraded?, reason?}, oracle:{enabled, items},
   truncated:{spans:bool, logs:bool, oracle:bool}}
```

- **errgroup DEĞİL** — `logs_context_halves.go:16-25`: `WithContext` ilk hatada
  kardeşi iptal eder, `MapBackendSlow` iptal edilmiş bağlamda `ErrBackendSlow`
  üretmez → yavaş backend 200 `{degraded}` yerine 5xx olurdu (v0.8.532 dersi;
  `api.go:1977-1986` getServices geri alımı). Desen: `sync.WaitGroup` + yarı
  başına hata (`logs_context_halves.go:26-50`) — log bacağı degraded ise bundle
  yine 200, `logs.degraded=true`.
- Havuz baskısı: CH bacakları spans + oracle = 2 bağlantı (CH log backend'inde
  +2: liste + sayaç; `SkipTotal` düşünülebilir). ES bacağı HTTP. Tıkla-
  tetiklenen, cache'li uç — poll değil.
- Cache iki seçenek: (a) tek anahtar `trace-bundle:v1:<id>:ll=<n>:ol=<n>`,
  TTL 15 s (en kısa bacak); (b) `dashboards_data.go:218-249` emsali — slot
  başına `s.cachedJSON(ctx, key, ttl, …)` ile mevcut `trace:v3:<id>` (30 s),
  `logsSearchKey` (15 s), `oracleLogsKey` (30 s) anahtarları **paylaşılır**:
  kiosk'un çektiği trace normal `/trace` sayfasını da ısıtır, TTL'ler dürüst
  kalır. (b) `getTrace` gövdesinin `traceDetailPayload(ctx, id)` olarak
  ayrılmasını ister (~30 satır taşıma). **Öneri (b)** → §11 soru 5.
- İmza tutarlılığı: repoda `/api/trace/` (tekil) öneki YOK; aile
  `/api/traces/{id}`, `/{id}/links`, `/{id}/link-identity`, `/{id}/shares`.
  `{id}` sonrası literal segment Go 1.22 mux'ta çakışmaz; `TestMuxRoutePatterns`
  doğrular. Öneri `/api/traces/{id}/bundle` → §11 soru 3.

### 7.4 Ölçülebilir gerekçe — dürüst tablo

| Kalem | Bugün (`?tab=logs`) | Bundle | Ölçüm yöntemi |
|---|---|---|---|
| Ağ turu (seri seviye) | 2 (span → [logs ∥ oracle]) | 1 | DevTools Network: trace yanıtı → ilk log isteği başlangıcı arası (istemci bağımlılık gecikmesi = JSON parse + React commit + effect) |
| İstek sayısı (ilk boyama yolu) | ≥4 (trace, links, logs×2, oracle) | 1 (+links lazy) | Network paneli sayımı; h1 6-bağlantı kotasında kuyruk "Stalled" segmenti |
| Zaman-sınırsız log sorgusu | 1 (penceresiz ilk istek) | 0 (pencere sunucuda span'lerden) | ES slow-log / CH `system.query_log` `log_comment=route:…` |
| Sunucu içi bağımlılık | — | span → log/oracle **kalır** (pencere) | `X-Cache` + sunucu süresi |
| Cache paylaşımı | trace 30 s / logs 15 s / oracle 30 s ayrı | slot-başı ile aynı anahtarlar | `X-Cache: HIT/MISS` |

- Beklenti: h2 + LAN'da kazanç bir RTT + istemci bağımlılık gecikmesi (onlarca
  ms); **h1 + çoklu sekme** (kiosk'un asıl senaryosu) ve `?tab=logs` çift
  isteği kalktığı için kazanç orantısız büyür. Log ucu ölçümü olmadığı için
  mutlak sayı **vaat edilmez**; kabul kriteri: aynı trace, 5 tekrar, medyan
  "pencere açıldı → loglar boyandı" süresi ve istek sayısı, önce/sonra
  (`feedback-perf-benchmark-discipline`).
- İkinci faz (isteğe bağlı): `GetTrace` pencere ön-sorgusunu (`trace_summary_5m`)
  dışa açıp span taraması ile log bacağını **eşzamanlı** başlatmak — chstore
  seam'i yok, Tempo-önce yolunda pencere yanıttan çıkar; bu belge kapsamı
  dışında, ölçüm sonrası karar.

---

## 8. Kayıt kalıbı — `api.go` sıfır satır

Defter: `internal/api/route_registry.go:29-38` `registerRoutesExtra(name, fn)`
(boş ad / çift kayıt → panic); `buildMux` defteri ad sırasıyla boşaltır;
`mux_routes_test.go:17-28` `TestMuxRoutePatterns` `s.buildMux()` kurar,
çakışma → panic → fail.

**Aynı aileden birebir emsal** — `internal/api/trace_routes.go:1-36`:

```go
package api

// trace_routes.go — v0.10.275 (trace view Dilim 1b): GET /api/traces/{id}
// api.go'dan buraya TAŞINDI (route_registry.go defteri, init kaydı; api.go
// kısaldı). …

func init() { registerRoutesExtra("trace", (*Server).registerTraceRoutes) }

func (s *Server) registerTraceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/traces/{id}", s.getTrace)
}

func (s *Server) getTrace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	…
	key := "trace:v3:" + id
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		spans, src, err := s.resolveTraceSpans(ctx, id)
		…
	})
}
```

**Saf anahtar emsali** — `oracle_logs_routes.go:72-74`:
```go
func oracleLogsKey(traceID string, from, to time.Time, limit int) string {
	return fmt.Sprintf("oracle-logs:t=%s:lim=%d:w=%s", traceID, limit, cacheBucket(from, to))
}
```

Yeni dosya iskeleti (yazılmadı, plan):
```go
// trace_bundle.go — v0.10.X (trace kiosk): GET /api/traces/{id}/bundle
// api.go BÜYÜMEZ — route_registry defteri. Rol kapısı YOK (salt-okunur;
// küresel middleware 401'ler, viewer görür). Üç bacak: span (mevcut
// resolveTraceSpans), log (SearchWithTimeout, degraded → 200), oracle.
// errgroup DEĞİL (v0.8.532): WaitGroup + bağımsız hata yuvaları.
func init() { registerRoutesExtra("trace-bundle", (*Server).registerTraceBundleRoutes) }
func (s *Server) registerTraceBundleRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/traces/{id}/bundle", s.getTraceBundle)
}
func traceBundleKey(id string, logLimit, oracleLimit int) string { … } // SAF, cache_key_test.go emsali
```

Gate'ler: `go test ./internal/api/ -run TestMuxRoutePatterns`, ad-pin testi
(`logs_routes_test.go:9-20` emsali: kalıp ADLA pinli — düşerse istemci 404
değil "boş 200" görür), anahtar testi (distinctness / stability / permutation),
`make audit` CHECK 7.

---

## 9. Log limiti ve "daha fazla" davranışı

| Katman | Varsayılan | Tavan | Sayfalama | Kesilme sinyali |
|---|---|---|---|---|
| ES (`elasticsearch.go:1307-1310`) | 100 | 1000 | PIT + `search_after`, yalnız `WantCursor` (`paging=1`/`after`) | `total` (`track_total_hits` 10 000 tavanlı) |
| CH (`repo.go:5029`) | 100 | `logsMaxLimit=1000` | `LIMIT/OFFSET` | `total` + `TotalIsLowerBound` (`LogsCountCap=100000`) |
| Oracle (`oracle_logs_routes.go:65-68`) | 200 | 1000 | yok | **yok** (`total=len(out)`) |
| FE Trace Logs sekmesi (`Trace.tsx:141-143`) | **500 sabit** | — | cursor/"daha fazla"/infinite scroll **YOK** | etiket `Trace.tsx:980-981` `ilk ${logs.length} / ${logsTotal} log satırı` |

**Bundle önerisi:** `logLimit` varsayılan **500**, tavan 1000 (üç arka ucun
ortak tavanı; `clamp` anahtarın önünde, `oracle_logs_routes.go:65-68` gibi);
`oracleLimit` varsayılan 200. `truncated` = `{spans: spanCapped, logs: total >
len(items) || totalIsLowerBound, oracle: len(items) == oracleLimit}` (Oracle
ucu bugün sinyal taşımadığı için sezgisel; dürüst not olarak yanıtta işaretli).

"Daha fazla": v1'de **limit yükseltme** (500 → 1000, aynı uç, anahtar limit'i
taşır); 1000'de hâlâ `truncated.logs` ise "Tümünü Logs'ta aç" bağlantısı
(`/logs?traceId=…`, yeni pencere, kabuklu). ES cursor sayfalaması bundle'a
sokulmaz: PIT tutma maliyeti ve `WantCursor` anahtarı zaten `/api/logs/search`'te
var — gerekirse kiosk ikinci sayfayı o uçtan çeker (§11 soru 8).

---

## 10. Dosya listesi, satır etkisi, commit sırası

| # | Dosya | Durum | Tahmini etki |
|---|---|---|---|
| B1 | `internal/api/trace_bundle.go` | YENİ | ~170 (init+register, saf `traceBundleKey`, saf `spanWindow`, handler, WaitGroup fan-out, `truncated`) |
| B2 | `internal/api/trace_bundle_test.go` | YENİ | ~90 (anahtar distinct/stable; `spanWindow` tablo testi; rota ad-pin) |
| B3 | `internal/api/trace_routes.go` | değişir | +25/−20 (`getTrace` gövdesi → `traceDetailPayload(ctx, id)`; davranış aynı; slot-başı cache için) |
| F1 | `frontend/src/lib/types.ts` | değişir | +30 (`TraceBundleResponse`, `TraceBundleTruncated`) |
| F2 | `frontend/src/lib/api.ts` | değişir | +4 (`traceBundle(id, {logLimit, oracleLimit}, signal)`) |
| F3 | `frontend/src/lib/otel/hooks.ts` (veya `lib/queries/trace.ts`) | değişir | +25 (`useTraceBundle`, `staleTime` = sunucu TTL; `cancellation.test.ts` listesine kayıt) |
| F4 | `frontend/src/lib/traceHref.ts` + `traceHref.test.ts` | değişir | +8 / +12 (`kiosk?: boolean`) |
| F5 | `frontend/src/components/AppShell.tsx` | değişir | +25 (kiosk-çıplak dal; `useEventStream` bayrağı; `SessionEndedCard`) |
| F6 | `frontend/src/components/AuthProvider.tsx` | değişir | +20 (`sessionEnded` state, iki yönlendirme noktasında kiosk kapısı) |
| F7 | `frontend/src/components/SessionEndedCard.tsx` | YENİ | ~40 |
| F8 | `frontend/src/pages/trace/TraceLogsPanel.tsx` | TAŞIMA | ~120 (Trace.tsx:885-1000 arasından, davranış aynı) |
| F9 | `frontend/src/pages/TraceKiosk.tsx` | YENİ | ~200 (bundle hook, `TraceWaterfall` salt-okunur, `TraceLogsPanel`, `truncated` etiketleri, "daha fazla") |
| F10 | `frontend/src/pages/Trace.tsx` | değişir | +8/−115 (varsayılan export'ta `kiosk` dallanması; `TraceLogsPanel` import) |
| F11 | `frontend/src/pages/Traces.tsx` | değişir | +15 (satır eylemi "⧉ yeni pencerede aç" → `window.open(traceHref(id,{kiosk:true, pageRange}), '_blank', 'noopener')`) |
| F12 | `frontend/src/styles/globals.css` | değişir | +20 (`[data-kiosk] .trace-kiosk` tam yükseklik, dolgu yok; tema token'ları) |
| F13 | `frontend/src/lib/postLoginRedirect.test.ts` | değişir | +4 (kiosk URL süzgeçten geçer) |
| F14 | `frontend/src/components/appShellKiosk.test.ts` | YENİ | ~40 (kaynak pini: kiosk dalında `Sidebar`/`CopilotChat` yok, `useEventStream(... && !kioskBare)`) |
| D1 | `docs/audit/trace-kiosk-mode-audit-2026-09-11.md` §Durum | değişir | +5/commit |

Dokunulmayan: `App.tsx` (rota aynı), `lib/dashboardUrl.ts`, `Login.tsx`,
`lib/api.ts` 401 yolu, `internal/chstore/*` (v1'de seam yok),
`internal/logstore/*`, `api.go` (sıfır satır).

**Commit sırası (her biri kendi `v0.10.X`'i, gate'ler tam):**

1. **B1+B2+B3** — bundle ucu + testler. FE tüketicisi yok; `TestMuxRoutePatterns`
   + ad-pin + anahtar testi. Tek başına deploy edilebilir, davranış değişmez.
2. **F1+F2+F3** — tip/istemci/hook. UI yok; `tsc` + `cancellation.test.ts`.
3. **F5+F6+F7+F14+F13** — kabuk kiosk-çıplak dalı + oturum-bitti kartı
   (`/trace` ile sınırlı). Bu noktada `/trace?id=…&kiosk=1` elle açılınca
   kabuksuz ama hâlâ eski sayfa (Topbar'lı) — geçici, 4 ile kapanır.
4. **F8+F9+F10+F12** — `TraceKiosk` sayfası (bundle tüketicisi) + panel
   taşıma. `Trace.tsx` davranışı aynen.
5. **F4+F11** — `traceHref` kiosk seçeneği + liste/detay "yeni pencerede aç"
   eylemi. Operatöre görünen ilk an bu.
6. "Daha fazla" (limit 1000) + `/logs` bağlantısı + cila; ölçüm raporu (önce/
   sonra, §7.4 tablosu) bu commit'in gövdesine.

Sıra gerekçesi: her adım öncekine bağımlı ama tek başına geri alınabilir;
kullanıcıya görünen eylem (5) en sona kalır ki yarım özellik prod'a çıkmasın.

---

## 11. Açık sorular (varsayım değil, karar)

1. **Kabuksuz dalın kapsamı:** yalnız `/trace?kiosk=1` mi, `?kiosk=1` taşıyan
   her sayfa mı? İkincisi Dashboard TV kiosk'unda `/api/events` (canlı
   invalidation) ve asistanı kaldırır — mevcut davranış değişir.
2. **Span detay paneli** kiosk'ta var mı? Varsa `SpanDetail` linksiz varyant
   (ek ~30 satır + `Link`'lerin `serviceLinks` benzeri tek bayrağa bağlanması)
   mi, yoksa v1 yalnız şelale + loglar mı?
3. **Yol adı:** `/api/traces/{id}/bundle` (aile) mi, istekteki
   `/api/trace/{traceID}/bundle` mi?
4. **Kiosk düzeni:** şelale üstte + loglar altta **aynı anda** mı (loglar ilk
   boyamada), yoksa sekmeli mi? Bundle'ın first-paint değeri buna bağlı.
5. **Bundle cache:** slot-başı `cachedJSON` (mevcut anahtarlar paylaşılır,
   `getTrace` küçük refactor) mi, tek anahtar 15 s mi?
6. **Normal `/trace` sayfası** da bundle'a geçsin mi (`?tab=logs` penceresiz
   çift isteği orada da kapanır) — v1 kapsamı yalnız kiosk mu?
7. **401 kartı:** donmuş şelalenin üstünde overlay mi, sayfanın yerine mi?
   "Yeniden giriş yap" aynı pencerede `/login` → kayıtlı URL'ye dönüş kabul mü?
8. **Log tavanı:** 500 → 1000 "daha fazla" + ötesi için `/logs?traceId=`
   bağlantısı (kabuklu, yeni pencere) kiosk'ta kabul edilebilir mi?
9. **Oracle satırları** bundle'ın üçüncü bacağı mı (CH, 5 s tavan), yoksa
   kiosk'ta ayrı tembel istek olarak mı kalsın?
10. **Tempo-önce 3 s bütçesi** bundle'a da uygulanır (mevcut davranış): Tempo
    yapılandırılı ama trace orada yoksa kiosk ilk boyaması 3 s'ye kadar bekler
    — kabul mü, kiosk'a özel daha kısa bütçe mi?
11. **`window.open` `noopener`:** opener ile iletişim (ör. seçili span'i
    senkron) istenmiyor varsayımı doğru mu?
12. **Prod router h2 mi h1 mi** (sse-http2 audit'i "ONAY BEKLİYOR"): önce/sonra
    ölçümü her iki durumda mı yapılsın; teşhis komutları
    `docs/audit/sse-http2-multitab-audit.md:37-58`.

---

## Durum

2026-09-11 — audit yazıldı (v0.10.670). Operatör "Önerini yapalım" ile
onayladı; §11 soruları önerilen cevaplarla kapandı: (1) kabuksuz dal yalnız
`/trace` · (2) v1 şelale + loglar, SpanDetail yok · (3) `/api/traces/{id}/bundle`
· (4) şelale üstte + loglar altta, aynı anda · (5) tek anahtar 15 s (slot-başı
`cachedJSON` ham JSON döndürdüğü için span'leri ikinci kez çözmek gerekirdi;
`/trace` sayfası cache'i paylaşılmaz — kiosk ayrı pencere) · (6) yalnız kiosk
· (7) overlay kart, aynı pencerede `/login` → dönüş · (8) 500 → 1000 "daha
fazla", ötesi `/logs` bağlantısı · (9) Oracle bundle'da · (10) Tempo bütçesi
aynen · (11) `noopener` · (12) ölçüm operatörde.

**GEMİDE (2026-09-11):**

| Dilim | Sürüm | İçerik |
|---|---|---|
| 1 | v0.10.671 | `GET /api/traces/{id}/bundle` (`trace_bundle.go`, defter kaydı; `traceDetailPayload` paylaşımı; WaitGroup; truncated/window) |
| 2 | v0.10.672 | `TraceBundleResponse`, `api.traceBundle`, `useTraceBundle`, `keys.traces.bundle`, iptal kapısı |
| 3 | v0.10.673 | `lib/kioskMode.ts` `isKioskBare`; AppShell kiosk-çıplak dalı (SSE/poll/krom yok); AuthProvider `sessionEnded`/`relogin`; `SessionEndedCard` |
| 4 | v0.10.675 | `pages/TraceKiosk.tsx` + saf `kioskModel`; `TraceLogsPanel` → `pages/trace/` (Trace.tsx 1607→1445) |
| 5 | v0.10.676 | `traceHref({kiosk:true})`; Traces NAME hücresi ⧉ + Trace detayı "⧉ Kiosk" (yeni pencere, noopener) |

Araya giren operatör bug'ı: v0.10.674 (palet kimlik araması küçük harf).

**Prod doğrulaması (operatör, 2026-09-12):** kiosk açılışı (liste ⧉ + detay düğmesi), satır-içi panel + toggle, loglar/"daha fazla", Logs bağlantısı (690 sonrası), oturum kartı, ölçüm ve h2/h1 teşhisi — 7 madde **OK**. Cilalar 678–693 (süre vurgusu, marka şeridi, Logs bağlantısı, Tempo düzeninde satır-içi span paneli; aynı düzen normal trace sayfasına 691–693) gemide.
