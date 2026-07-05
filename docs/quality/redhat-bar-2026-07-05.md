# Red Hat Quality Bar — audit raporu (2026-07-05)

Operatör direktifi: "Coremetry, Red Hat ürünleri gibi stabil ve arayüzlü olmalı."
Kapsam: stabilite + görsel tutarlılık, kombine audit. Üç paralel tarama
(UI tutarlılık ihlalleri / stabilite-crash sınıfları / PatternFly anatomi
farkları). Kısıtlar: yeni bağımlılık YOK, token-level CSS var'ları, plain React.

Üç en kritik bulgu el ile doğrulandı (App.tsx:91, globals.css:1025,
Services.tsx:203-213).

---

## Temiz çıkan sınıflar (iyi haber)

- `as any`: **0** kullanım.
- İkon paketleri: yalnız lucide-react + inline SVG — **0** ihlal.
- EventSource/WS: reconnect + catch-up + visibilitychange hepsi var.
- JSON.parse: localStorage/URL/SSE okuma yolları guard'lı (2 minör istisna:
  `lib/api.ts:1218,1246` — iç round-trip, düşük risk).
- Polling: tüm setInterval'ler `document.hidden` guard'lı; refetchInterval <10s
  yalnız /api/health (izinli). **`make audit`in Profiling.tsx:549 uyarısı
  YANLIŞ POZİTİF** — operatöre gösterilen örnek kod string'i, çalışan döngü
  değil.
- Sayısal URL param parse: `|| fallback` deseniyle NaN-korumalı.
- Günlük sayfaların chart bölme işlemleri guard'lı (NaN yalnız ikincil
  yüzeylerde: Profiling/AIObservability/flame bileşenleri — crash değil,
  görsel bozulma).

---

## Bulgular — öncelik sırasıyla

### 🔴 S1 — Tek global ErrorBoundary nav'ı da sarıyor (App.tsx:91)
Tek boundary `<AuthProvider>`/`<Routes>`'un DIŞINDA. Herhangi bir route'ta
render throw (örn. null'a `.toFixed`) → sidebar dahil TÜM uygulama kararır;
operatör başka sayfaya gidemez, tek çare Reload. Günlük sürülen konsol için
kabul edilemez. Fix: route-content seviyesinde ikinci boundary (AppShell
outlet'i saran, location.pathname'de reset'lenen) — nav hayatta kalır.
Effort: ~30 dk.

### 🔴 S2 — redhat temasında hardcoded koyu renk sınıfı (görünür bug)
- `#graph-wrap` (globals.css:1025) `#0d1117` literal + yalnız light override →
  redhat'ta beyaz sayfada simsiyah canvas. Doğrulandı.
- `.row-selected` (339-345) koyu accent rgba; redhat'ta PF mavisine dönmüyor.
- Status aileleri PF paletine hiç dönmüyor: `.badge .b-*`, `.status-banner/pill/dot`,
  `.toast-*`, `.trace-logs-sev.*`, `.wf-row.wf-*`, `.trace-lock`, `.ex` —
  hepsi koyu-palet rgba literal'leri (`63,185,80` / `255,82,82` / `56,139,253`).
  Fix: `color-mix(in srgb, var(--ok|--err|--warn|--accent) N%, transparent)`
  idiomu (kodda zaten var: `.wf-row.wf-err`, `.logtbl-dense`). Effort: ~1 saat.

### 🔴 S3 — Sayfa-seviyesi fetch yarışları (iptalsiz .then(setState))
Hızlı filtre/sort/sayfa değişiminde eski yanıt yeniyi ezebilir (stale-overwrite):
- `Services.tsx:203` (+ iç `serviceSparklines` :210) — EN üst sayfa. Doğrulandı.
- `Traces.tsx:300` (setData), `:405` (setAgg) — kardeş effect :382 guard'lı, bunlar değil.
- `Explore.tsx:262/284/298` — 300ms debounce'lu builder deps; her edit'te üst üste fetch.
- `AIObservability.tsx:51-52/72`.
Fix: standart `let cancelled=false` + cleanup deseni (kodda örneği var). Effort: ~1 saat.

### 🟡 S4 — `.toFixed`/`.toLocaleString` genişlik riski (425 site, kontrat-bağımlı)
TS `number` tipli ama backend null gönderirse (sıfır-trafik servis) route crash
= S1 yüzünden tam ekran karartı. Günlük kümeler: Services.tsx:506+,
Traces.tsx:1031-1036, Inbox.tsx:392+, AnomaliesPage.tsx:846+. Karar: S1
indikten sonra risk route-içi kalır; ayrıca en sık 4 sayfadaki kümeler
`fmtNum`-tarzı güvenli helper'a alınabilir. Effort: ~1 saat (4 sayfa) — tam
sweep önerilmez.

### 🟡 U1 — El yapımı butonlar: 54 site (46 yüksek öncelik)
`<Button variant size>` atomu dururken inline style'lı `<button>`. En görünür:
Logs.tsx (pillBtn ×3 + 2), Trace.tsx:809/842/853, Services.tsx:385,
Sidebar.tsx:505/549, Slos.tsx:483, Explore.tsx:444, AdminCatalog ×5,
ThresholdField ×6, CopilotChat, Login.tsx:238. Sayfa-bazlı batch'lerle
normalize. Effort: ~2 saat (2-3 release'e bölünür).

### 🟡 U2 — Hardcoded renk sweep'i (~30 site)
- Flame ailesi `#0d1117` metin/stroke literal'leri (FlameGraph:65/67, FlameDiff:84,
  AggregateFlame:151) — light/redhat'ta bozuk.
- LogsHistogram.tsx:29-52 severity paleti (`#dc2626/#ef4444/#f59e0b/#3b82f6`) →
  `--err/--warn/--accent` map'i.
- MultiLineChart:499/532/652 + TimeSeriesPanel:437 fallback'siz hex → `--purple` vb.
- ~19 rgba soft-bg sitesi → `--ok-soft` tarzı token'lar (SystemAnalysis.tsx:23
  doğru örnek). Effort: ~1.5 saat.

### 🟡 U3 — PF Empty action slot'u (S — ucuz, her yerde görünür)
`<Empty>` icon+title+body alıyor, primary action YOK (PF anatomisinin 4.
katmanı). ~40 çağrı sitesi faydalanır. Effort: ~30 dk.

### 🟡 U4 — Toast: warning yok + FlashBox ayrışması
`toast-{success,error,info}` var, `warning` yok; Settings `FlashBox` kendi
rgba'larıyla ayrı sistem. Severity token birleşimi + warning. Effort: ~45 dk.

### 🟡 U5 — useDataTable dışı tablolar (~23)
Kendi sort'unu yazan 3 (DBQueriesPanel, MethodHotspots, explore/TracesResult)
önce; sort/resize'sız operatör tabloları (Alerts:534, AIObservability ×2,
SystemAnalysis:118, Runbooks:85, NoisyRulesPanel:171…) sonra; settings config
tabloları en son. Effort: ~2 saat (bölünür).

### 🔵 P1 — Drawer primitive'i (4-5 el yapımı çekmece kabuğu)
InboxTriageDrawer, CopilotChat, CorrelationContextDrawer, ColumnManager…
Modal.tsx'in çekmece kardeşi (title/close/body/footer + --shadow-md). ~2 saat.

### 🔵 P2 — Settings formları → kanonik Field (validation state'li)
`pages/settings/shared.tsx`teki ikinci Field kopyası error/aria-invalid
bilmiyor; 7 tab raw label+input (PipelineTab ×11, TempoTab ×7, ElasticTab ×9,
AiTab, BackupTab, KibanaTab, RolesTab). ~2 saat.

### 🔵 P3 — Topbar'a breadcrumb/description/actions slot'ları (PF title block)
41 sayfa tek Topbar kullanıyor (iyi) ama PF anatomisinin description/actions
katmanları yok; detay sayfaları `.svc-head`/`.rb-bar` one-off'ları. ~1 saat + sayfa adaptasyonu.

### 🔵 P4 — Liste sayfalarında Toolbar + aktif-filtre chip-group standardı
`.fb-chip`/`.facet` primitive'leri yalnız Traces/Explore'da; Services/Logs
dolu input = aktif filtre. PF toolbar anatomisi. ~yarım gün.

### 🔵 P5 — Tab-strip kaçakları (3): Profiling.tsx:199, AdminSql.tsx:273, AdminStatusPage.tsx:229. ~20 dk.
### 🔵 P6 — Tri-state kaçakları (2): ServiceFlow.tsx:76, ServiceClusterBreakdown.tsx:33 (load+error sessizce null). ~20 dk.
### 🔵 P7 — make audit: Profiling:549 yanlış pozitifini whitelist'le. ~10 dk.
### 🔵 P8 — Spacing scale sweep'i — düşük görünür etki, ÖNERİLMEZ şimdilik.

---

## Önerilen dilim sırası (her biri kendi v0.8.X'i)

1. S1 route-level ErrorBoundary (~30 dk)
2. S2 redhat token kaçakları (~1 saat)
3. S3 fetch-race iptalleri (~1 saat)
4. U3 Empty action slot (~30 dk)
5. U2 renk sweep'i — flame + LogsHistogram önce (~1.5 saat)
6. U1 buton atomu sweep'i — Logs/Trace/Services/Sidebar batch'i önce (~2 saat)
7. U4 toast warning + FlashBox (~45 dk)
8. S4 `.toFixed` günlük-sayfa güvenli-format (~1 saat)
9. U5 tablolar (~2 saat) → sonra P1-P7 polish kuyruğu
