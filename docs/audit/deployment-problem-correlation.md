# Audit — Problem ↔ Deployment/Rollout korelasyonu

**Tarih:** 2026-09-02 · **Durum:** ONAY BEKLİYOR — kod değişikliği yok
**Hedef:** bir Problem açıldığında etkilenen service/namespace/pod'larda son
N dakikada rollout olup olmadığını tespit edip kanıta *"olası neden:
<deployment> rollout'u, <zaman>"* satırı eklemek; Problem detayında
"Related deployments" bloğu, rollout feed'inde "caused N problems" işareti.
**Keşif:** salt-okunur Explore ajanı (53 okuma) + kritik iddialar elle
doğrulandı (`RolloutServices`, `NodeKindNode` emsali, `RolloutDrawer:73`,
`EnrichProblemsWithDeploys`, `maxEvidenceTypes=4`, `root_cause_hypotheses`
kolonları).

---

## 0. Özet — beş karar

| # | Karar | Gerekçe |
|---|---|---|
| **K1** | **İki deploy modeli var; korelasyon YENİ `workload_rollouts` katmanına (v0.10.197-215) bağlanır**, eski `service.version` çıkarımı (`RecentDeploy`) yerinde kalır ve "hangisi varsa" birleştirilir | Eski model servis-kapsamlı ve versiyon-string'inden çıkarım (`effectiveVersionExpr`); yeni model ReplicaSet/revizyon kimlikli, namespace+cluster+workload taşıyor, KSM ile doğrulanıyor (`detected_by = spans+ksm`). Operatörün istediği "deployment adı + zaman" yalnız yenisinde var. |
| **K2** | **Eşleme yönü: Problem → servis → `workload_revision_activity_1m` (service_name boyutu) → (cluster, namespace, workload, revision) → `workload_rollouts FINAL`** — `RolloutServices`'in TERSİ, bugün YOK | Problem `namespace`/`pod` taşımıyor (`Problem.Pod` yalnız runtime pod denetimlerinde dolu); MV'nin `service_name` boyutu köprü. Pod adı geldiğinde (Influx `k8s.pod.name` / runtime problem) `k8s_replicaset` terfi kolonu doğrudan revizyon verir; `podTemplateHash` string-parse yedek. |
| **K3** | **Skor = güncellik bandı × kanıt gücü:** 0-30 dk → 0.9 (yüksek), 30-120 dk → 0.5 (düşük), >120 dk → yok; `status ∈ {completed, in_progress, stalled, rolled_back}` gücü ±; KSM doğrulamalı (`spans+ksm`) +0.05; birden çok rollout → en yakın kazanır, diğerleri listede kalır | Operatör pencere/skor bandını verdi; mevcut deploy tier'ı `0.80 + 0.15×freshness` (hypothesis.go:46-60) ile uyumlu bant. |
| **K4** | **Correlator'a rollout, `ScoredCause{Kind:"rollout"}` adayı olarak girer** (`RankNodeCauses` / `NodeKindNode` emsali, propagation.go:197-245) — weighted edge grafiğine düğüm EKLENMEZ | Edge modeli servis→servis çağrı istatistiği (`EdgeStat{Calls,Errors}`); rollout bir çağrı kenarı değil, bir "değişiklik" adayı. Node co-tenancy tam bu şekilde eklenmiş (v0.9.1056). ⚠ `maxEvidenceTypes=4` paydası: yeni kanal eklemek TÜM güven skorlarını düşürür → rollout, deploy KANALININ İÇİNDE sayılır (payda değişmez). |
| **K5** | **Kanıt depolama:** `RootCauseHypothesis.RecentDeploy` genişletilmez; `DeepEvidence`'a `Rollouts []RolloutEvidence` (+ `Candidates`'a `Kind:"rollout"` adayı). Rollout feed rozeti için `workload_rollouts`'a kolon YOK — `RolloutList` sonucuna okuma anında `problemsCausedCount` (`filterOpenProblemsSince` hoist) | Problem satırı değişmez (tam-satır replace invariant #4); hypothesis tablosu zaten JSON blob, 30 g TTL; feed rozeti `RolloutDrawer:73`'teki sayımın liste seviyesine taşınması — yeni şema yok. |

---

## 1. Rollout olay modeli — şema

`workload_rollouts` (`migrations/0012_rollout_layer.sql:124-149`, app-managed
kurulumda `store.go`; dış Distributed prod'da 0012 sihirbazı):

| Kolon | Anlam | Kaynak |
|---|---|---|
| `cluster_id` | Remote Cluster **registry EffectiveID** (`c-…`), span cluster değeri DEĞİL | `MapClusters` (reconciler.go:268) |
| `namespace`, `workload`, `workload_kind` (`Deployment/StatefulSet/DaemonSet`) | `k8s_deployment/k8s_statefulset/k8s_daemonset` terfi kolonlarından (`multiIf`, MV 0012:171-203) | span resource attr |
| `revision` | `k8s_replicaset` (Deployment) — STS/DS için `container_image_tag` vekili (v0.10.211) | span resource attr |
| `started_at DateTime64(3)`, `first_span_at`, `traffic_confirmed_at`, `ksm_started_at`, `pods_ready_at`, `completed_at` | zaman çizgisi (JSON'da epoch-**ms**) | spans + KSM |
| `status` | `in_progress · completed · rolled_back · stalled · superseded` (`reconcile.go:115-124`); `stalled` YALNIZ KSM'den | |
| `detected_by` | `spans` \| `spans+ksm` (ksm.go:114) | |
| `image`, `image_tag`, `prev_*`, `span_count`, `note` | | |

Anahtar: `ORDER BY (cluster_id, namespace, workload, revision, started_at)`,
`ReplacingMergeTree(version)`, TTL 180 g. Go: `rollout.Rollout`
(reconcile.go:87-113), `chstore.RolloutRow` (+`MarshalJSON` camelCase/ms,
rollouts.go:289-311). Okuma: `RolloutList(ctx, RolloutFilter{ClusterID,
Namespace, Workload, Status, Kind}, from, to, limit)` (rollouts.go:343),
`rolloutWhere` (:311-328), `RolloutByID` (:362). Köprü MV
`workload_revision_activity_1m` `ORDER BY (cluster, k8s_namespace, workload,
revision, service_name, bucket)`, TTL 7 g — **service_name birinci sınıf
boyut** (0012:171-203).

**Pod → ReplicaSet → rollout:** `k8s_replicaset` terfi kolonu (0012:63-96)
otoriter; `podTemplateHash(pod)` (`deploys.go:669`) `<deploy>-<hash>-<rand5>`
string yedeği (host.name biçiminde boş döner). Bugün `Problem.Pod` →
revizyon eşleyen fonksiyon YOK.

**Eski model (korunur):** `Deploy/RecentDeployEntry` (`deploys.go:24-51`),
`EnrichProblemsWithDeploys` (`problem_telemetry.go:174-216`, okuma anında,
`effectiveVersionExpr` = image.tag ≫ service.version ≫ labels ≫ helm),
`Problem.RecentDeploy{Version, TimeUnixNs, AgeSeconds, Impact}`; UI
`ProblemDetail.tsx:764-766` DeployBox + `:788-802` zaman çizgisi;
RootCausePanel `likelyCause()` (:266-283) deploy'u birinci sırada okur.

---

## 2. Problem kanıt yapısı + yeni kanıt tipi

- `root_cause_hypotheses` (`store.go:1427-1443`): `(anchor_kind, anchor_id)`
  RMT(version), kolonlar `candidates`/`recent_deploy`/`deep_evidence` JSON,
  30 g TTL; `UpsertHypothesis` açık kolon listesiyle **tam satır** yazar
  (:123 şerhi) — yeni alan `UpsertHypothesis`'ten geçmezse sıfırlanır.
- `DeepEvidence{Checked, Exceptions, Templates, Heap, GCPause, Runtime,
  SlowOps, Business, CodeMeaning}` (`rootcause_hypothesis.go:44-54`) —
  deploy/rollout üyesi YOK. `ScoredCause{Service, Score, Hops, Path, Kind,
  Reason}`, `Kind: "" | "node"` (:89-99) — genişleme noktası.
- Sentezleyici (`rootcause_worker.go:221-240`): açık **critical** problemler
  → `buildEvidenceBundle` (`fusion.go:40-57`, `Deploy *RecentDeployEntry`,
  30 dk lookback) → `synthInputForProblem` → `appendNodeCauses` →
  `enrichDeployImpact` → `correlator.Synthesize` → `UpsertHypothesis`.
  ⚠ Yalnız critical anchor'lar (batch tavanı) — operatörün "bir Problem
  açıldığında" cümlesi her şiddeti kapsıyorsa anchor seçimi genişler
  (maliyet: tik başına +N rollout okuması, hepsi PK'lı, ucuz).

**Yeni kanıt tipi (öneri):**
```go
// chstore/rootcause_hypothesis.go — DeepEvidence'a
Rollouts []RolloutEvidence `json:"rollouts,omitempty"`

type RolloutEvidence struct {
    ClusterID, Namespace, Workload, Kind, Revision string
    ImageTag, PrevImageTag, Status, DetectedBy      string
    StartedAtMs, CompletedAtMs int64
    AgeMin      int     // problem onset − started_at (dk); negatif = sonra başlamış (elenir)
    Band        string  // "high" (0-30) | "low" (30-120)
    Score       float64 // K3
    MatchedBy   string  // "service" | "pod"
    Services    []string // MV'den (RolloutServices), ≤10
}
```
`ScoredCause{Service: RolloutIDFor(ev), Kind: "rollout", Score, Reason:
"olası neden: <workload> rollout'u (<rev>), <N dk önce>, <status>"}` —
`Service` alanında opak kimlik (`rollout:<cluster>/<ns>/<workload>@<rev>`),
`NodeIDFor` emsali; UI `Kind`'a bakar.

---

## 3. Eşleme anahtarları

| Adım | Anahtar | Kaynak | Durum |
|---|---|---|---|
| A | `Problem.Service` (+`Problem.Clusters` span cluster değerleri, okuma anında) | `problem.go:118,167` | ✅ var |
| B | servis → `(cluster, k8s_namespace, workload, revision)` | `workload_revision_activity_1m` `WHERE service_name = ? AND bucket ∈ [onset−120 dk, onset]` — **`RolloutServices`'in tersi, YENİ** `RolloutsForService` | ❌ yazılacak (PK öneki cluster → `cluster IN (span değerleri)` ile) |
| C | `(cluster, ns, workload)` → rollout'lar | `RolloutList` / `rolloutWhere`, `started_at ∈ [onset−120 dk, onset+5 dk]` | ✅ var |
| D | cluster kimliği çevirisi | span değeri ↔ `EffectiveID`: `ClusterConfig.SpanClusterKeys()` (`thanos/cluster_identity.go:67-80`), emsal `rollout_detail.go:59-79 resolveCluster` | ✅ var |
| E | pod → revizyon (Influx `INSTANCEID`/`k8s.pod.name`, runtime pod problemleri) | `k8s_replicaset` terfi kolonu: `SELECT any(k8s_replicaset), any(k8s_namespace), any(k8s_deployment) FROM spans WHERE k8s_pod = ? AND time ∈ [onset−15 dk, onset] LIMIT 1` (skip index `idx_k8s_pod` var) → revizyon eşleşen rollout | ❌ yazılacak; `podTemplateHash` yedek |
| F | `Problem.StartedAt` **ns** ↔ `started_at` **ms** | | birim çevirisi — test şart (unit-mixing dersi) |

`Problem.Kind` (service/db/external) ile `workload_kind` KARIŞTIRILMAZ.
Dış (Influx) problemde servis = kaynak adı → B boş döner; **yalnız E (pod)
yolu** çalışır — D4 enrichment'ın pod listesi (`AffectedPods`) girdidir.

---

## 4. Pencere ve skorlama (saf, tablo-testli)

```
ageMin = (onsetNs/1e6 − startedAtMs) / 60000
ageMin < −5            → elenir (problemden sonra başlayan rollout)
0   ≤ ageMin ≤ 30      → band=high, base=0.90
30  < ageMin ≤ 120     → band=low,  base=0.50
> 120                  → elenir
status: completed/in_progress → ×1.0 · stalled/rolled_back → ×1.1 (kendisi bir sinyal) · superseded → ×0.7
detected_by = spans+ksm → +0.05 · matchedBy=pod → +0.05 (daha kesin eşleme)
tavan 0.98; birden çok rollout → skor desc, eşitlikte en yakın started_at
```
Mevcut deploy tier'ıyla uyum: `deployBaseScore 0.80 + freshness×0.15`
(hypothesis.go:46-60) — rollout adayı aynı BANDA düşer, eski
`RecentDeploy` varsa ikisi de listelenir (aynı olayın iki gözlemi olabilir:
`image_tag` eşitse tek satıra birleştir, `Reason` "spans+ksm ile doğrulandı").
`freshnessFrac` (`rootcause_worker.go:473-493`) yerine banda göre sabit —
operatörün istediği iki-kademeli güven.

---

## 5. Correlator'a ekleme

- `SynthesisInput`'a `Rollouts []RolloutCandidate` (hypothesis.go:129-165);
  `Synthesize` Tier 1'e (deploy tier, :263-290) rollout adaylarını ekler:
  `ScoredCause{Service: rolloutID, Kind: "rollout", Score: K3, Reason}`.
  Deploy ile aynı `image_tag` → birleştir (tek aday, en yüksek skor).
- `maxEvidenceTypes = 4` (:123) DEĞİŞMEZ: rollout "deploy" kanalını doldurur
  (`Confidence` paydası korunur; `hypothesis_deploy_impact_test.go` pinleri
  bozulmaz).
- `appendNodeCauses` (rootcause_worker.go) emsalinde `appendRolloutCauses(ctx,
  in, problem)`: B+C+D+E adımları, tik başına anchor ≤ batch, her biri 1-2
  PK'lı sorgu (`rolloutWhere` + MV öneki).
- Weighted edge (`EdgeStat`, `Downstream/Upstream`) DOKUNULMAZ (K4).
- Bayrak: rollouts katmanı kapalıysa (`rolloutCfg` disabled, `rollouts.go:53-60`)
  → adım atlanır, eski deploy tier'ı aynen.

---

## 6. UI

**Problem detayı — "Related deployments" bloğu** (`ProblemDetail.tsx`,
`Root cause analysis` Sect'inin altına, `RootCausePanel` içinde yeni
`Section`): satır başına `workload (kind) · revision/image_tag · N dk önce ·
status rozeti · band rozeti (high/low) · "olası neden" cümlesi · link
`/rollouts?rollout=<id>` (mevcut `RolloutDrawer` `?rollout=`). Kaynak:
`rc.hypothesis.deep.rollouts`; `likelyCause()` (:266-283) rollout'u deploy
ile aynı önceliğe alır; `nothing` yüklemi (:52) rollout'u da sayar.
Zaman çizgisi (`:788-802` `pb-tl`) rollout girdisi `<li class="warn">`.
Atomlar: `Section`, `.badge`, `Link` — yeni primitif yok.

**Rollout feed — "caused N problems"** (`Rollouts.tsx:47-59` COLS, `:174-192`
satır): yeni kolon `problems` (sayı + rozet `b-err` >0), tıklanınca
`RolloutDrawer` (`"Deploy'dan beri açık problemler"` bölümü zaten var,
`RolloutDrawer.tsx:73`). Backend: `listRollouts` cevabına `problemsCaused`
— `filterOpenProblemsSince` (`deployment_report.go:40-55`) liste
seviyesine hoist: rollout'un servisleri (`RolloutServices`) × açık
problemler snapshot'ı (tek `OpenProblemsSnapshot`), rollout başına O(servis).
Liste ≤ 200 satır → tek snapshot + bellek-içi eşleme; cache anahtarına
snapshot sürümü değil TTL (20 s).

---

## 7. Riskler ve dosya planı

| # | Risk | Önlem |
|---|---|---|
| R1 | `workload_revision_activity_1m` TTL 7 g — 7 günden eski problem için B adımı boş | Eski problemler için `RecentDeploy` (span çıkarımı) yedek; kanıt "rollout katmanı 7 g" notu |
| R2 | Cluster kimlik uzayı ikili (span değeri vs EffectiveID) | D adımı zorunlu; test iki yönü de pinler |
| R3 | ns/ms karışımı (StartedAt ns, started_at ms) | saf `rolloutAge(onsetNs, startedAtMs)` + tablo testi her birimde |
| R4 | Tam-satır replace: `Rollouts` alanı `UpsertHypothesis` kolon listesine girmezse sıfırlanır | deep_evidence JSON içinde — mevcut kolon; test `GetHypotheses` gidiş-dönüş |
| R5 | Dış Distributed prod'da 0012 uygulanmadıysa tablo yok | `rolloutCfg` bayrağı kapalı → adım atlanır (bugünkü 404 sözleşmesi) |
| R6 | Aynı olayın iki gözlemi (RecentDeploy + rollout) çift aday | image_tag eşitliğinde birleştir; test |
| R7 | `maxEvidenceTypes` paydası | rollout deploy kanalında — güven skorları değişmez; pin testi |

| Dilim | Kapsam | Dosyalar | Test |
|---|---|---|---|
| **D1** eşleme + skor (saf) | `RolloutsForService` (MV ters okuması), `rolloutForPod` (k8s_pod → replicaset), `ScoreRollout(onsetNs, rollout)` bandı, cluster çevirisi | `chstore/rollout_problem.go` (yeni), `rollout/score.go` (yeni) | tablo: rollout var/yok, 0-30/30-120/>120 dk, problemden sonra, birden çok rollout, ns/ms, stalled/superseded, pod eşlemesi |
| **D2** worker + kanıt | `DeepEvidence.Rollouts`, `appendRolloutCauses`, `Synthesize` rollout tier + deploy birleştirme | `chstore/rootcause_hypothesis.go`, `anomaly/rootcause_worker.go`, `correlator/hypothesis.go` | hypothesis pinleri (paydanın değişmediği), birleştirme, gidiş-dönüş |
| **D3** Problem UI | RootCausePanel "Related deployments" + zaman çizgisi + `likelyCause`/`nothing` | `components/RootCausePanel.tsx`, `features/anomalies/ProblemDetail.tsx`, `lib/types.ts` | vitest: rollout varken `nothing=false`; kind=rollout satırı |
| **D4** Rollout feed rozeti | `problemsCaused` (liste hoist) + kolon + drawer linki | `api/rollouts.go` (mevcut dosya, rota yok), `chstore/rollouts.go`, `pages/Rollouts.tsx`, `lib/types.ts` | Go: hoist sayımı = drawer sayımı (aynı fonksiyon); vitest kolon |

api.go büyümez (rotalar mevcut dosyalarda). Efor: D1 ~2 s · D2 ~2 s · D3 ~1 s · D4 ~1 s.

**Onay soruları:** (1) anchor kapsamı — yalnız critical (bugünkü sentezleyici) mi, tüm açık problemler mi? (2) pencere 120 dk üst sınırı ve 0.9/0.5 bantları; (3) rollout + eski RecentDeploy birleştirme kuralı (image_tag eşitliği).
