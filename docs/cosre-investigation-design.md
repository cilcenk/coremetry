# CoSRE — P1 soruşturması (tasarım)

**Tarih:** 2026-08-01 · **Durum:** uygulandı (v0.9.510-516) · model gerekçesi v0.9.517'de düzeltildi

Operatör: *"CoSRE akıllı olsun; bir hata olduğunda gerekirse pod'larına,
metriklerine, loglarına baksın, ona göre sonuç çıkarsın, kaydetsin.
Gerçek bir SRE gibi."* Kapsam kararı: **yalnız P1** — yeni açılan ya da
P1'e eskale olan. P2/P3 bugünkü sığ paketiyle kalır.

---

## 1. Bugün ne var, ne yok

`rootcause_worker.go` + `problem_explainer.go` zaten "bak → sonuç çıkar →
kaydet" iskeletini kuruyor: lider-kapılı arka plan işçisi, çapraz-sinyal
kanıt paketi, deterministik `correlator.Synthesize`, kalıcı
`RootCauseHypothesis` + `Problem.AISummary`.

Eksik olan **neye baktığı**. Kanıt paketi (`fusion.go:40`) sabit:

| Var | Yok |
|---|---|
| Aynı servisteki açık problemler | **Pod durumu** (restart, CrashLoop, OOMKill, pending) |
| Anomali olayları (log_pattern / trace_op) | **Doygunluk** (CPU/bellek/heap/GC/JMX) |
| Deploy'lar | **Gerçek log satırları / baskın şablonlar** |
| Topoloji komşuluğu (düz + ağırlıklı) | **Exception grupları** |

Yani bugünkü muhakeme "komşuma ve deploy'a bakan" bir muhakeme.
Operatörün istediği "pod'una, metriğine, loguna bakan" muhakeme.

## 2. Mimari ilke — "gerçek SRE" nasıl kurulur

**Düzeltme (2026-08-01, v0.9.517):** bu bölüm önce "model Gemma4-2B"
varsayımıyla yazılmıştı. Operatör ekranından doğrulanan gerçek model
**gemma4-26b-a4b-it** (26B toplam / 4B aktif MoE, instruction-tuned) —
çok daha yetenekli bir sınıf. Tasarım DEĞİŞMEDİ ama gerekçesi değişti:
artık "model yapamaz" değil, üç somut sebep:

1. **Maliyet** — her P1'de serbest ajan döngüsü N tur LLM çağrısı;
   deterministik playbook tek anlatım çağrısıyla bitiyor.
2. **Denetlenebilirlik** — hangi sinyale bakıldığı KOD, dolayısıyla
   tablo-testli ve tekrarlanabilir. Model seçseydi her koşuda farklı
   bakabilirdi; denetim izi de anlamını yitirirdi.
3. **Sınırlılık** — okumalar sınırlı kalmalı (LIMIT / timeout / pencere).
   Modele araç verirsek bu sınırları o seçer.

Serbest tool-loop bu modelde yeniden değerlendirilebilir — ama önce
ölçüm aleti (altın-küme) gerekir, yoksa "daha iyi mi" sorusu cevapsız.

SRE davranışını **doğru katmana** koymak:

| SRE'nin yaptığı | Nerede olur | Neden |
|---|---|---|
| Nereye bakacağına karar vermek | **Kod** — sinyal şekline göre playbook | Deterministik, test edilebilir, ucuz |
| Sinyalleri okumak | Mevcut sınırlı store okumaları | Yeni sorgu yazmıyoruz, mevcutları besliyoruz |
| Sebepleri sıralamak | `correlator.Synthesize` (LLM'siz) | Zaten en güçlü parça |
| Sonucu insan diliyle yazmak | Model, tek atış, kanıt önünde | Modelin katma değeri burada; kanıt zaten hazır |
| Kaydetmek | `RootCauseHypothesis` + `AISummary` | Zaten var |

Modelin işi **araştırmak değil, bulguyu anlatmak**.

## 3. Koşullu playbook

Problemin metriğine/kural şekline göre hangi ek kanıt ailesinin
çekileceği dallanır. Her dal mevcut, sınırlı okumaları kullanır:

| Tetikleyen sinyal | Ek olarak bakılır | Kullanılan mevcut okuma |
|---|---|---|
| `error_rate`, exception-şekilli | Exception grupları + baskın log şablonları | `ListExceptionGroups`, `ListLogTemplates`, `DetectLogPatterns` |
| `p99_ms` / `p95_ms` / `avg_ms` | Pod doygunluğu + yavaşlayan operasyonlar | `JVMHeapPodUsage`, `JVMGCPodPause`, `JVMGCActivity`, `operation_summary_5m` |
| DOWN / availability | Pod durumu, sonra deploy | `GetServiceRuntime`, `PodServiceMap` |
| db / messaging metriği | İlgili summary MV + çağıran kırılımı | `db_summary_5m`, `db_caller_summary_5m`, `messaging_*_5m` |
| Her durumda | Bugünkü beş kaynak | `gatherEvidenceInputs` |

Dallanma **saf bir fonksiyon** olur (`investigationPlan(p) []signalFamily`),
tablo-testli — hangi problem şeklinin neyi tetiklediği pinlenir.

## 4. Denetlenebilirlik — "neye baktım, ne buldum"

Modelin ürettiği metne güvenmenin tek yolu, hangi sinyallerin
GERÇEKTEN okunduğunun görünür olması. Hipotez bir denetim izi taşır:

```
Checked []CheckedSignal   // family, okundu mu, ne bulundu (kısa), kaç kayıt
```

Bu hem operatöre "SRE raporu" hissi verir hem de model uydurursa
yakalanmasını sağlar: iz "pod'lara bakıldı, anormallik yok" derken
anlatım "pod restart'ları yüzünden" diyorsa çelişki görünür olur.

Ayrıca `Confidence` zaten dürüstçe düşük olabiliyor; anlatım prompt'u
**"kanıt zayıfsa sebep uydurma, veri yetersiz de"** talimatını taşır.

## 5. Maliyet sınırı

Bugünkü tasarımın en güçlü yanı tick başına **5 okuma**, tüm batch
paylaşıyor. Koşullu derinleşme bunu bozar. Sınır:

- **Yalnız P1.** `computePriority(p, nowNs) == "P1"` (saf fonksiyon,
  `problem.go:221`). Prod'da 105 açık problem vardı; hepsine derin
  soruşturma ClickHouse'a ciddi yük bindirirdi.
- **Yalnız durum değişiminde** — yeni açılan ya da P1'e eskale olan.
  Sürekli değil. Bir SRE de olay *olduğunda* bakar, her 30 saniyede değil.
- Playbook dalları **en fazla 3 ek okuma** ekler; her biri zaten sınırlı
  (LIMIT + `max_execution_time` + zaman-sınırlı WHERE).
- Aynı anchor için soruşturma **bir kez** koşar (hipotez zaten kayıtlı
  olduğunda tekrar etmez), eskalasyon yeni bir tetikleyicidir.

## 6. Dosyalar (gönderim sırası)

- `internal/anomaly/investigation.go` (yeni) — `investigationPlan(p)` saf
  dallanma + `gatherDeepEvidence(ctx, store, logs, plan, service)`
- `internal/anomaly/investigation_test.go` (yeni) — plan tablosu:
  hangi metrik hangi aileyi tetikler, P2/P3 tetiklemez
- `internal/anomaly/fusion.go` — `EvidenceBundle`'a derin alanlar +
  `renderEvidence`'a "neye bakıldı" bölümü
- `internal/anomaly/rootcause_worker.go` — P1 kapısı + plan çağrısı
- `internal/chstore/rootcause_hypothesis.go` — `Checked` kolonu
  (ReplacingMergeTree: TÜM alanlar ileri taşınır) + okuma/yazma
- CH şema değişikliği → **`/clickhouse-schema` kapısı**; kolon ekleme
  **distributed-safe day one** olmalı (hasXCol probe + koşullu INSERT),
  bkz. prod'u iki kez kıran sınıf

## 7. Bu tasarımın yuttuğu bekleyen işler

- **Kök-neden chat intent'i** — zenginleşmiş hipotezi okur, ayrı iş değil
- **P1 e-postasına AI yorumu** — aynı sonucu taşır; e-posta 45sn'ye kadar
  özeti bekler (operatör onayı alındı, SSE/webhook anında gider)

Sıra: önce soruşturma derinleşsin, sonra sonuç chat'e ve e-postaya aksın.

## 8. Riskler

| Risk | Azaltma |
|---|---|
| ClickHouse yükü | P1 + durum-değişimi kapısı; dal başına ≤3 sınırlı okuma |
| Model derin kanıtla daha iddialı ama daha yanlış olur | Denetim izi + düşük-güven talimatı + `Confidence` |
| Kolon ekleme prod'u kırar | `/clickhouse-schema` + distributed-safe probe deseni |
| Playbook yanlış dala girer | `investigationPlan` saf + tablo-testli |

## 9. Ölçüm

- `ai_calls.Surface` — soruşturma anlatımları ayrı yüzey olarak kaydedilir,
  `/ai`'da kalite ayrı izlenir
- Denetim izi sayesinde "kaç P1'de gerçekten pod/log okundu" sorulabilir
- 👍/👎 zaten var; soruşturma anlatımları için ayrışır
