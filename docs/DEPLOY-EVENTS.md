# Deploy event'leri — pipeline'dan deploy kaydı (v0.9.1204)

Coremetry deploy'ları normalde telemetriden çıkarır: bir servisin daha
önce görülmemiş bir versiyonu ilk span'ini attığında bu bir deploy
sayılır. Versiyon şu attribute zincirinden okunur:
`container.image.tag` → `k8s.container.image.tag` → `service.version` →
k8s/helm version label'ları.

**Bu zincirin hiçbir halkası değişmiyorsa** (tipik örnek: uzun ömürlü
JBoss'a WAR deploy'u — süreç, container ve attribute'lar aynı kalır)
deploy görünmez ve önce/sonra etki analizi ile kök-neden "ne değişti"
korelasyonu kör kalır.

Çözüm: release pipeline'ınızın son adımı deploy'u KENDİSİ bildirir.

## Azure DevOps (TFS) release pipeline adımı

```bash
curl -sS -X POST "$COREMETRY_URL/api/operator-events" \
  -H "Authorization: Bearer $COREMETRY_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "kind": "deploy",
    "service": "bsa-cashpayment-eft-prod",
    "label": "release-2026.08.21.3",
    "link": "'"$RELEASE_RELEASEWEBURL"'"
  }'
```

- `service` — Coremetry'deki servis adıyla AYNI olmalı (`service.name`).
- `label` — versiyonun kendisi sayılır (parse edilmez); Azure DevOps'ta
  `$(Release.ReleaseName)` ya da `$(Build.BuildNumber)` doğal adaydır.
- `time` — verilmezse "şimdi"; geriye dönük kayıt için unix nanosaniye
  gönderilebilir.
- Token: Settings → API Tokens'tan editor rollü bir token yeterli.

## Ne kazanırsınız

Event kaydı, span-çıkarımlı deploy'larla AYNI kaynak muamelesi görür:

- "What changed" banner'ı ve grafiklerdeki deploy marker'ları,
- /deploys geçmişi (satırda `pipeline` rozetiyle) ve **önce/sonra RED
  etki analizi** (p99/hata/rps kıyası — deploy ANI bilinince attribute
  gerekmez),
- problem satırlarındaki "recent deploy" korelasyonu ve kök-neden
  hipotezindeki deploy kanıtı.

Aynı (servis, versiyon) hem telemetriden çıkarılır hem event'ten
gelirse **event kazanır** — pipeline kaydı gerçeğin kendisidir.
