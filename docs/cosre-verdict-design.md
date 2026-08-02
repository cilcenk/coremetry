# CoSRE — RCA verdict (tasarım, 2. yazım)

**Tarih:** 2026-08-02 · **Durum:** onay bekliyor · kardeş doküman:
[cosre-investigation-design.md](cosre-investigation-design.md) (soruşturma
katmanı, v0.9.510-516 gemide)

Operatörün RCA paketi altı katman öneriyor. `[1] DETECT` · `[2] CORRELATE`
· `[3] ENRICH` zaten gemide. Bu doküman **yalnız `[5] EXPLAIN`**'i
kapsıyor: bugün düzyazı olan kök-neden anlatımını, kalkanlarla
korunan katı bir JSON verdict'e çevirmek.

`[4] INVESTIGATE`'in agentic tool döngüsü **açılmıyor** (operatör
kararı, 2026-08-02): bu runtime'da (gemma4-26b, air-gapped) deterministik
prefetch'in tool döngüsünü yendiği daha önce ölçülmüştü ve mevcut
`investigationPlan → gatherDeepEvidence` bilerek öyle yazıldı. Prompt 1'in
asıl değerli kısmı — *"prefer refutation over confirmation"* — döngü
olmadan, prompt'a gömülerek alınıyor (§6).

`[6] LEARN` bu dilimde YOK. İmza çıkarabilmek için önce yapılandırılmış
bir verdict gerekiyor; sırası bu.

---

## 0. Bu, tasarımın İKİNCİ yazımı

Birinci yazım çekişmeli incelemeden **"ciddi-sorun"** aldı: yedi kritik
bulgu, ikisi mevcut duruma göre **regresyon**. Bu yazım her birine
açıkça cevap veriyor; §9 hangi bulgunun nerede karşılandığını listeler.

En önemli ders şuydu: **bir kalkan, kandırıldığında sessiz kalmakla
kalmaz — kendi çıktısı uydurmayı doğrulanmış gösterebilir.** Birinci
yazımda model "pod restart döngüsünde" deyip kanıt olarak *"pod restart
kaydı bulunamadı"* satırını gösterebiliyordu; kalkan geçiyordu (id
gerçekten katalogda), sonra sunucu o katalog metnini iddianın yanına
basıyordu. Ekranda "Kök neden: pod restart döngüsü · Kanıt: pod: yok"
görünüyordu. Kalkanın varlığı zararı **artırıyordu**.

---

## 1. Bugün ne var

| Katman | Durum | Yer |
|---|---|---|
| Deterministik hipotez | ✅ | `correlator.Synthesize` → `RootCauseHypothesis` (kalıcı, CH) |
| Kanıt paketi | ✅ | `anomaly.EvidenceBundle` (`fusion.go:40`) |
| Derin soruşturma | ✅ | `investigationPlan` + `gatherDeepEvidence`, yalnız P1 |
| Anlatım (düzyazı) | ✅ | `rootCauseExplainProse` → `{"prose": "..."}` |
| Anti-uydurma kuralları | ✅ | sistem prompt'unda (v0.9.556) |
| **Yapılandırılmış verdict** | ❌ | — bu dilim |
| **Deterministik kalkanlar** | ❌ | — bu dilim |
| Öğrenme / imza | ❌ | sonraki dilim |

Yüzey **tek**: `rootCauseExplainProse` (`internal/api/rootcause.go`), yani
`/api/problems|anomalies/{id}/rootcause/explain`. Arka plan
`ProblemExplainer`'a (CH'ye `ai_summary` yazan düz metin) **dokunulmuyor** —
onun altı çizim yeri var ve ikisi `line-clamp` ile kırpıyor, yani zengin
çıktı orada zaten görünmez.

---

## 2. Kanıt kataloğu — iki ayrı id uzayı

Bu, birinci yazımın en ağır hatasının düzeltmesi.

```
E1..En   POZİTİF kanıt — bulunmuş sinyaller
N1..Nn   NEGATİF kanıt — bakılmış, BULUNAMAMIŞ sinyaller
```

Ayrım keyfi değil, mantıksal: **aranmış ve bulunamamış olmak, olduğunun
kanıtı değildir.** Bir çürütmenin dayanağı negatif kanıt *olabilir*
("trafik artışı değil, çünkü istek hacmi sabit"), ama bir kök-nedenin
dayanağı **olamaz**.

Kural bu yüzden asimetrik:

| Alan | E kabul | N kabul |
|---|---|---|
| `root_cause.evidence` | ✅ | ❌ **reddedilir** |
| `causal_chain[].evidence` | ✅ | ❌ **reddedilir** |
| `rejected_hypotheses[].refuted_by` | ✅ | ✅ |

`Deep.Checked` içindeki `Found:false` kayıtları N uzayına gider.
Katalog kurulurken bu ayrım **veriden** yapılır, modelin beyanından
değil.

---

## 3. Verdict şeması

Modelden istenen (`rcaModelVerdict`) ile operatöre dönen
(`RCAVerdict`) **aynı şey değil**: kalkanlar aradaki farkı üretir.

```go
// Modelin ürettiği ham şekil — json_schema ile zorlanır.
type rcaModelVerdict struct {
    Verdict     string   // root_cause_identified | probable_cause | insufficient_evidence
    Title       string   // ≤90 char
    Summary     string   // ≤60 kelime
    RootCause   struct {
        Entity         string   // topolojiden, serbest metin DEĞİL
        FailureMode    string
        Trigger        string
        LatentWeakness string
        Evidence       []string // yalnız E-id
    }
    CausalChain []struct {
        Step     int
        Entity   string
        Effect   string
        Evidence []string // yalnız E-id
    }
    RejectedHypotheses []struct {
        Hypothesis string   // SUNUCUNUN verdiği listeden (enum)
        RefutedBy  []string // E veya N
        Reason     string
    }
    ModelConfidence float64 // 0..1 — modelin BEYANI
    MissingEvidence []string
    Remediation []struct {
        Kind   string // mitigate | fix
        Action string
        Target string
        Risk   string // low | medium | high
    }
}
```

**`deciding_rules` alanı YOK.** Paket R1-R7 kurallarını verdict'e
yazdırmak istiyor, ama o kurallar kodda kural olarak yaşamıyor —
`ScoredCause` harmanında gömülüler. Modelden "hangi kural karar verdi"
istemek, sıfır kalkanı olan bir **denetim izi görüntüsü** üretirdi:
denetlenebilir görünen, aslında tamamen uydurulmuş bir alan. Denetim
izi taklidi, denetim izinin yokluğundan kötüdür.

Operatöre dönen zarf:

```go
type RCAVerdict struct {
    rcaModelVerdict                    // kalkanlardan geçmiş hâli
    Confidence      float64            // TAVANLANMIŞ (bkz. §4)
    HypothesisConfidence float64       // deterministik motorun kendi güveni
    Impact          *RCAImpact         // ölçülmüş; ölçülemezse nil
    Shields         RCAShieldReport    // ne yakalandı
}
```

### Üç "confidence" sorunu

Bugün ekranda zaten iki tane var ve üçüncüsü geliyordu:

| # | Alan | Anlam | Aralık |
|---|---|---|---|
| 1 | `RootCauseHypothesis.Confidence` | deterministik füzyon | 0..1 |
| 2 | `EvidenceBundle.Confidence` | *kaç FARKLI kanıt tipi var* | 0..5 |
| 3 | `RCAVerdict.ModelConfidence` | modelin beyanı | 0..1 |

(1) ve (3) aynı yanıtta yan yana duracak ve ikisi de "confidence".
Bu yüzden: modelin alanı **`model_confidence`**, deterministik olan
**`hypothesis_confidence`** adıyla döner; tavanlanmış nihai değer
`confidence` kalır. (2) zaten dışa açılmıyor, adı korunur.

---

## 4. Kalkanlar

Her kalkan için **ne yakaladığı** kadar **ne yakalamadığı** da yazılı.
Bir kalkanın sınırını bilmemek, olmamasından tehlikelidir.

### K1 — Şema doğrulama
`json_schema` (v0.9.526 merdiveni) + Go tarafında yeniden doğrulama.
Başarısız → **1 onarım turu** → yine başarısız → `insufficient_evidence`.

*Yakalamaz:* şemaya uygun ama içeriği uydurma cevap. Diğer kalkanların
tamamı bunun içindir.

### K2 — Kanıt id doğrulama (iki uzaylı)
Her id katalogda var mı **ve doğru uzayda mı**. `root_cause.evidence`
içinde N-id → **o iddia düşürülür**, `shields.rejected_evidence`'a yazılır.

*Yakalamaz:* gerçek bir E-id'nin **alakasız** bir iddiaya iliştirilmesi.
Bunu hiçbir mekanik kalkan yakalayamaz — bu yüzden kanıt metni iddianın
*yanına* değil, **altına** ve *"model şu kanıtı gösterdi"* ayrımıyla
basılır. Sunucu, modelin iddiasını kendi sesiyle onaylamaz.

### K3 — Varlık doğrulama + **serbest metin taraması**
`root_cause.entity` beyaz listede olmalı (topoloji + hipotez adayları).
**Ayrıca** `title`, `summary`, `causal_chain[].entity/effect`,
`rejected_hypotheses[].hypothesis/reason`, `remediation[].action/target`
alanlarının **hepsi** taranır.

Tarayıcı **zaten var**: `serviceTokenRe` + `nonServiceHyphenated`
(`copilot_aianalyze.go:436`), kardeş yüzey `postCheckServiceAnalysis`'te
kullanılıyor. Yeniden yazılmıyor, paylaşılan yere taşınıyor.

Bilinmeyen ad → `shields.unknown_entities` + güven cezası.

*Neden şart:* beyaz listeden geçen bir `entity` ile, `causal_chain`'de
ve `remediation.action`'da **var olmayan** bir servis anlatılabilir.
Operatör ekranda gerçek görünen bir nedensellik zinciri ve somut bir
aksiyon görür. Birinci yazım bu alanlara hiç bakmıyordu — yani mevcut
kardeş yüzeye göre **regresyondu**.

### K4 — Elemecilik, oyunlanamaz hâlde
Üç parça birlikte çalışır:

1. **Rakip hipotezler SUNUCU üretir.** Model serbest metin yazmaz;
   deterministik hipotezin 2..N. adaylarından + sabit sınıflardan
   (yük artışı, altyapı, bağımlılık, config, doygunluk) kurulan
   listeden **seçer** (enum).
2. **`refuted_by` ∩ `root_cause.evidence` = ∅.** Aynı kanıt hem destek
   hem çürütme olamaz. Kesişim varsa eleme geçersiz sayılır.
3. **Tavan çift taraflı:**
   `confidence = min(model_confidence, hypothesis_confidence + 0.1)`
   ve eleme yoksa ayrıca `≤ 0.6`.

*Neden (1) ve (2) şart:* birinci yazımda tek gerçek kanıt id'siyle sahte
bir eleme yazmak yeterliydi ve tavan hiç tetiklenmiyordu. Daha kötüsü,
prompt modele bunu **öğretiyordu**: *"root_cause_identified yalnız
rakipler elendiyse"* = *"sahte bir eleme yaz, en yüksek verdict'i al"*.
Bir kalkan, davranış teşviki üretiyorsa yanlış kurulmuştur.

*Neden (3):* model, korelasyon motorunun verdiğinden **fazla kanıta
sahip değil** (tool döngüsü yok). Motordan belirgin şekilde daha emin
olması, tanım gereği temelsizdir.

### K5 — Impact ClickHouse'tan
`affected_request_share`, `error_count` modelden **alınmaz**; MV'den
okunur.

Üç düzeltme:

- **Doğru varlık için**: `root_cause.entity` (K3'ten geçtiği için
  güvenli), ankor servisi ayrı alanda. Birinci yazım ankorun servisini
  okuyordu — "Kök neden: payment-db" başlığının altında checkout'un
  sayıları görünecekti.
- **Boş sonuç ≠ sıfır**: MV henüz materyalize olmamışsa (son 5dk)
  sorgu 0 satır döner, `err == nil`. Sayılar **nil** döner ve
  `denominator_note` sebebi yazar. `0` basmak, tam patlama anında
  "etkilenen istek yok" demek olurdu — ve ✨ Explain'e basılan an tam
  olarak o andır.
- **Kova hizalaması**: ✅ **v0.9.555'te düzeltildi** (bu incelemenin
  yan ürünü — hata tasarımda değil mevcut kodda çıktı).

Pencere üst sınırı `now` ile de kırpılır (gelecek pencere gösterilmez).

Hesabın kendisi `*chstore.Store`'a bağlı olduğu için test edilemez;
**pencere + hizalama saf bir yardımcıya** çıkarılır ve tablo-testlenir.

---

## 5. Cache ve yanıt sözleşmesi

**`serveCached` BIRAKILMIYOR.** Birinci yazım açık `cache.Get/Set`'e
geçiyordu; bu, SWR'nin yanı sıra **singleflight dedupe**'unu, L1'i ve
ayrık L2 yazımını da kaldırırdı. Tek gerçek ihtiyaç "başarısız verdict'i
pinleme" idi ve o **zaten sağlanıyor**: `fn` hata dönerse `serveCached`
hiçbir şey yazmaz (`cache.go:266-268`).

İptal-edilmiş-ctx tuzağı ✅ **v0.9.557'de düzeltildi** (yine bu
incelemenin yan ürünü).

### `prose` boş kalabilmeli

Yanıt gövdesi `{"prose": ...}` kalır, yanına `"verdict": {...}` eklenir.
Ama **fallback `prose`'u DOLDURMAZ**:

`RootCauseRibbon.tsx` `prose === null` iken *"No narration available"*
basıyor. `prose` hep dolu gelirse bu dürüst-boş dalı **ölür** ve
deterministik yedek cümle, gerçek LLM anlatımıyla **birebir aynı
kutuda** çizilir — operatör modelin cevap verip vermediğini ayırt
edemez.

Bu yüzden: verdict düşerse deterministik özet `verdict.summary`'ye
yazılır, `prose` **null kalır**. Frontend'in mevcut dalı çalışmaya
devam eder ve tek satır değişmez.

---

## 6. Prompt değişikliği — elemecilik, döngüsüz

Yeni sistem prompt'u (`systemRCAVerdict`), mevcut
`systemRootCauseNarration`'ın yerine değil **yanına** gelir; narration
prompt'u **silinmez** (düşüş yolunda dönülecek düzyazı sözleşmesi
olarak lazım).

Prompt 1'den alınan tek şey **elemecilik**:

> Önce 2-4 rakip hipotez say. Her biri için, onu en güçlü şekilde
> ÇÜRÜTECEK gözlemi söyle. Doğrulamayı değil çürütmeyi tercih et.

Ama **hipotez listesi sunucudan gelir** (§4.1), yani "elemecilik"
serbest metin üretmez, verilen listeden seçim yaptırır.

v0.9.556'nın iki anti-uydurma kuralı bu prompt'a da **aynen** taşınır —
özellikle negatif kanıt kuralı, çünkü bu yüzeyin kanıt kataloğu N
uzayını da içeriyor.

---

## 7. Ölçüm

`aiSurfaceFromPath` bu ucu `other` kovasına atıyordu; ✅ **v0.9.557'de
`rootcause-explain` adıyla ayrıldı**. Böylece `/ai`'da:

- `insufficient_evidence` oranı
- onarım turu (`repaired`) oranı
- kalkan tetiklenme sayıları (`shields.*`)

ölçülebilir hâle gelir. **Uyarı:** `insufficient_evidence` oranı
düşerken precision düşüyorsa model aşırı özgüvenli olmuş demektir —
kalibrasyon bandı sıkılaştırılır. Bu oranı "iyileştirilecek bir metrik"
sanmak, tam da kalkanları etkisizleştiren yol olur.

`prompt_sample` 4KB'de kesiliyor ve yeni sistem prompt'u tek başına
cap'i doldurup **kanıt kataloğunu kayıttan siliyor**. Denetim için
katalog kaydı ayrı tutulmalı ya da cap yükseltilmeli — **açık soru**.

---

## 8. Dosyalar (gönderim sırası)

| # | Dosya | Değişiklik |
|---|---|---|
| 1 | `internal/api/rca_evidence.go` *(yeni)* | E/N katalog kurucu — **saf**, tablo-testli |
| 2 | `internal/api/rca_shields.go` *(yeni)* | K2/K3/K4 — **saf**, tablo-testli |
| 3 | `internal/api/rca_impact.go` *(yeni)* | K5; pencere/hizalama saf yardımcıya ayrık |
| 4 | `internal/api/entity_scan.go` *(yeni)* | `serviceTokenRe` buraya taşınır, iki yüzey paylaşır |
| 5 | `internal/copilot/copilot.go` | `systemRCAVerdict` (narration prompt'u KALIR) |
| 6 | `internal/api/copilot_schemas.go` | verdict şeması + **sayısal prop yardımcısı** (bugün yok) |
| 7 | `internal/api/copilot_schemas_test.go` | şema sayımları elle yazılı — güncellenmeli |
| 8 | `internal/api/rootcause.go` | verdict çağrısı + zarf |
| 9 | `frontend/src/lib/types.ts` | `RCAVerdict` tipi |
| 10 | `frontend/src/features/anomalies/RootCauseRibbon.tsx` | verdict alanları; `prose===null` dalı **korunur** |

1-4 saf ve testli; 8-10 ince. Gerçek risk 5-7'de (prompt + şema).

---

## 9. Çekişmeli bulguların karşılığı

| Bulgu | Karşılık |
|---|---|
| 🔴 Negatif kanıt pozitif destek olarak sunuluyor | §2 iki id uzayı + §4-K2 + §6 kural |
| 🔴 Serbest metin kalkansız | §4-K3, mevcut `serviceTokenRe` ile |
| 🔴 Elemecilik oyunlanabilir, prompt uydurmayı ödüllendiriyor | §4-K4 (sunucu enum + kesişim yasağı + çift tavan) |
| 🔴 Impact yanlış varlık / sessiz sıfır | §4-K5 |
| 🔴 Kova hizalaması | ✅ v0.9.555 |
| 🔴 `serveCached` kaybı (singleflight/L1) | §5 — bırakılmıyor |
| 🔴 `prose` hiç boş kalmıyor, dürüst-boş dalı ölüyor | §5 — fallback `prose`'u doldurmaz |
| 🟡 Üçüncü "confidence" | §3 — yeniden adlandırma |
| 🟡 `deciding_rules` kalkansız uydurma yüzeyi | §3 — **alan kaldırıldı** |
| 🟡 Ölçüm `other` kovasında | ✅ v0.9.557 |
| 🟡 `explain_problem` MCP üçüncü tüketici | ✅ v0.9.556 |
| 🟡 `prompt_sample` 4KB cap denetimi öldürüyor | §7 — **açık soru** |
| 🟡 Şema testleri elle sayıyor, sayısal prop yardımcısı yok | §8 madde 6-7 |
| 🟡 `computeRCAImpact` test edilemez | §4-K5 — saf yardımcı ayrıldı |

---

## 10. Bilinçli kabuller

- **Gerçek bir E-id'nin alakasız iddiaya iliştirilmesi yakalanamaz.**
  Mekanik çare yok; çare sunum: sunucu modelin iddiasını kendi sesiyle
  onaylamaz (§4-K2).
- **Model her zaman `probable_cause` derse** bunu ancak operatör
  geri bildirimi ortaya çıkarır — o da sonraki dilim. Bu dilimde
  `insufficient_evidence` oranı izlenir, ama tek başına yeterli değildir.
- **Türkçe/enum karışması** şema doğrulamayla yakalanır, ama bedeli
  sahte bir `insufficient_evidence`'tır ve operatör bunu gerçek kanıt
  yokluğundan ayırt edemez. `shields.parsed=false` yanıtta döner ve
  UI'da **gösterilmelidir** — yoksa dürüst degrade kaybolur.

## 11. Bu dilimde YAPILMAYANLAR

- Agentic tool döngüsü (operatör kararı; §giriş)
- LEARN / `known_signatures` (verdict yapılanmadan imza çıkarılamaz)
- Operatör geri bildirimi (`confirmed/corrected/false_positive`)
- Arka plan `ProblemExplainer` / `ai_summary` (line-clamp'li 6 çizim yeri)
- R1-R7'nin kod-seviyesi kural motoru
