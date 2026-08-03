# Traces yavaşlığı — FAZ 3: uygulama planı

Tarih: 2026-08-03 · Kanıt: [traces-evidence.md](traces-evidence.md) ·
Teşhis: [traces-diagnosis.md](traces-diagnosis.md)

## Bu plan neden kısa

Teşhisin sekiz dilimi vardı. Uygulamaya geçince **üçü değişti** ve
biri tamamen düştü — hepsi ölçümle:

| Dilim | Planlanan | Gerçek |
|---|---|---|
| D1 | efor 1 | `metric-batch`'te `search` yokmuş; önce o eklendi |
| D3 | "`approx`'a geç" | `approx` de MV'yi kapatıyor — efor 1 → 3 |
| D5 | en büyük kazanç | **ölçümle çürütüldü**, yapılamaz |
| D7 | 4.3× bayt | prod'da zaten yapılmış — **düştü** |

Ders: teşhis kod okumaya dayanıyordu, uygulama ölçüme. Aşağıdaki her
dilim **gönderilmeden önce ölçülecek**, ve ölçüm dilimi değiştirirse
plan değişir, dilim zorlanmaz.

---

## Gemiye giren (2026-08-03)

Hiçbiri şemaya dokunmuyor; her biri tek başına geri alınabilir.

| Sürüm | Dilim | Ne değişti |
|---|---|---|
| v0.9.601 | D1 | hacim şeridi 3 CH taraması → 1 (`metric-batch`, + `search` alanı) |
| v0.9.603 | D2 | gerçek istek iptali (+ iptal/zaman aşımı ayrımı) |
| v0.9.602 | D4 | `/api/attribute-keys` debounce + yarış koruması |

**Ölçüm borcu:** üçünün de öncesi/sonrası tablosu prod'da doldurulacak.
D1 tarama sayısını 3→1 indiriyor ama `read_rows` toplamını değiştirmiyor
(aynı WHERE); kazanç soğuk önbellekte ve gidiş-dönüşte.

---

## Sıradaki dilimler

### D3 — MV tabanlı tavanlı sayım
**Sorun:** "Toplamı göster" `CountMode=exact` gönderiyor;
`countModeAllowsMV` (`repo.go:2004`) yalnız `skip`/`""` kabul ettiği
için liste ham yola düşüyor. Çift ceza: hem sayım hem 22.575:1'lik
liste.

**Kapsam:** sayımı `trace_summary_5m`'den tavanlı yapmak —
`SELECT count() FROM (SELECT trace_id FROM trace_summary_5m WHERE …
GROUP BY trace_id LIMIT 10000)`. Tavana değerse UI "10.000+" gösterir.
`countModeAllowsMV`'ye yeni kip eklenir.

**Dosyalar:** `internal/chstore/repo.go` (yeni sayım yolu +
`countModeAllowsMV`), `frontend/src/pages/Traces.tsx` (kip seçimi),
`Pager` etiketi.

**Kabul kriteri:** "toplamı göster" açıkken liste sorgusunun
`read_rows`'u kapalıyken ile AYNI kalmalı (yani MV'de kalmalı).
Sayım sorgusu ayrıca `read_rows < 200.000`.

**Geri alma:** tek commit; `countModeAllowsMV` eski hâline döner.

**Efor:** 3 · **Şema:** hayır

---

### D5b — promoted attribute kolonuna skip index
**Durum: PROD ÖLÇÜMÜ BEKLİYOR.** Lokalde `attr_channel_code` boş,
ölçüm anlamsız.

**Hipotez:** filtre granül-seçici değil. `attr_channel_code`
MATERIALIZED kolon olarak var ama üzerinde skip index yok, dolayısıyla
`WHERE attr_channel_code = 'X'` tüm pencereyi tarıyor.

**Önce ölç (prod, kod değişikliği YOK):**
```sql
EXPLAIN indexes = 1
SELECT trace_id, count() FROM spans
WHERE time >= now() - INTERVAL 6 HOUR AND attr_channel_code = '<gerçek değer>'
GROUP BY trace_id
```
`Granules: X/Y` — X ≈ Y ise indeks tam oraya oturur, X ≪ Y ise bu
dilim gereksiz ve düşer.

**Kapsam (ölçüm doğrularsa):** `ALTER TABLE spans ADD INDEX IF NOT
EXISTS idx_channel_code attr_channel_code TYPE set(0) GRANULARITY 4`.

⚠ Skip index YALNIZ YENİ part'lara uygulanır; eski veri merge edilene
kadar indekssiz taranır. `OPTIMIZE FINAL` zorlamak ağırdır — beklemek
doğru.

⚠ Bu bir ON CLUSTER ALTER: **dağıtık DDL kuyruğu düzelmeden
uygulanamaz** (bkz. aşağıdaki açık risk).

**Kabul kriteri:** aynı sorguda `Granules` en az 3× düşmeli, ve
`read_bytes` medyanı en az 2× azalmalı.

**Geri alma:** `ALTER TABLE spans DROP INDEX idx_channel_code`.

**Efor:** 2 · **Şema:** EVET

---

### D8 — trace başına, ZAMANA göre sıralı özet tablo
**En büyük kazanç, en büyük iş.** D5 çürütüldükten sonra ham yolun
maliyetini gerçekten değiştirebilecek tek dilim.

**Neden gerekli:** `spans` ORDER BY `(service_name, time)`; trace
listesi servis-bağımsız ve zaman-öncelikli. `trace_summary_5m` ise 5dk
KOVALI, yani `GROUP BY trace_id` tüm pencereyi okutuyor. İkisi de
erken sonlanamıyor (ölçüldü: 24h penceresinde MV 665k satır).

**Kapsam:** trace başına TEK satır, `ORDER BY (time, trace_id)`,
sıcak attribute'ları (CHANNEL_CODE/FUNCTION_CODE) taşıyan bir tablo.
Filtre + sıralama + LIMIT üçü de birincil anahtara biner.

**Açık tasarım soruları — ÖNCE cevaplanmalı:**
- Besleme: ingest'te mi (yazma yolu ağırlaşır), MV ile mi (trace
  kapanışını bilemez), yoksa periyodik toplama mı?
- Bir trace kova sınırını aşarsa "son hâli" ne zaman yazılır?
- Geri doldurma: 30 günlük `spans` üzerinden mi, yoksa yalnız ileriye
  dönük mü?

Bu sorular cevaplanmadan dilim yazılamaz. **`/spec` ile ayrı ele
alınmalı.**

**Efor:** 4+ · **Şema:** EVET

---

### D9 — keyset sayfalama (ölçülmedi, KARAR BEKLİYOR)
Operatörün UX brief'i keyset sayfalamayı karara bağladı. Traces
teşhisinde ise **ölçülmüş bir sorun değil**: `count=skip` zaten
varsayılan ve derin OFFSET yalnız 10+ sayfa ilerlenirse ısırıyor.

**Önce ölç:** sayfa 1 ile sayfa 20'nin `read_rows`/`p95` farkı. Fark
yoksa dilim düşer; varsa D8 ile birlikte tasarlanmalı (keyset kursörü
`(time, trace_id)` çifti — D8'in ORDER BY'ı ile aynı).

**Efor:** 3 · **Şema:** hayır (D8'den sonra)

---

## Açık risk — planın tamamını etkiliyor

**Dağıtık DDL kuyruğu tıkalı** (operatör ölçümü, 2026-08-03:
`system.distributed_ddl_queue`'da 50 satır, hepsi `Inactive`).

v0.9.604-608 boot'u buna RAĞMEN mümkün kıldı ama kuyruk düzelmeden
**hiçbir şema değişikliği uygulanamaz** — yani **D5b ve D8 bloke**.
Şemaya dokunmayan D3 ve D9 etkilenmiyor.

Kuyruk düzelene kadar sıra: **D3 → (prod ölçümü) → D9 kararı**.

---

## Ölçüm sözleşmesi

Her dilim sonrası, aynı pencerede, `system.query_log` **medyanı**:

| Metrik | Bugün (6h, ham yol) | Hedef |
|---|---|---|
| `read_rows` | 1.128.772 | — dilime göre |
| `read_bytes` | 76.90 MiB | — |
| oran | 22.575:1 | < 1.000:1 |
| p95 | prod'da ölçülecek | < 1.5s |
| TTFB | ölçülmedi | < 800ms |

Tek ad-hoc zamanlama kabul edilmiyor: `ms` önbellek sıcaklığıyla 2-3×
oynuyor (bu turda bir vakada dizi yolu materialized yoldan *hızlı*
göründü). Karar `read_rows`/`read_bytes` üzerinden verilir.
