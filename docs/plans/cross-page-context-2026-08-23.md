# Sayfalar arası bağlam — varlık-merkezli gezinme planı

**Tarih:** 2026-08-23 · **Yön (operatör):** "Entity modeline geçtikten sonra
sayfalar arası ilişkiyi de kurabiliriz bağlam güçlensin."
**Taban:** `docs/audit/entity-model-audit-2026-08-23.md` (§7.2 A1-A4, §7.3 B1,
§7.4 kırmızı çizgiler) + memory `project-entity-model-direction.md` (sıra).
**Yöntem:** beş mercek, salt-okuma kod taraması. Hiçbir sorgu çalıştırılmadı,
hiçbir sayfa tarayıcıda açılmadı — §7'ye bakınız.
**Revizyon 2026-08-24 (eleştiri turu):** 29 eleştirinin tamamı ele alındı —
23'ü uygulandı, 6 iddia koddan çürütüldü ya da rakamı düzeltildi (**§8**).
En ağır üçü: (1) A2 **gemide** (v0.9.1317) → §3.4'ün yokluk maddesi silindi,
**Faz 3 kaldırıldı**; (2) geri linkleri ve **backend'in ürettiği 33 link** hiç
incelenmemişti → K6/K7 açıldı; (3) MRU/pin kaydı **var**, doküman yok
sanıyordu — planın en ucuz kazancı onun altında saklıydı.

---

## 1. Özet

Bugün Coremetry'de gezinme **sayfa-merkezli**: operatör bir listeden bir
sayfaya gider, sayfa kendi filtresini kurar, geçiş sırasında yalnız iki eksen
(zaman aralığı ve `env`) hayatta kalır. Hedef **varlık-merkezli** gezinme:
ekranda görünen her varlık adı (servis, pod, endpoint, DB instance, kuyruk,
sürüm, host) o varlığın sayfasına giden bir kapı olsun ve o kapıdan geçerken
operatörün kurduğu bağlam (pencere, env, cluster, kapsam) düşmesin. Altyapı
sanıldığından çok daha olgun: `serviceHref` 56 çağrımla CI kapılı tek üretici
(`frontend/src/lib/serviceHref.ts:62`, kapı `serviceHref.test.ts:303`),
`pivotHref` ailesinde pencere **tip düzeyinde zorunlu** argüman
(`frontend/src/lib/pivotHref.ts:17-19`), `navHref` üç kabuk yüzeyini tek
sözleşmeye bağlamış (`frontend/src/lib/navHref.ts:40`). Kopmalar altyapı
yokluğundan değil **kardeş yüzey atlamasından** doğuyor: aynı satır şeklini
çizen iki tablodan biri linkli, öbürü düz metin — ve `/trace`, `/logs` gibi en
çok tıklanan hedeflerde hiç üretici ve hiç kapı yok. Veri tarafında ise ilişki
kalıcı değil: çağrı grafiği 5 dakikalık kovaların yığını, kenarın kimliği ve
yaşam döngüsü yok (`internal/chstore/store.go:1900-1954`, TTL 14 gün).
Bu doküman entity yol haritasının **üstüne oturan** gezinme katmanını tarif
ediyor ve entity katmanı olmadan bugün yapılabilecek işleri ayrı işaretliyor —
çünkü ölçülen **15 çıkmazın 13'ünde** hedef üretici zaten depoda duruyor
(yalnız Ç4 ve Ç11 A3'e yaslanıyor). Aynı şey kırık linkler için de geçerli:
yedi kalemin tamamı entity katmanından bağımsız.

---

## 2. Bugün ne VAR

Bu bölüm **yeniden inşa edilmesin** diye var. Aşağıdakilerin hiçbiri kuyruğa
alınmaz.

| Yetenek | Nerede | Durum |
|---|---|---|
| `/service`'in tek üreticisi + CI kaynak-tarama kapısı | `frontend/src/lib/serviceHref.ts:62` · kapı `serviceHref.test.ts:303`, allowlist BOŞ (`:205`) | 56 çağrım, el-yapımı link 0 |
| Olay penceresi türeticileri (inbox / tek-an / yaşam süresi) | `serviceHref.ts:94, :113, :140` | Bozuk damgada `undefined` döner, epoch-0 penceresi çakmaz |
| `/traces` pivot ailesi, pencere ZORUNLU alan | `frontend/src/lib/pivotHref.ts:17-19, :60, :96, :131, :199, :265` | Dört ayrı pencere-düşürme olayının (v0.9.208/213) yapısal cevabı |
| Gezinti taşıyıcısı (sidebar / ⌘K / `g x`) | `frontend/src/lib/navHref.ts:40-58`, kapı `navHref.test.ts:72-88` | Yalnız `custom:` mutlak pencere + `env` taşır; hedef kazanır kuralı var |
| Zaman aralığı zinciri `?range=` → sessionStorage → sayfa varsayılanı, mount'ta URL'e geri-yazım | `frontend/src/lib/useUrlRange.ts:136, :159-165, :179` | v0.9.937'den beri mutlak pencereler de kayıtta |
| `env` kalıcılığı + "hangi sayfa gerçekten uyguluyor" kapısı | `frontend/src/lib/useUrlEnv.ts:53-61` · `frontend/src/lib/envApplies.test.ts:53-81` | 12 sayfa uygular, gerisinde picker devre dışı (v0.9.864 dürüstlük kararı) |
| Yabancı param koruma sözleşmesi (sessiz silme sınıfı kapalı) | `frontend/src/lib/urlState.ts:164-177` · `pages/Traces.tsx:330` · `pages/Explore.tsx:332` | Efekt yalnız kendi anahtarlarına sahip |
| Domain detay üreticileri: pod / endpoint / database / statement / topoloji düğümü | `pages/service/podDetailPath.ts:13` · `pages/endpoints/endpointParam.ts:61` · `pages/databases/databaseParam.ts:34, :68` · `components/topology/nodeDetailHref.ts:115` | `nodeDetailHref` çözemediğinde `null` döner, uydurma link çizmez (`:108-114`) |
| Legacy derin-link çözücüleri (tek atlamada yeni sayfa) | `endpointParam.ts:90` · `databaseParam.ts:120` · `App.tsx:95` (`/topology`→`/service-map`) | Shim değil redirect |
| Yatay ilişki verisi (kalıcı): servis→servis, servis→çağıran, op→op, akış→servis | `internal/chstore/store.go:1900, :1978, :2005, :2023` (dördü de TTL 14 gün) | Aggregator + Redis lider kilidi yazıyor (`internal/topology/aggregator.go:137-207`) |
| INSERT-zamanı ilişki MV'leri: db / messaging / operation / spanmetrics | `store.go:3230, :3277, :3333, :3525, :3558, :3064, :3140` | TTL 90 gün |
| Hesaplanmış kök-neden ilişkisi (kalıcı, yönlü) | `store.go:1331` `root_cause_hypotheses` | Envanterdeki tek kalıcı "A, B'nin sebebi" kaydı |
| Tam yaşam döngüsü taşıyan kavramlar | `exception_groups` (`store.go:1130`), `service_version_5m` `minState` (`store.go:3378`), `problems`, `anomaly_events` | Mekanizma repoda VAR — yanlış kavramlara bağlı |
| Varlık detay sayfası | `App.tsx:130` `/service`, `:142` `/endpoint`, `:148` `/database`, `:154` `/pod` | Dördü gerçek sayfa |
| Pencere → `range=` dönüştürücüsü (TEK yer) | `frontend/src/lib/urlState.ts:28` `windowRangeParam` | Üç üretici üstüne biniyor: `serviceHref.ts:59`, `pages/slowqueries/stmtParam.ts:58`, `components/DBQueriesPanel.tsx:109` |
| Servis MRU + pin (localStorage) | `frontend/src/lib/recentServices.ts:30` `recordServiceVisit` · `:36` `getRecentServices` · `:40` `getPinnedServices` · `:51` `toggleServicePin`; anahtarlar `lib/storage.ts:35-36`, kanonik-anahtar kapısı `lib/pinnedKey.test.ts:14-25` | Yazıcılar: `pages/Service.tsx:81` (her detay açılışı), `pages/Services.tsx:98, :105` (pin). Okuyucu **tek**: `components/CommandPalette.tsx:280-281` → `:293-294` '★ Pinned' / 'Recent' |
| Metrik MRU | `frontend/src/lib/recentMetrics.ts:36, :42`, anahtar `storage.ts:37` | Tüketicisi var: `components/viz/GroupedMetricPicker.tsx:75, :79` |
| "Son odaklanılan servis" (topoloji) | `lib/storage.ts:52` `topoFocus` (v0.9.225) | Harita ilk boyamasını hızlandırmak için; gezinme yüzeyi değil |
| Drill çipi primitifi ("view in X") | `frontend/src/components/DrillButton.tsx:53`, `range`i `encodeRange` ile kendi kodluyor (`:54-57`) | v0.5.463'te "8+ yerde kopyalanan `<Link to={\`/traces?service=…\`}>` deseni" için yazıldı; bugün yalnız **iki** dosya kullanıyor: `pages/Service.tsx:414, :417, :420, :431, :434, :437` ve `pages/Trace.tsx:417` |
| Kuyruk/topic kimlik kodeği | `frontend/src/pages/messaging/destinationParam.ts:20` encode · `:33` decode, kapı `destinationParam.test.ts` | `endpointParam` / `databaseParam` ile aynı olgunlukta; tüketicileri `pages/Messaging.tsx:13` **ve** `components/topology/nodeDetailHref.ts:62` |
| URL→varlık kayıtları (ikinci kaynak-hakikat riski) | `frontend/src/lib/chatContext.ts:38-50` `SERVICE_PARAM_ROUTES` + `serviceFromRoute` · `frontend/src/lib/aiSubject.ts:20-24` `AI_KINDS` (`?ai=service-health:<svc>:<from>:<to>`) | Chat'in varsayılan servis kapsamını ve AI çekmecesinin öznesini bunlar belirliyor; **elle bakımlı**, tsc göremez |
| Dar-ekran kolon düşürme mekanizması | `frontend/src/lib/dataTable.ts:87` `mobileHide?` · `:106` `visibleColumns` süzgeci, kapı `lib/dataTableMobileHide.test.ts` (+`:141` "tüketiciler kayıtlı" izin listesi) | **TÜKETİCİSİZ**: depoda `mobileHide` işaretleyen tek kolon yok (`:14-17` şerhi bunu kendisi söylüyor) |

---

## 3. Nerede KOPUYOR

### 3.1 Kırık linkler (bugün yanlış yere gidiyor)

| # | Ne | Kanıt | Belirti |
|---|---|---|---|
| K1 | `stmtDetailHref` var olmayan `/slow-queries` rotasını üretiyor; gerçek rota `/databases/slow-queries` | üretici `pages/slowqueries/stmtParam.ts:60` · rota `App.tsx:150` · catch-all `App.tsx:195` `<Navigate to="/" replace />` | Servis sayfası → DB sorguları paneli → "Detail →" = **ana sayfa**. Üstelik yanlış yazım bir testte çivili: `stmtParam.test.ts:110` |
| K2 | `statementTracesHref` pencereyi hiç yazmıyor | `features/dependencies/panels/shared.tsx:427` (`range=` yok); aynı bileşen `serviceHref(instance, { range })` kullanıyor (`shared.tsx:492`) | `pivotHref`'in var oluş sebebinin (dosya başı şerh `pivotHref.ts:4-16`) birebir ihlali; hedef sticky pencerede açılır |
| K3 | `/shift` üç servis linki de penceresiz | `pages/Shift.tsx:155, :183, :211` — sayfa penceresi `?w=` ile aynı dosyada (`:58`) | 24 saatlik vardiya özetinden tıklanan servis, 30dk sticky pencerede açılır. `serviceHref.test.ts:263-266` şerhi "penceresiz tek site CommandPalette" diyor — şerh bayat |
| K4 | `/service-map` düğüm çekmecesini açan **hiçbir kod yolu yok** | vaat `pages/ServiceMap.tsx:107-109` · `commitNode` tek çağrımı `:464` `onClose` · grafik tıkı `:453` `onSelectNode={commitFocus}` (`TopologyFlowGraph.tsx:557`) | `ServiceMapNodeDrawer` (inbox/logs/traces/endpoints pivotları, `:85-105`) yalnız URL elle yazılarak açılabiliyor |
| K5 | `/services` filtreleri URL'e yazılmıyor, gelen `?cluster=` bayat kalıyor | `pages/Services.tsx:172-175` (init-only `useState`), `:587` `setCluster`; `setSearchParams` yalnız `:127`/`:251` | Dropdown'dan B'ye geçen operatörün URL'i hâlâ `cluster=A` der; paylaşım/F5 operatörü A'ya atar. ⚠ dosya v0.9.1317'de düzenleniyor, satırlar kayabilir |
| K6 | **Backend'in ürettiği ürün linkleri penceresiz** — K2/K3'ün Go'daki aynısı | Ölçüm (`grep -n 'Href: *"/' internal/api/copilot_followup.go internal/api/chat_tool_links.go`): **33 el-yapımı in-app href**, 25'i `copilot_followup.go` (`:244, :258, :259, :264, :265, :268, :271, :273, :276, :286, :288, :290, :292, :294, :308, :312, :336, :339, :343, :345, :362, :367, :373, :380, :382`), 8'i `chat_tool_links.go` (`:62, :67, :69, :81, :83, :85, :87, :89`). `range=` yazan **tek** site `copilot_followup.go:373` (request-ID log penceresi). Ayrıca `chat_tool_links.go:18-27` sözleşmeyi frontend SATIR NUMARALARINA çiviliyor (`Traces.tsx:234`, `logsUrl.ts:42`) — hiçbir kapının görmediği, elle bakımlı diller-arası bağ | AI cevabındaki "Trace'ler →" çipi, cevabın konuştuğu 14:32 olayına değil operatörün sticky penceresine açılır. FE'de `pivotHref` bunu tip düzeyinde imkânsız kılarken (`pivotHref.ts:17-19`) backend aynı hatayı serbestçe yapabiliyor |
| K7 | **Geri/kırıntı linklerinin yarısı bağlamı düşürüyor** — bu, deponun KENDİ kapattığı bir bug sınıfı | Taşıyan emsaller: `pages/Pod.tsx:212` `serviceHref(service, { range, tab })` (şerh `:206-209`: "GERİ linki penceresini taşımıyordu… Bir geri linkinin bağlamı değiştirmesi, geri linki olmaktan çıkması demek", v0.9.965) · `pages/ServiceBacktrace.tsx:108` `serviceHref(svc, { range })`. Taşımayan çıplak beşli: `pages/Service.tsx:399` `<Link to="/services">` · `pages/EndpointDetail.tsx:137` `<Link to="/endpoints">` · `pages/DatabaseDetail.tsx:176` `<Link to="/databases">` · `pages/SlowQueries.tsx:201` `<Link to="/databases">` · `components/TriageCrumb.tsx:12` `<Link to="/inbox">` | 6 saatlik bir pencereden bir endpoint'e inen operatör "← Endpoints" dediğinde 30dk sticky pencereye düşer. §1'in teşhisi olan **kardeş yüzey atlamasının** birebir örneği: aynı işi yapan yedi linkten ikisi doğru, beşi çıplak |

### 3.2 Çıkmaz sokaklar (varlık adı görünüyor, gidilemiyor)

Hedef üreticisi **zaten depoda** olanlar. **"Hedef" sütunu bir KARAR
istiyor:** *git* (rota değiştir) mi, *daralt* (bu değere filtrele, sayfada
kal) mı? İkinci seçenek depoda zaten var ve daha olgun —
`components/LogTable.tsx:78-108` `kv-filterable` satırı `onAdd`/`onExclude`/
`onToggleCol` ile "bu değere daralt"ı sunuyor, `components/BubbleUpPanel.tsx:163-170`
`onApply` aynı model. Bir log satırında operatörün istediği çoğu zaman
"/pod'a git" değil "bu pod'a daralt"tır; ikisi aynı hücrede yan yana da
durabilir (⊕ çipi + adın kendisi link).

⚠ **Tıklanabilir satır tuzağı:** Ç1/Ç3/Ç5/Ç9'un hepsi satır-tıklı yüzey
(`LogTable.tsx:396` `<tr onClick={onClick}>`). İçine konan her `<Link>`
`onClick={e => e.stopPropagation()}` almalı — emsaller `LogTable.tsx:483`
(trace linki) ve `pages/Hosts.tsx:118` (`<td onClick={e => e.stopPropagation()}>`).
Unutulursa link hem gider hem satır çekmecesini açar.

| # | Nerede | Kanıt | Hazır hedef |
|---|---|---|---|
| Ç1 | Endpoint detayı "boyuta göre böl" — `k8s.pod.name` / `host.name` / `peer.service` / `service.version` değerleri düz metin | `pages/endpoints/detailSections.tsx:425` `<td … title={r.value}>{r.value}</td>`; boyut listesi `:339-349`; aynı dosya `serviceHref`'i `:21`'de import ediyor | `podDetailPath`, `serviceHref`, `operationTracesHref` |
| Ç2 | Kök-neden panelinde bubble-up değerleri terminal | `components/RootCausePanel.tsx:86-87` statik `<span>`; ikizi `components/BubbleUpPanel.tsx:163-170` `onApply` ile tıklanabilir | `BubbleUpPanel` + `onApplyFilter`; `pages/service/ServiceLatencyHeatmap.tsx:230-234` prop'u geçmediği için orada da `disabled` |
| Ç3 | `/logs` satırında service · cluster · pod hücrelerinin hiçbiri gezinilebilir değil | `components/LogTable.tsx:436-462` (üçü de `<td>`/`<span>`); dosyada `serviceHref` importu yok | `serviceHref`, `podDetailPath` |
| Ç4 | `SpanDetail`'de yalnız servis kimliği linkli; db / messaging / pod / route ölü | `components/SpanDetail.tsx:523-532` link kuralı üç anahtara sabit; `:289-292` Info satırları insan etiketi taşıdığı için kural ısırmıyor | `databasesFilterHref`, `endpointDetailHref`, `podDetailPath` **ve messaging** — kimlik kodeği hazır (`pages/messaging/destinationParam.ts:20`), eksik olan yalnız ince bir `destinationHref(ref, {range})` sarmalayıcı |
| Ç5 | DB çağıran tablolarında "Pod / host" hücresi düz metin (iki yerde) — servis aynı satırda linkli | `features/dependencies/DetailDrawer.tsx:664-666` · `pages/DatabaseDetail.tsx:531` (Link) vs `:537` (düz) | `podDetailPath` (yalnız `pod` zorunlu) |
| Ç6 | Problem detayında cluster çipleri linksiz — listede linkli | `features/anomalies/ProblemDetail.tsx:787-789` elle `pb-pill`; primitif `components/ClusterChips.tsx:29` `/services?cluster=` | `ClusterChips` (dört yüzeyde kullanılıyor) |
| Ç7 | Exception grubu → hatanın olduğu operasyon: veri geliyor, hiç çizilmiyor | `lib/types.ts:2968` `spanName; // operation that errored`; `ProblemDetail.tsx:578-596` örnek satırı spanName'i basmıyor; tek gösterim **`features/anomalies/AnomaliesPage.tsx:787`** düz `<b>` (dosya `pages/` altında DEĞİL) | `operationTracesHref` |
| Ç8 | Deploy sürümü hiçbir yüzeyde o sürümün span'lerine köprülenmiyor | Ölçüm (`grep -rn "recentDeploy.version\|versionAfter\|\.version}" --include=*.tsx`, test hariç): **en az 11 gösterim / 9 dosya** — `pages/Deploys.tsx:150` · `components/DeployHistoryPanel.tsx:147` · `features/anomalies/ProblemsSection.tsx:651` (rozet `:648` `stopPropagation` ile tıkı **yutuyor**) · `components/RootCausePanel.tsx:275` · `components/RootCauseRibbon.tsx:221` · `features/anomalies/ProblemDetail.tsx:691, :722` · `features/anomalies/AnomalyDetailDrawer.tsx:132, :249` · `features/anomalies/streams.tsx:766` · `components/ServiceCharts.tsx:825`. (Önceki "5 yer" rakamı eksikti.) | `resource.service.version` filtrelenebilir anahtar: `lib/useAttributeKeys.ts:38` |
| Ç9 | İfade tablolarında motor / db.name / servis hücreleri kataloğa bağlı değil | `pages/Databases.tsx:419` düz; kardeşi `pages/SlowQueries.tsx:270` linkli; çalışan emsal `components/DBQueriesPanel.tsx:198` `databasesFilterHref` | `databasesFilterHref` tam bu satır şekli için yazılmış |
| Ç10 | Servis adı düz metin: `/slos:124, :250`, `/alerts:634`, `explore/TracesResult.tsx:159`, `Traces.tsx:1401` (`SvcBadge` = `<span>`, `components/traces/shared.tsx:39-51`), `Profiling.tsx:179` | — | `serviceHref` |
| Ç11 | `/external` Explore pivotu yalnız `peer.service` filtreliyor, oysa sunucu üç kaynaktan kimlikliyor | link `pages/External.tsx:207` · sunucu `internal/chstore/external_paths.go:227-229` coalesce zinciri · sayfanın kendi metni `External.tsx:106-109` | v0.9.256/268'de messaging+db için kapatılan bug sınıfının aynısı; `pivotHref.ts`'te `externalTracesHref` YOK |
| Ç12 | Dış bağımlılığın "en çok çağrılan yollar" satırları terminal | `components/ExternalPaths.tsx:86-92` | Kırılım soruyu soruyor, cevabın üstüne basılamıyor |
| Ç13 | `/incidents` listesinde servis hücresi düz metin — **aynı hücrede cluster çipi linkli** | `pages/Incidents.tsx:157` `{i.service \|\| '—'}` düz, hemen altında `:158` `<ClusterChipsRef clusters={i.clusters} />` linkli (import `:7`); dosyada `serviceHref` importu **0** | `serviceHref` + `eventLifespanWindow` — kardeş detay sayfası bunu ZATEN çözmüş: `pages/Incident.tsx:300` `serviceHref(inc.service, { range: eventLifespanWindow(inc) })`. Kopyalanacak tek satır |
| Ç14 | **Trace ailesi** — deponun en çok bakılan yüzeyinde her span satırının servis adı ölü | `components/TraceWaterfall.tsx:721-723` `<span className="wf-svc" title={\`service.name: …\`}>` · `components/traces/TraceLogList.tsx:74-75` `{l.serviceName}` düz · `components/TracePeekDrawer.tsx:131` `sub={summary.root.serviceName}` düz (aynı dosya `:118`, `:192`, `:201`'de el-yapımı `/trace?id=` ve `/logs?` kuruyor — kapı boşluğunun canlı örneği) | `serviceHref` (waterfall'da satır tıkı span seçtiği için **stopPropagation şart**) |
| Ç15 | Topoloji **external** düğümü linksiz, oysa hedef hazır | `components/topology/nodeDetailHref.ts:170` `return null` (external dalı) · hedef mevcut: `App.tsx:151` `/external` rotası + `pages/External.tsx:79-82` çalışan `?host=` derin linki (`params.get('host')` → çekmece, `replace:true` + yabancı param koruması) | `/external?host=<name>` — **entity katmanına borçlu değil**, bugün yazılabilir |

**Kapı boşluğu (bu sınıfın kök nedeni):** kaynak-tarama kapısı yalnız tek bir
dize arıyor — `serviceHref.test.ts:303` `if (!text.includes('/service?name='))
continue;`.

**Sayım (ölçüm komutu yazılı, çünkü bir önceki tur üç farklı rakam üretti).**
`cd frontend/src && grep -rn "/trace?id=" . | grep -v node_modules`:

| Süzgeç | `/trace?id=` | `/logs?` |
|---|---|---|
| ham (test dosyaları dahil) | 52 satır / 33 dosya | 24 |
| test hariç | 48 satır / 29 dosya | 13 |
| test **ve yorum satırı** hariç = **gerçek kod sitesi** | **39 / 24 dosya** | **13 / —** |

Dokümanın önceki "48 site" rakamı yorum satırlarını da sayıyordu (9 tanesi
yorum: `LogTable.tsx:164`, `TracePeekDrawer.tsx:25`, `ai/useChatThread.ts:56`,
`lib/utils.ts:339-340`, `lib/api.ts:1840`, `lib/types.ts:472`, `:3682`,
`pages/PublicTrace.tsx:18`). İş kalemi **39 site**tir; Faz 1 tahmini buna göre
daraldı.

**Rota yazımı** için hiç kapı yok — K1 tam bu yüzden gemiye gitti. Yardımcı
yarımlar mevcut: `lib/urlState.ts:28` `windowRangeParam` (pencere→`range=`
dönüşümünün TEK yeri, üç üretici üstünde) ve `lib/logsUrl.ts:89`
`logsRangeParam`, ama ikincisinin yalnız 2 çağrımı var ve **sekiz üretim
sitesi** kendi `custom:${Math.floor(fromNs/1e6)}-…` kopyasını taşıyor
(ölçüm `grep -rn 'custom:\${' --exclude='*.test.*'`):
`features/anomalies/AnomalyDetailDrawer.tsx:178`,
`components/InboxTriageDrawer.tsx:150`,
`components/CorrelationContextDrawer.tsx:339` ve `:344`,
`components/ServiceMapNodeDrawer.tsx:95`, `components/TracePeekDrawer.tsx:201`,
`components/ai/ServiceChartsExplainBody.tsx:187`,
`pages/service/ServiceLatencyHeatmap.tsx:199`. (Önceki "altı site" eksikti.)

**Ayrım — kapı yok ≠ üretici yok.** Kapı yokluğu iddiası aynen geçerli.
Üretici yokluğu **yarı** doğru: `/trace`/`/logs` için adlandırılmış bir
`traceHref`/`logsHref` yok, ama pencere kodlaması `windowRangeParam` +
`logsRangeParam` ile zaten çözülmüş — Faz 1'in işi sıfırdan yazmak değil,
mevcut yarımın üstüne ince bir üretici koymak.

### 3.3 Düşen bağlam çiftleri (geçiş sırasında ne kayboluyor)

| Eksen | Taşıyıcı | Somut düşen çift |
|---|---|---|
| Zaman aralığı — **göreli** preset | **sticky (`useUrlRange`) taşıyor** — `navHref` yalnız `custom:` yazar (`navHref.ts:47`, bilinçli: §6.6) | Bu bir mimari boşluk DEĞİL, tek sayfalık bir bug. v0.9.937'den beri `useUrlRange.ts:176` `writeStoredRange(enc)` **göreliyi de mutlağı da** oturum kaydına yazıyor, yani hook'u sahiplenen her sayfada preset sticky kanalıyla taşınıyor. Kopan **tek** yer `/explore`: `pages/Explore.tsx:108-109` `useState<TimeRange>` + `storedRangeString()` ile okuyor, `:723` `onRangeChange={setRange}` yerel state'e gidiyor, dosyada `rememberRange`/`writeStoredRange` çağrımı **yok**. ⚠ `useUrlRange.ts:67-69`'daki "(Explore, Metrics)" atfı da bayat: `pages/Metrics.tsx:86` `useUrlRange('30m')` kullanıyor, bu tuzağa düşmüyor |
| `env` — URL'e geri-yazım | **yarım** | Seçim URL'e **yazılıyor**: `useUrlEnv.ts:66-81` `setEnv` `next.set('env', v)` + `{ replace: true }` + sticky güncellemesi. Yok olan yalnız **mount'ta sticky→URL geri-yazım efekti** (`useUrlRange.ts:159-165` emsali). Yani kayıp yalnız taze-sekme / sticky-restore dalında: env sticky'den geliyorsa paylaşılan URL onu göstermez. Sayfa geçişlerinde kayıp yok — `navHref.ts:58` mevcut URL'deki env'i taşıyor. `lib/shareUrl.ts` yalnız `range`i donduruyor |
| `cluster` | **hiç yok** | `/clusters?cluster=A` → sidebar → `/logs` = tüm cluster'lar. Kalıcı kayıt da yok (`lib/storage.ts:17-70`'te cluster girdisi yok) |
| Servis kapsamı | yalnız açık pivot üreticileri | `/service?name=X` → sidebar → `/traces` = tüm servisler. **"Son bakılan servis" kaydı VAR ve bağlı** — `lib/recentServices.ts` (v0.7.89): `pages/Service.tsx:81` her detay açılışında `recordServiceVisit(svc)`, `pages/Services.tsx:98, :105` pin'i yazıyor, anahtarlar `lib/storage.ts:35-36`, kanonik-anahtar kapısı `lib/pinnedKey.test.ts:14-25`. Eksik olan iki şey: **(a) tek tüketicisi ⌘K** (`CommandPalette.tsx:280-281` okuyor, `:293-294` '★ Pinned'/'Recent' basıyor) — hiçbir sayfa "son bakılanlar" şeridi göstermiyor; **(b) MRU yalnız servis + metrik için** (`recentMetrics.ts`, tüketicisi `GroupedMetricPicker.tsx:75`) — pod / endpoint / database / destination karşılığı yok |
| `compare` (önceki-döneme kıyas) | **asimetrik — `env` ile aynı hastalık** | `/endpoints` URL'de tutuyor: `pages/Endpoints.tsx:228` `params.get('compare')`, yazımı `:231`, kapsam kodeğinde `pages/endpoints/endpointParam.ts:43, :71, :99` ve kapısı `pages/endpoints/endpointPage.test.ts:13` ("the href must carry the SCOPE (range/env/cluster/compare/entry)"). `/service` ise **localStorage**'da tutuyor: `components/ServiceCharts.tsx:148` `useState<CompareMode>` + `lib/storage.ts:60` `svc.charts.compare`. Sonuç: paylaşılan `/service` linki kıyası yansıtmaz. §3.3'ün "kolon kalıcılığı tutarsız" kaleminin ikizi |
| Zaman penceresi kavramı | **/problems, /inbox, /anomalies'te hiç yok** | `AnomaliesPage.tsx:253-264` API çağrısında `from`/`to` yok; `lib/queries/inbox.ts:53` yalnız `since?`; `Topbar` picker'ı `range && onRangeChange` verilmedikçe çıkmıyor (`components/Topbar.tsx:41-47`). `?range=` URL'de yolculuk etmeye devam ettiği için adres çubuğu pencere varmış gibi görünür |
| Seçili kolonlar | sayfa-yerel, kalıcılık tutarsız | `/traces` + `/logs` localStorage'a yazıyor (`Traces.tsx:119`, `Logs.tsx:209`), `/endpoints` + `/explore` yalnız URL'de (`Endpoints.tsx:365-372`, `Explore.tsx:241`). Kolon **genişlikleri ve sıralaması** her tabloda kalıcı (`lib/storage.ts:73-74`) — aynı tablonun yarısı hatırlanıyor |
| Arama metni | sayfa-yerel (makul) | Tek gerçek eksik `/services`: kutu tamamen React state (`Services.tsx:141`) |
| **Liste durumu (scroll + facet) — geri dönüşte** | rota değişiminde **hiç yok** | `<ScrollRestoration>` depoda **hiç kullanılmıyor** (tüm `src/`de tek `window.scrollTo`: `pages/Alerts.tsx:282`). Deponun kendi çözümü rota DEĞİŞTİRMEMEK: `pages/Inbox.tsx:232-234` şerhi "liste + bölüm MOUNTED kalır (display:none) ki facet/seçim state'i '← Problems'te yerinde dursun", uygulaması `:561` `<div className="page-body" style={problemParam ? { display: 'none' } : undefined}>` (v0.9.837, `/problems`'ın v0.8.426/428 deseni). Yani derinlemesine gidiş için **"aynı rotada `?param=`"** tercih edilmiş; bu planın önerdiği rota-değiştiren linkler o kazanımı kullanamaz — `spaLinks.test.ts`'in saydığı bedelin (React Query önbelleği, TTFI) yumuşak hâli |
| Dar ekran (kolon düşürme) | **mekanizma kurulu, tüketici yok** | `lib/dataTable.ts:87` `mobileHide?` + `:106` `visibleColumns`, kapı `lib/dataTableMobileHide.test.ts` (`:14-17`: "Bu sürümde HİÇBİR kolon `mobileHide` taşımıyor"). Bu plan ~10 yoğun tabloya yeni link hücresi ekliyor; hangisinin telefonda düşeceği kararı Faz 1'in içinde. Dokunma hedefi kuralı `styles/globals.css` ≤640px bloğunda (v0.9.1023, 36px taban / WCAG 2.5.8 24px şerhi) ve satır içi yeni `<Link>`ler tam olarak o kuralın konusu |

### 3.4 Veri modeli tarafındaki kopmalar

- **Kenarın kimliği ve yaşam döngüsü yok.** `topology_edges_5m` ORDER BY'ın
  başında `time_bucket` var (`store.go:1900-1954`); aynı A→B çifti her 5
  dakikada yeni satır. `first_seen`/`last_seen`/`edge_id` hiçbir kenar
  tablosunda yok (tek yarım istisna `service_callers_5m.last_seen_ns`,
  `internal/chstore/backtrace.go:42`). "Bu bağımlılık ne zaman doğdu / ne
  zaman kayboldu" **sorulamıyor**; kenar-kaybı için alarm/anomali dedektörü de
  yok.
- **TTL asimetrisi.** Kenar tabloları 14 gün ve **operatör ayarına kapalı**
  (`internal/chstore/retention.go:89-106` plan listesinde yoklar), özet MV'ler
  90 gün. "İki hafta önce bu servis kimi çağırıyordu" cevapsız, "iki hafta önce
  gecikmesi neydi" cevaplı.
- **Endpoint (route) grafta düğüm değil.** İki dosya bunu yazılı kabul ediyor:
  `internal/chstore/endpoints_callers.go:20-31` ve
  `endpoints_downstream.go:20-26` — hiçbir MV `(route, caller)` çiftini
  taşımıyor, okuma örneklemli ham `spans` taraması.
- **Dikey eksen yok.** `kube_pod_info` grep → 0 hit; node→pod kenarı hiç
  sorgulanmıyor (`docs/plans/dynatrace-parity-2026-08-21.md:248`). host→servis
  ilişkisi kalıcı değil, `metric_points`'ten `GROUP BY` ile türetiliyor
  (`internal/chstore/hosts.go:97-120`, `service_instances.go:39-70`) ve kodun
  kendisi MV olmadığını koşullu kabul ediyor (`hosts.go:10-15`).
- **Arıza öznesi tek tip.** `Problem` yapısında entity-kind alanı yok
  (`internal/chstore/problem.go:78-125` — `Service`, `Pod`, `Metric` var,
  `kind` yok); `Pod` yalnız runtime kurallarında dolan bir ek açıklama
  (`:104`). DB kapasite problemi topolojik izole: korelatör grafiği
  `node_kind = 'service'` ile filtreli (`internal/chstore/service_adjacency.go:62,
  :122`). Self-health problemleri `Service: ''` ile açılıyor
  (`internal/evaluator/selfhealth.go:653`) — ama bu **bilinçli bir karar** ve
  gerekçesi aynı satırın yorumunda yazılı: "dört kuralda BOŞ (özne
  Coremetry'nin kendisi — sahte bir servis adı uydurmak v0.9.401/402'de
  düzelttiğimiz kırık servis bağlantısını geri getirirdi)". Bu satırı bug
  sanıp "düzeltmek" v0.9.401/402'yi geri kırar.
  ⚠ Bu dokümanın önceki hâli "tek incident'a çöküyor" diyordu; incident
  gruplamasının servis adına göre yapıldığı **doğrulanmadı** → §7'ye
  ölçülmemiş varsayım olarak taşındı.
- ~~**Servisin yaşam döngüsü yok.**~~ **BU MADDE GEÇERSİZ — A2 gemide
  (v0.9.1317).** Doğrulama (çalışma ağacı, 2026-08-24):
  `internal/chstore/store.go:3723-3788` `CREATE MATERIALIZED VIEW … service_seen
  ENGINE = AggregatingMergeTree ORDER BY (service_name)` +
  `minState(time) AS first_seen_state` / `maxState(time) AS last_seen_state`,
  bilinçli olarak TTL'siz ve PARTITION'sız; okuma tarafı
  `internal/chstore/service_seen.go` (+ `ServiceSeenGrace` MV-kapsama dürüstlük
  kapısı, `service_seen_test.go`); `frontend/src/lib/types.ts:34-35`
  `lastSeen?` / `firstSeen?`; `frontend/src/pages/Services.tsx:856-869`
  "Son görülme" kolonu + firstSeen yoksa "bilinmiyor" hâli.
- **Rename varlığı öldürüyor.** `FingerprintException` servis adını hash'e
  katıyor (`internal/chstore/exception_inbox.go:73-78`) → rename'de tüm açık
  exception gruplarının triyaj geçmişi sıfırlanır. Tek alias benzeri mekanizma
  iki dilde elle kopyalanmış bir string listesi
  (`internal/logstore/env_suffix.go:44`, ikizi `podWorkload.ts`).

---

## 4. Entity modeli NEYİ AÇAR / NEYİ AÇMAZ

Sıra memory'de kayıtlı (`project-entity-model-direction.md`): A2+A3+A4 → B1 →
DB instance entity → dikey eksen → kalıcı çağrı grafiği → OTel hizası.

| Adım | Gezinmeye somut katkısı | AÇMADIĞI |
|---|---|---|
| **A2** — `service_seen` MV · ✅ **GEMİDE (v0.9.1317)** | Teslim edilen: `/services`'te "Son görülme" kolonu (`Services.tsx:856-869`) + dürüst "bilinmiyor" hâli. **Kalan gezinme kazancı iki küçük FE kalemi** (aşağıda, Faz 1): (a) "yeni servis / kaybolan servis" filtreleri, (b) ölü servisin `/service` sayfasının dürüst boş hâli. Ayrıca en kalıcı vaka: **⌘K'nın pin/recent listesi hiçbir varlık kontrolü yapmıyor** — `CommandPalette.tsx:280-281` doğrudan localStorage'dan okuyor, `:288-290` `mkSvc` her adı körlemesine `serviceHref(name)`e çeviriyor, pin listesi `recentServices.ts:51-59` kapsız. Emekli olmuş bir servis listede **süresiz** kalır, her ⌘K açılışında görünür, tıklanınca sessizce boş `/service` açar. `lastSeen` artık geldiği için liste canlılıkla süzülebilir (ölü servis soluk + "6 gün önce" etiketi). Bu, A2'nin tek somut **gezinme** (yalnız raporlama değil) kazancı | Rename'i çözmez; ilişkiye dokunmaz; pod/host/DB için yaşam döngüsü vermez |
| **A3** — kanonik kimlik ifadeleri + sıra-pinli testler | **Ç11 ve global `/service-map`'in db düğümü çıkmazının kökü**: bugün aynı DB örneği `dependencies.go:157`, `chstore/topology.go:703`, `service_map.go:460` üçlüsünde üç farklı coalesce ile kimlikleniyor; `service_map.go:460` `"db:"+dbSystem` üretirken MV `"db:"+system+"@"+host` yazıyor → odak tam-eşitlikte (`topology.go:1212`) 0 kenar döner | Kimlik **tablosu** getirmez; URL'ler doğal adda kalır |
| **A4** — `node_kind='service'` filtresinin gevşetilmesi | **Çıktısı bununla SINIRLI: korelatör grafiği db/queue/external düğümlerini de görür** (`service_adjacency.go:62, :122` filtresi kalkar). ⚠ Önceki hâlde yazan "arıza öznesi olur → 'Oracle doldu → 15 servis' tek incident" vaadi **A4'ün içinde değil**: bir DB'nin arıza ÖZNESİ olması için ayrıca `problems` şemasına `kind` kolonu, üretici tarafın (db_capacity vb.) hangi özneyi yazacağı kararı ve tüm okuma/UI hattı gerekir — §3.4'ün entity-kind bulgusuna bağlı, kendi `/spec`'ini hak eden ayrı bir kalem (aşağıda Faz 4b) | Yeni sayfa yaratmaz. ⚠ Düzeltme: **`/queue` diye bir eksiklik yok** — kuyruk düğümünün hedefi zaten bağlı ve gerçek: `components/topology/nodeDetailHref.ts:134-168` `/messaging?msys=&q=&destination=` üretiyor, üçlü tamamlandığında çekmeceyi açan derin link (v0.9.1026, şerh `:145-160`). `/host` (tekil host detayı) gerçekten yok; `/hosts` **listesi vardır** (`App.tsx:152`). Bugün `null` dönen dallar yalnız `nodeDetailHref.ts:128` (çözülemeyen db adı, A3'e bağlı) ve `:170` (external — hedefi HAZIR, bkz. Ç15) |
| **B1** — MV ailesine `deploy_env` boyutu | `env`'in bugün 12 sayfada uygulanıp gerisinde devre dışı olması (`envApplies.test.ts:53-81`) yapısal olarak biter; `env` gerçek bir taşınabilir eksen olur, `?env=` her hedefte anlam taşır | Cluster/servis/filtre taşımasına dokunmaz; `problems` satırlarının env'siz olması (`env_members.go:13-20`) ayrı bir kalem |
| **DB instance entity** (içerik-hash, `db_stmt_hash` deseni) | `/database` sayfası kararlı kimliğe oturur; Ç9'daki üç tablonun aynı satıra aynı adresi vermesi garanti altına alınır | Servis rename'ini çözmez |
| **Dikey eksen** (pod/process + RUNS_ON) | Ç1/Ç3/Ç5'in **hedefini** anlamlı kılar: pod linki bugün de çizilebilir ama pod sayfası "hangi servisin, hangi node'un üstünde" sorusunu ancak bu eksenle cevaplar; "node %90 CPU → üstündeki workload" zinciri açılır | Bugünkü ölü hücreleri **kendiliğinden** linklemez — o iş §5 Faz 1'de |
| **Kalıcı çağrı grafiği** (kenar kimliği + yaşam döngüsü) | "Bu bağımlılık ne zaman doğdu / kayboldu" birinci sınıf soru olur; topoloji değişimi alarm konusu olabilir; 14 gün TTL asimetrisi tartışmaya açılır | Gezinme yüzeyinde tek başına görünmez — bir sayfa/panel gerektirir |
| **OTel hizası** (`Resource.entity_refs`, `internal/` grep → 0 hit) | Kimliği icat etmek yerine üreticiden almak; rename dayanıklılığının tek gerçekçi yolu | Bugünkü hiçbir çıkmazı kapatmaz |

### Entity KATMANI OLMADAN yapılabilecekler (ucuz kazançlar)

Bunların **hiçbiri** A2/A3/A4/B1'e bağlı değil — bugün yapılabilir:

- K1-K7'nin tamamı (kırık linkler, §3.1) — geri linkleri (K7) ve backend link
  üreticileri (K6) dahil.
- Ç1, Ç2, Ç3, Ç5, Ç6, Ç7, Ç8, Ç9, Ç10, Ç12, **Ç13, Ç14, Ç15** — hedef üretici
  depoda hazır.
- `/trace` ve `/logs` için üretici + kaynak-tarama kapıları (`windowRangeParam`
  + `logsRangeParam` üstüne); sekiz el-yapımı `custom:` kopyasının tekleşmesi.
- **Rota yazımı kapısı**: üretilen her yolun `App.tsx`'te kayıtlı bir rotaya
  düştüğünü çiviyen test (K1 sınıfı bir daha gemiye gitmesin) — ayrı kalem,
  bkz. Faz 1.
- `/explore`'un `rememberRange` çağırması; `env` sticky→URL geri-yazım efekti.
- `DrillButton`'ın (`components/DrillButton.tsx:53`) `/endpoint`, `/database`,
  `/pod`, `/incident` detay sayfalarına yayılması.
- A2'nin FE kuyruğu: `/services` "yeni / kaybolan servis" filtreleri, ölü
  servis sayfasının dürüst boş hâli, ⌘K pin/recent listesinin canlılıkla
  süzülmesi.
- `navHref`'in taşıdığı kümenin genişletilmesi (karar gerektirir, §6).

Ç4 ve Ç11 kısmen bağlı: link **çizilebilir** ama hedefin doğru satırı açması
A3'ün kanonik kimlik ifadesine yaslanır.

**Hücre linki ≠ drill çipi — iki AYRI iş.** Hücre linki *bire-bir*: satırdaki
adın kendi sayfasına gider (Ç1-Ç15). Drill çipi *özne sabit, sinyal değişiyor*:
"aynı servis, şimdi trace'lerde / loglarda / problemlerde" (`DrillButton`).
Bugün çip primitifi yalnız `/service` ve `/trace`'te; `/endpoint` kendi üç
çipini elle yeniden yazmış (`pages/EndpointDetail.tsx:177-186`, `<Link
className="sec" style={{…}}>` ×3, v0.9.1210), `/pod` ve `/database`'de hiç yok.
Plan bu ikisini tek başlıkta eritmemeli.

---

## 5. Fazlandırma

Sıra operatörün kuyruk kuralına uyar: **bugs > perf > HA > features > polish**.
Efor tahminleri kaba; hiçbiri ölçülmedi.

### Faz 0 — Bugs (~2,5-3 gün, 7 ayrı `v0.9.X`)

| İş | Dosya | Efor |
|---|---|---|
| K1 `/slow-queries` → `/databases/slow-queries` + testteki yanlış çivinin düzeltilmesi | `pages/slowqueries/stmtParam.ts:60`, `stmtParam.test.ts:110` | 0,5 gün |
| K2 `statementTracesHref`'e zorunlu `window` argümanı (pivotHref deseni) | `features/dependencies/panels/shared.tsx:423-428` + 2 çağrım | 0,5 gün |
| K3 `/shift` üç linkine olay penceresi (`eventLifespanWindow` / `inboxItemWindow`) | `pages/Shift.tsx:155, :183, :211` | 0,25 gün |
| K4 `/service-map` düğüm çekmecesi: ya ikinci-tık yolu geri bağlanır ya vaat + ölü kod kaldırılır (operatör kararı) | `pages/ServiceMap.tsx:107-110, :453` | 0,5 gün |
| K5 `/services` filtrelerinin URL'e yazılması (rebuildPreserving deseni) ⚠ v0.9.1317 ile çakışma riski | `pages/Services.tsx` | 0,5 gün |
| **K6 Go link üreticileri pencereyi taşısın** — `guidedAnswerLinks` ve `toolCallLink` bir `rangeParam` (custom:ms-ms) alsın; öznenin penceresi zaten elde (problem/anomali damgaları). Emsal: `copilot_followup.go:373` bunu zaten yapıyor | `internal/api/copilot_followup.go` (25 site), `internal/api/chat_tool_links.go` (8 site) | 0,5 gün |
| **K7 beş geri/kırıntı linki `range`+`env` taşısın** — emsal `pages/Pod.tsx:212`. `TriageCrumb` prop olarak `search` alsın ya da `navHref(to, search)` üzerinden geçsin | `pages/Service.tsx:399` · `pages/EndpointDetail.tsx:137` · `pages/DatabaseDetail.tsx:176` · `pages/SlowQueries.tsx:201` · `components/TriageCrumb.tsx:12` | 0,25 gün |

**Kapı (K7 ile aynı dilimde, `serviceHref.test.ts:303` deseninde):** "detay
sayfasındaki liste-geri linki paramsız olamaz" — kaynak taraması, muafiyetler
gerekçeye anahtarlı.

**Entity bağımlılığı:** yok.

### Faz 1 — Kapılar + ölü hücreler + A2'nin FE kuyruğu (~3-3,5 gün)

1. **`traceHref` + `logsHref` üreticileri** (`lib/`) — sıfırdan değil,
   `lib/urlState.ts:28` `windowRangeParam` + `lib/logsUrl.ts:89` `logsRangeParam`
   üstüne ince bir katman. **39** el-yapımı `/trace?id=` sitesi (24 dosya) +
   **13** `/logs?` sitesi + **8** el-yapımı `custom:` kopyası dönüştürülür.
2. **İki yeni kaynak-tarama kapısı** (`/trace?id=`, `/logs?`) — sıfırdan desen
   kurma: `frontend/src/pages/spaLinks.test.ts`'in **`walk()` + gerekçeye
   anahtarlı `ALLOWED`** iskeletini yeniden kullan (v0.9.914; `:33-36` şerhi
   "satıra bağlı muafiyet import eklenince kayar" dersini taşıyor). İkinci
   emsal `frontend/src/pages/brokenAffordances.test.ts` (v0.9.869, koşullu
   kural + yorum-boşaltma ile yanlış-pozitif eleme). `serviceHref.test.ts:299-342`
   tek-dize deseni bunların yanında dar kalıyor.
3. **Ölü hücrelerin linklenmesi:** Ç1, Ç2, Ç3, Ç5, Ç6, Ç7, Ç9, Ç10, Ç12,
   **Ç13, Ç14, Ç15**. Sıra önerisi: **Ç13** (en ucuz — kardeşi
   `Incident.tsx:300`'de çözülmüş, üretici + pencere fonksiyonu hazır) →
   Ç2 (kök-neden, en yüksek anlık değer) → Ç1 → Ç3 → **Ç14** (trace ailesi tek
   dilim) → Ç5+Ç6 → Ç7+Ç9+Ç10 → **Ç15** → Ç12.
   **Her Ç kalemi üç soruyu yanıtlayarak kapanır:**
   - *git mi, daralt mı?* (§3.2 başlığı; log/bubble-up satırlarında "daralt"
     çoğu zaman doğru cevap, ikisi yan yana da durabilir)
   - *satır tıklanabilir mi?* → evetse `stopPropagation` zorunlu
     (`LogTable.tsx:483` / `Hosts.tsx:118` emsalleri)
   - *bu kolon dar ekranda `mobileHide` mı?* — **mekanizmanın ilk tüketicisi bu
     dalga olsun** (`lib/dataTable.ts:87`). `DataTable.tsx:49-50`'nin uyarısı da
     uygulanır: kolonu işaretleyen sayfa **gövde `<td>`'sini de** atlamalı,
     sonra `dataTableMobileHide.test.ts:141` izin listesine eklenir.
4. **DrillButton'ın yayılması** (hücre linkinden AYRI iş): `/endpoint`,
   `/database`, `/pod`, `/incident` detay sayfalarına çip satırı;
   `pages/EndpointDetail.tsx:177-186`'daki üç el-yapımı çip `DrillButton`'a
   indirilir. ~0,5 gün.
5. **A2'nin FE kuyruğu** (MV işi YOK, saf FE, ~0,25 gün): `/services`'te "yeni
   servis / kaybolan servis" filtreleri (`lastSeen`/`firstSeen` artık geliyor);
   ölü servisin `/service` sayfasının dürüst boş hâli ("6 gündür telemetri yok"
   ≠ "pencere yanlış"); ⌘K pin/recent listesinin canlılıkla süzülmesi
   (`CommandPalette.tsx:280-290`).
6. **Trace kıyaslama affordance'ı** (~0,1 gün): `/trace` başlığında düğme
   **zaten var** (`pages/Trace.tsx:395` `<Link to={\`/trace/compare?a=…\`}>`);
   eksik olan `/traces` **listesinde** satır menüsü "kıyasla →". Hedef URL
   tabanlı (`TraceCompare.tsx:49` `useSearchParams`), iş tek düğme.

⚠ **Gizli sayfa kapısı — Ç10 (`Profiling.tsx:179`), Ç11
(`pages/External.tsx:207`), Ç12 (`components/ExternalPaths.tsx:86-92`) ve Ç15
için ÖNCE bir karar:** `/hosts`, `/external`, `/profiling`, `/monitors`
sidebar'dan **gizli** (`components/Sidebar.tsx:64`: "profiling / monitors /
external / hosts — hidden from nav, code alive", v0.8.489/490). Rotaları
yaşıyor (`App.tsx:151, :152, :168, :170`) ve ⌘K'da görünüyorlar
(`CommandPalette.tsx:76, :77, :82, :91`), ama sidebar'da yoklar. Bu, kalemlerin
değerini iki yönde de değiştirir: ya boşuna cila, ya da **gelen link o sayfanın
pratikte tek erişim yolu** — o zaman geri linki de aynı dilimde gider.

**Entity bağımlılığı:** yok. **Ölçülmedi:** her hedefin gerçekten dolu bir
sayfa açtığı — yalnız üreticinin imzasıyla satırın alanlarının uyuştuğu
doğrulandı.

### Faz 1b — Rota-varlığı kapısı (ayrı kalem, 0,5-1 gün)

Faz 1'in içine gömülmemeli: paketin **en pahalı** kalemi. Mevcut kapı deseni
(`serviceHref.test.ts:299-342`) tek bir SABİT dize arayıp yorum ayıklıyor.
"Üretilen her yolun `App.tsx`'te kayıtlı bir rotaya düştüğü" testi ise
şablon-literal yolları (`/logs?service=${…}`) **statik olarak çözmeyi** ve JSX
rota listesini ayrıştırmayı gerektiriyor — nitel olarak farklı bir iş. Memory
`feedback-gate-single-spelling` tam bu sınıfı işaretliyor (grep-kapısı ikinci
yazımı muaf tutar).

**Kapsamı baştan darat:** yalnız **sabit önek** yazan siteleri yakalayan bir
kapı (`'/slow-queries'`, `'/topology'` gibi) K1 sınıfını kapatmaya yeter ve
statik çözümleme gerektirmez. Şablon-literal çözümü ayrı bir turda.

### Faz 2 — Bağlam taşıma (~0,75 gün + 1 karar)

Tabloda göründüğünden **küçük** — iki kalem de tek çağrım/tek efekt.

1. `/explore` `rememberRange` çağırsın (göreli preset kaybı biter) —
   `pages/Explore.tsx:723` `onRangeChange`. **Tek çağrım.** Aynı dilimde
   `useUrlRange.ts:67-69`'daki bayat "(Explore, Metrics)" atfı düzeltilsin:
   `pages/Metrics.tsx:86` artık `useUrlRange` kullanıyor. ~0,25 gün.
2. `env` sticky→URL **mount geri-yazım efekti** (`useUrlEnv.ts`) —
   `useUrlRange.ts:159-165` deseninin birebir ikizi (tek `useEffect`, yabancı
   paramları `window.location.search`ten tohumlayan biçimiyle). Seçim zaten
   yazılıyor (`useUrlEnv.ts:66-81`), eksik olan yalnız restore dalı. ~0,1 gün.
3. **`compare` ekseni** `env` ile birlikte ele alınsın: `/service`'in
   localStorage'daki `svc.charts.compare`'ı (`ServiceCharts.tsx:148`,
   `storage.ts:60`) URL'e taşınsın, `/endpoints`'in emsaline hizalansın
   (`endpointParam.ts:43, :71, :99`). ~0,25 gün.
4. **Karar:** `navHref` `cluster`i de taşısın mı? Bugün yalnız `range`+`env`
   (`navHref.ts:42-43`). Karşı-argüman: cluster her sayfada aynı anlama
   gelmiyor ve `envApplies` gibi bir "hangi sayfa uyguluyor" kapısı gerekir.
   **Emsal zaten elde:** `pages/endpoints/endpointParam.ts` cluster'ı taşıyor
   ve `endpointPage.test.ts:13` bunu testle çiviliyor — karar bu emsale
   yaslanmalı.
5. `navHref.ts:10-12`'deki **bayat gerekçe yorumu** düzeltilsin (v0.9.937 ile
   çelişiyor: `useUrlRange.ts:175-180`) — davranış bug'ı değil, karar dayanağı
   kayması.

**Şart (kırmızı çizgi 7'nin uygulanışı):** URL'e yeni bir eksen yazan her dilim
`lib/chatContext.ts:38-50` `SERVICE_PARAM_ROUTES`'u da güncellemek zorunda,
yoksa AI sohbeti o rotada **sessizce kör** açılır (kapısı yok, tsc göremez).
Aynı şekilde `rebuildPreserving` / env geri-yazımı `?ai=` eksenini
(`lib/aiSubject.ts:20-24`) **ezmemeli**. Tercih edilen çözüm: kaydı tek yere
(`navHref` ailesi) taşımak.

**Karar kalemi — hangi Ç rota değiştirsin, hangisi `?param=` çekmecesi olsun?**
Faz 1 çıktısı liste durumunu (scroll + facet + seçim) **kaybeder**: `<ScrollRestoration>`
depoda yok. Deponun kendi çözümü aynı rotada param
(`pages/Inbox.tsx:232-234, :561`, v0.9.837; emsaller `?item=` / `?problem=` /
`?destination=` / `?endpoint=`). En azından hangi Ç'lerin bu bedeli ödediği
yazılmalı; ideali, yoğun triyaj yüzeylerinde (Ç2, Ç3, Ç13) rota yerine
çekmece seçmek.

**Entity bağımlılığı:** yok.

### Faz 3 — ~~A2: yaşam döngüsü~~ ✅ GEMİDE (v0.9.1317)

**Bu faz KALDIRILDI.** `service_seen` MV + okuma tarafı + "Son görülme" kolonu
çalışma ağacında (kanıt §3.4). Kalan iki FE kalemi **Faz 1 madde 5**'e taşındı
(~0,25 gün, MV işi yok). Fazın numarası bilerek boş bırakıldı ki aşağıdaki
atıflar kaymasın.

### Faz 4 — A3 + A4: kanonik anahtar + tipleme (~3,5-5 gün, ⚠ CH geçişi dahil)

**Kapı:** bu faz `/clickhouse-schema` skill'i OKUNMADAN başlamaz (CLAUDE.md
zorunlu kapısı: "BEFORE any CH table/query/MV change"), ve her yeni
kolon/ifade **gün-bir dağıtık-güvenli** olmak zorunda (memory
`feedback-distributed-column-safety` — bu sınıf prod'u iki kez kırdı:
v0.8.185 cluster, v0.8.186 op_group).

- A3 çıktısı doğrudan iki çıkmazı kapatır: global `/service-map`'in db düğümü
  (üretici/tüketici uyumsuzluğu) ve Ç11'in `peer.service`-only pivotu.
- **Gizli CH maliyeti (önceki tahminde yoktu).** Kimliği yazan taraf
  agregatör'ün `SELECT`'i: `internal/chstore/topology.go:742-745`
  `multiIf(db_system != '' AND infra_host != '', concat('db:', db_system, '@',
  infra_host), db_system != '', concat('db:', db_system), …)`. Tüketici tarafta
  ikinci yazım `internal/chstore/service_map.go:460` `depName := "db:" + sp.dbSystem`,
  üçüncüsü `internal/chstore/dependencies.go:157` `dbInstanceExpr`. Yazıcı
  ifadesini değiştirmek **mevcut satırları eski yazımda bırakır** ve
  `topology_edges_5m` TTL'i 14 gün (`internal/chstore/store.go:1954`
  `TTL toDate(time_bucket) + INTERVAL 14 DAY`) → **en az iki haftalık
  karışık-kimlik okuma penceresi** doğar.
  **Açık geçiş adımı (üçünden biri seçilir):**
  (a) yeni ifadeyi yazıcıya al → okuma tarafını geçici olarak **iki yazımı da**
  kabul edecek şekilde genişlet → 14 gün sonra eski dalı kaldır;
  (b) tabloyu yeniden kur (14 günlük kenar geçmişi feda, maliyeti açıkça
  yazılır);
  (c) çift-kolon (eski + yeni kimlik) ve okuma yeniyi tercih eder.
  Tahmindeki artışın tamamı bu adımdan geliyor.
- A4 sonrası korelatör grafiği db/queue/external düğümlerini de görür.
  `nodeDetailHref`'in bugün `null` dönen **tek gerçek** dalı `:128` (db adı
  çözümlemesi) ve o da A3'ün kanonik ifadesine yaslanıyor — `:170` external
  dalı Faz 1'e taşındı (Ç15, hedefi hazır).

### Faz 4b — Arıza öznesi tipi (ayrı `/spec`, tahmin edilmedi)

A4'ten **ayrı**: bir DB/kuyruk/dış bağımlılığın *arıza öznesi* olması
(`problems` şemasına `kind` kolonu + üretici tarafın hangi özneyi yazacağı
kararı + tüm okuma/UI hattı). §3.4'ün entity-kind bulgusuna bağlı. "Oracle
doldu → onu kullanan 15 servis tek incident" vaadi buranın çıktısıdır, A4'ün
değil. `/clickhouse-schema` + dağıtık-güvenlik kapıları burada da geçerli.

### ~~Faz 5 — B1: `deploy_env` boyutu~~ ⚠ DÜŞÜRÜLDÜ (2026-08-24)

**Bu faz KOŞULMAYACAK.** Aşağıdaki "emsal kararlar" bölümü zaten gerginliği
işaret ediyordu; operatöre sunulduğunda gerginlik gerginlik olmaktan çıkıp
karar oldu: **prod tek-env** (*"prod'ta herkes prod env zaten"*, 2026-07-18)
ve **G6'daki MV-cerrahisi reddi** birlikte B1'in değer önermesini
çürütüyor. Gerekçenin tamamı: `docs/audit/entity-model-audit-2026-08-23.md`
§7.3. İhtiyaç doğarsa yol ham-spans (`env_members.go`, v0.9.1041 emsali) —
hafta değil gün.

Metin aşağıda **arşiv olarak** duruyor; kuyruğa alma.



Denetimin en pahalı dört tutarsızlığının kökü. Gezinme açısından tek cümlelik
faydası: `env` gerçek bir global eksen olur, "taşınıyor ama uygulanmıyor"
yarım hâli biter. `/clickhouse-schema` + dağıtık-güvenlik kapıları şart.

**Emsal kararlar — operatöre B1 sunulurken birlikte gösterilmeli:**
- Memory `project-ux-audit-execution`: **"G6 `/database` instance = OPERATÖR
  'bugünkü kapsamla yaşa' (MV cerrahisi = 90g statement geçmişi feda,
  reddedildi)"**. B1 tam da o aileye `deploy_env` eklemek, yani operatörün bir
  kez reddettiği maliyet sınıfı.
- Aynı memory: **"env(a) Service = DONE v0.9.1039-1041 … ham-spans yolu, MV
  cerrahisi YOK; DatabaseDetail bilinçli hariç — o MV'lerde deploy_env yok"**.
  Yani env'i MV'ye dokunmadan uygulayan bir emsal ZATEN var.
- Memory `project-flowgraph-preview`: bir env dilimi **"prod tek-env, değer
  yok"** gerekçesiyle iptal edilmişti — B1'in değer önermesiyle gerginlik
  içinde.

**Alternatif (sunulmalı):** env'i MV boyutu yerine ham-spans / servis-üyeliği
yoluyla uygulamak (`internal/chstore/env_members.go` deseni). Maliyeti hafta
değil **gün**; karşılığı, ağır agregat sayfalarında env'in yaklaşık kalması.

### Faz 6+ — Entity sırası

DB instance entity → dikey eksen (pod/host + RUNS_ON) → kalıcı çağrı grafiği →
OTel `entity_refs`. Her biri kendi `/spec`'ini hak ediyor; bu doküman yalnız
gezinme kancalarını işaretliyor.

### Polish (en sona)

- Kolon seçimi kalıcılığının dört sayfada tekleştirilmesi.
- `/services`'e `SavedViewsBar` (bugün onsuz tek büyük liste sayfası).
- `FocusedNeighborhood.tsx:486` altyazısı ("Click a node to recenter") artık
  yanlış — tık pin'liyor.
- **`components/charts/AnnotationLane.tsx:60-67`** (dosya `components/` kökünde
  DEĞİL) `targetHref` switch'i yalnız `problem|anomaly|event` işliyor, oysa tip
  birleşimi dört değer taşıyor: `lib/types.ts:5172` `targetType?:
  'problem'|'anomaly'|'event'|'rollout'` — `rollout` `default: return null`'a
  düşüyor. Hedef belirsiz, önce karar.
- **Kıyaslama ekseni — kısmi.** `/trace` sayfasında "compare" düğmesi ZATEN var
  (`pages/Trace.tsx:395`, v0.4.96'da accent'e terfi ettirilmiş) ve
  `pages/TraceCompare.tsx:124` ikinci id'yi ekliyor. Eksik olan yalnız
  `/traces` **listesinden** "bununla kıyasla" affordance'ı; ⌘K girişi de var
  (`components/CommandPalette.tsx:83`). Faz 1 madde 6'ya alındı.
- `?insight=exception:` kodeği var, açan host yok (`components/ai/insightRow.tsx:87-92`
  vs üç `useInsightRow` çağrımı) — bilinçli bekleme mi düşmüş dilim mi
  ölçülmedi.

---

## 6. Kırmızı çizgiler

Denetim §7.4'ün eklemeli-olma kuralları bu katmanda da aynen geçerli:

1. **`entity_id` hiçbir ORDER BY'a, hiçbir shard anahtarına, hiçbir önbellek
   anahtarına girmez.** `service_name` bugün 18 tablonun shard anahtarı ve
   `optimize_skip_unused_shards=1` buna yaslanıyor; önbellek tarafında v0.5.187
   çapraz-zehirlenme sınıfı hâlâ geçerli (`internal/api/cache.go`).
2. **Doğal anahtar her yerde birincil kalır.** URL'ler `entity_id`'ye
   GEÇMEZ: `/service?name=<service_name>` aynen kalır. Kırılırsa bookmark'lar,
   dashboard linkleri ve incident kanalına atılmış adresler ölür.
   ⚠ **Garanti bugün YARIM:** frontend'de 56 `serviceHref` çağrımı + **0**
   el-yapımı link (kapı `serviceHref.test.ts:303`, allowlist boş), ama
   **backend'de 33** el-yapımı in-app href var ve hiçbir kapı görmüyor
   (§3.1 K6). İkisi de aynı sözleşmeye bağlanmalı — aksi hâlde bir kimlik
   değişimi FE'de tip hatası verirken Go'da sessizce yanlış URL üretir.
   Faz 1'in dördüncü kapısı bunu çiviler: `internal/api`'de üretilen her
   ürün-yolunun FE rota listesiyle eşleştiğini doğrulayan bir Go testi —
   emsal `internal/api/copilot_followup_links_test.go:23` zaten dize arıyor.
3. **Çözümlenemeyen dal bugünkü davranışa düşer** ve testle çivilenir.
   `nodeDetailHref.ts:108-114` (`null` dön, link çizme) ve `databaseParam.ts:76`
   ('default' sentinel'i yazma) bu disiplinin mevcut emsalleri. Entity
   çözümlemesi **hiçbir zaman satır düşürmez, hiçbir zaman yanlış varlığa
   yazmaz**.
4. **URL = shareable view'ın tek kaynağı.** Yeni taşınan her eksen URL'e
   yazılmalı (yoksa paylaşılan link yalan söyler — bugünkü `env` durumu) ama
   `replace: true` ile ve `rebuildPreserving` (`lib/urlState.ts:164-177`)
   sözleşmesine uyarak: efekt yalnız kendi anahtarlarına sahiptir.
5. **Yaşam döngüsü MV'den okunur, tabloda tutulmaz** (`minState` emsali) — tik
   başına satır yeniden yazımı yok.
6. **Göreli preset URL'e çivilenmez.** `navHref`'in yalnız `custom:` taşıma
   kararının orijinal gerekçesi (paylaşılan link donar) hâlâ geçerli; yorumu
   bayat olan kısım v0.9.937 önermesi, kararın kendisi değil.
7. **Yeni ikinci kaynak-hakikat yaratılmaz.** Bugün servis→servis ilişkisi
   zaten üç temsilde (MV kenarları, örnekli iz yürüyüşü `service_map.go:302-360`,
   `span_links`) — dördüncüsü eklenmez, tersine birleştirme hedeflenir.
   ⚠ **Frontend'de zaten ikinci bir kayıt var ve kapısı yok:**
   `frontend/src/lib/chatContext.ts:38-50` `SERVICE_PARAM_ROUTES` sabit kümesi
   (`/traces, /endpoints, /logs, /inbox, /deploys, /metrics, /explore,
   /clusters, /profiling`) + `serviceFromRoute` `/service`,
   `/service/backtrace`, `/pod` için elle dallanıyor. Bu küme AI sohbetinin
   varsayılan servis kapsamını belirliyor (dosya şerhi: "bugüne dek chat bu
   rotalarda kör açılıyordu"). Yeni bir sayfa `?service=` kabul ettiğinde elle
   güncellenmezse sohbet **sessizce kör** açılır. İkinci eksen: `?ai=`
   (`lib/aiSubject.ts:20-24` `AI_KINDS`) — `rebuildPreserving` / env
   geri-yazımı onu ezmemeli. Hedef: kaydı `navHref` ailesine taşımak.
8. **Müşteri/banka adı repoda geçmez.**

---

## 7. Ölçülmemiş varsayımlar

Beş merceğin `unmeasured` alanlarının birleşimi. Bu dokümandaki hiçbir iddia
canlı sistemde doğrulanmadı.

- **Hiçbir sorgu çalıştırılmadı.** Canlı ClickHouse'a bakılmadı; satır
  sayıları, gerçek TTL uygulanması, MV doluluğu, kenar sayıları, `instance`
  kardinalitesi ölçülmedi. Tüm veri-modeli iddiaları DDL metni ve Go kaynağı
  üzerinden.
- **Hiçbir sayfa tarayıcıda açılmadı** (Playwright kullanılmadı). "Bu link ana
  sayfaya atıyor" / "0 satır döner" iddiaları üretici ile tüketicinin kod
  karşılaştırmasıdır. K1 `App.tsx` rota listesiyle doğrulandı, runtime'da
  görülmedi.
- **Eşzamanlı v0.9.1317 gönderimi (2026-08-24 revizyonunda ÇÖZÜLDÜ):**
  `internal/chstore/store.go`, `summary.go`, `frontend/src/lib/types.ts`,
  `lib/api.ts`, `pages/Services.tsx` başka bir ajan tarafından düzenleniyordu.
  Revizyonda çalışma ağacı yeniden okundu: **A2 gemide** (§3.4 kanıtı), §3.4'ün
  yokluk maddesi silindi, Faz 3 kaldırıldı, `types.ts` satır numaraları
  yenilendi (`Service.lastSeen/firstSeen` `:34-35`; `spanName` `2955`→`2968`;
  `targetType` `5159`→`5172`). Bu dosyalar için verilen satır numaraları hâlâ
  o anki ağaca aittir; alıntı metinleri sabit kalmalı.
- **Global `/service-map` db düğümü uyumsuzluğu:** `infra_host` boşsa MV de düz
  `db:<system>` yazıyor (`chstore/topology.go:745`) ve o hâlde odak eşleşir.
  Hangi kurulumda `infra_host`'un dolu olduğu **bakılmadı** — çıkmaz
  adlandırılabilen (sağlıklı) kurulumda ortaya çıkar.
- ~~**Backend'in ürettiği linkler** açılmadı~~ — **2026-08-24'te ÖLÇÜLDÜ**,
  §3.1 K6'ya taşındı: 33 el-yapımı in-app href, `range=` yazan tek site
  `copilot_followup.go:373`. ⚠ Önceki hâlde atıf verilen
  `internal/api/links.go` diye bir dosya **yok**; gerçek dosyalar
  `chat_tool_links.go`, `copilot_followup.go`, `correlation_link.go`,
  `request_id_links.go`. Son ikisi **dış-sistem** korelasyon linkleri
  (operatör şablonlu, `buildCorrelationLink`) — in-app ürün yolu üretmiyorlar,
  ayrı sınıf. FE href'i aynen çiziyor (`lib/insightCard.ts:41`).
- **Incident gruplamasının servis adına göre yapıldığı** doğrulanmadı — §3.4'ün
  eski "self-health problemleri tek incident'a çöküyor" cümlesi bu varsayıma
  dayanıyordu; cümle kaldırıldı, iddia buraya taşındı. `Service: ''`
  atamasının kendisi doğrulandı ve **bilinçli** (`selfhealth.go:653` yorumu,
  v0.9.401/402).
- **Dış-sistem köprüleri** okunmadı: `lib/kibanaLink.ts`,
  `components/ExternalPaths.tsx` param sözleşmesi, `lib/shareUrl.ts` ve
  `lib/dashboardUrl.ts` içerikleri.
- **Taranmayan sayfalar:** `/explore`, `/metrics`, `/dashboards`, `/alerts`,
  `/slos`, `/runbooks`, `/monitors`, `/watchers`, `/clusters` (yalnız grep),
  tüm `/system` ve `/admin` yüzeyleri. **Revizyonda tarandı ve listeden
  çıkarıldı:** `/incidents` (→ Ç13), `/trace` + `/traces` trace ailesi (→ Ç14,
  ayrıca `Trace.tsx:395` compare düğmesi bulundu), `/profiling` (gizli-sayfa
  notu). Klavye erişilebilirliği ölçülmedi; **dar ekran** artık ölçüldü
  (mekanizma kurulu / tüketici yok — §3.3 son satır).
- **`internal/chstore` altında ~250 dosyanın ~35'i açıldı.** İlişki taşıyabilecek
  okunmayanlar: `bubbleup.go`, `business_dims.go`, `heatmap.go`,
  `infra_metrics.go`, `runtime_pods.go`, `external_catalogue.go`,
  `failure_slo.go`. `migrations/*.sql` ve `internal/chmigrate/` hiç açılmadı —
  rollup ailelerinin ilişki taşıyıp taşımadığı ölçülmedi.
- **ES yolunda trace korelasyonu paritesi** bakılmadı
  (`internal/logstore/elasticsearch*.go`); prod'da birincil logstore ES.
- **`Resource.entity_refs`** alanının vendor'daki proto sürümünde gerçekten
  bulunduğu doğrulanmadı — yalnız `internal/` içinde 0 referans olduğu
  doğrulandı.
- **Problem üretici ailelerinin tamamı taranmadı** — yalnız `db_capacity.go` ve
  `selfhealth.go`'nun `Service:` atamalarına bakıldı.
- **Efor tahminleri ölçüm değil.** Faz 0-2 dosya sayısından, Faz 4-5 denetimin
  kendi tahminlerinden alındı. **Tahminlerin ATLADIĞI maliyet kalemleri**
  (2026-08-24'te eklendi, hâlâ ölçülmedi):
  - CH tarafında **çift-okuma penceresi ya da tablo yeniden kurulumu**
    (Faz 4, A3 — `topology_edges_5m` 14 günlük TTL yüzünden zorunlu);
  - **`/clickhouse-schema` + dağıtık-güvenlik gözden geçirme kapıları**
    (Faz 4, 4b, 5) — CLAUDE.md zorunlu, ama tahminlerin hiçbirinde yok;
  - `make audit` + `go test ./...` + `npx tsc --noEmit` **release kapıları
    dilim başına** (Faz 0'da 7 ayrı `v0.9.X` = 7 tam tur);
  - Faz 1'de her Ç kalemine eklenen üç soru (git/daralt, stopPropagation,
    `mobileHide`) — kalem başına küçük, on kalemde toplamı küçük değil.
- **Ölçüm komutları dokümana yazıldı** (§3.2 sayım tablosu). Bir sonraki
  ajan **üçüncü bir rakam üretmeden önce** aynı süzgeci kullanmalı: ham vs
  test-hariç vs test+yorum-hariç arasındaki fark `/trace?id=` için 52 → 48 →
  39. Önceki iki tur bu farkı yazmadığı için iki farklı sayı gemiye gitti.
- **Denetimin kendi açık soruları** (E2 çelişkileri, E3 shard'lanmamış state
  tabloları) bu dokümanda ele alınmadı.

---

## 8. Reddedilen eleştiriler (2026-08-24 turu)

Eleştirmen de yanılabilir. Aşağıdakiler **koddan doğrulandı ve reddedildi**;
gelecek turlar bunları yeniden açmadan önce buradaki kanıta baksın.

| # | İddia | Neden reddedildi (kanıt) |
|---|---|---|
| R1 | "§4 A4 satırı `/hosts` rotası yok diyor; `/hosts` VAR" | Doküman `/hosts` demiyor, **`/host`** (tekil detay) diyor — ve `/host` gerçekten yok. `App.tsx:152` `/hosts` **listesi** var, `App.tsx`'te `"/host"` yok. Satır zaten doğruydu; yine de netlik için "(`/hosts` listesi vardır)" şerhi eklendi. Eleştirinin **ikinci yarısı** (gizli sayfalar) ise haklıydı ve işlendi |
| R2 | "`/trace/compare`'ın TEK giriş noktası ⌘K; grep başka .tsx'te geçmiyor" | Yanlış. `pages/Trace.tsx:395` `<Link to={\`/trace/compare?a=${encodeURIComponent(id)}\`}>` — v0.4.96'da "operatörler 'iki trace'i nasıl diff'lerim' diye sormaya devam ettiği için" accent'e terfi ettirilmiş bir düğme. `pages/TraceCompare.tsx:124` de ikinci id'yi ekliyor. Gerçek boşluk yalnız `/traces` **listesinde**; kalem o kapsamda Faz 1 madde 6'ya alındı |
| R3 | "`/trace?id=` sayısı 48 değil **47** olmalı" | Ölçüm ikisini de tutmuyor. `grep -rn "/trace?id=" frontend/src \| grep -v node_modules`: ham **52**, test hariç **48** (dokümanın eski rakamı — doğru ama yorumları sayıyor), test+yorum hariç **39 / 24 dosya**. Doğru iş kalemi **39**; sayım tablosu ve komut §3.2'ye yazıldı ki üçüncü bir rakam doğmasın |
| R4 | "`destinationParam`'ın tek tüketicisi `pages/Messaging.tsx:13`" | İkinci tüketici var: `components/topology/nodeDetailHref.ts:62` `import { encodeDestinationParam }`, kullanım `:163`. Bu, R1'in ikizi bir noktayı da çürütüyor: kuyruk düğümü ZATEN `/messaging`'e derin linkli (`:134-168`, v0.9.1026). Eleştirinin **envanter yarısı** (§2'ye eklensin, Ç4'ün hazır hedeflerine messaging yazılsın) haklıydı ve işlendi |
| R5 | "Ç8 '5 yer' diyor, dört atıf veriyor → '4 yer' yap" | Ters yönde yanlış. Ölçüm: **en az 11 gösterim / 9 dosya** (§3.2 Ç8'de tam liste). Sayı **yukarı** düzeltildi |
| R6 | "Backend'de 'en az 12' el-yapımı link var" | Eksik. Ölçüm **33** (25 + 8, komut §3.1 K6'da). Sayı yukarı düzeltildi; bulgunun kendisi kabul edildi |

**Not — bir eleştiri bulgu değil, olumlu doğrulamaydı** ve kaydedilmeye değer:
plan CLAUDE.md'nin hiçbir sert kısıtını ihlal etmiyor (MV-önce okuma,
picker/sanallaştırma/poll≥10sn yüzeylerine dokunmuyor, `saved_views` dışında
şema önermiyor), §6.1 önbellek anahtarına `entity_id` girmesini yasaklıyor
(v0.5.187 sınıfı) ve §6.4 `replace: true` + `rebuildPreserving` sözleşmesini
şart koşuyor (`lib/urlState.ts:164-179` doğrulandı, yabancı param koruma
dahil). Reddedilmiş tasarımlardan hiçbiri geri gelmiyor: kart akışı, Servis
Overview Tek-Bakış, yüzen şeritler, çok-kiracılık, PII maskeleme, in-binary
sampling, Inbox Option B.
