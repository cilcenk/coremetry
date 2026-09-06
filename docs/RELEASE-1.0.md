# v1.0.0 Kesim Prosedürü

> **ERTELENDİ — operatör kararı 2026-08-25:** "0.10.1 diye devam edelim,
> 1'e geçmeyelim." Zincir `v0.9.1388`'de kapandı ve `v0.10.X` olarak
> sürüyor. Bu dosya SİLİNMEDİ: 2026-08-25'te HEAD'e karşı yeniden
> ölçüldü, §0.1'in canlı kapıları prod'da KOŞULDU ve altısından beşi
> geçti — o emek 1.0 günü tekrar harcanmasın.
>
> **O gün geçerli kalanlar** (bugün ölçüldü):
> - DDL kuyruğu temiz (4640 kayıt, hepsi `Finished`), bekleyen mutasyon 0
> - Altı MV probe'u ve wrapper kayması kontrolü: hepsi GEÇTİ
> - `v0.9.1385..HEAD` filtresiz delta: kodda tek DDL satırı yok
>
> **O gün YENİDEN sorulacaklar:** §0.1'in tamamı (canlı durum bayatlar) ve
> §4'ün operatör soruları. Ve §0'un kendi uyarısı: ön-durum bloğuna
> GÜVENME, komutları koştur.
>
> **Kapanmamış tek kalem:** prod `coremetry-secret` içindeki `jwtSecret`
> yer tutucu değerde görüldü (`CHANGE_ME_…`). Hangi ortamda olduğu
> netleşmedi; JWT imzalama anahtarı olduğu için bilen biri parolasız admin
> token üretebilir. Rotasyon oturumları düşürür, o yüzden bir rollout
> penceresine denk getirilmeli.

Amaç: "kes" dendiği gün 1.0'ın TEK oturumda, sürprizsiz çıkması.

> **Bu dosya kesim günü KOŞULUR.** Yani içindeki her yanlış cümle
> uygulanır. İlk yazımı (2026-07-30, v0.9.437) tam bu yüzden tehlikeliydi:
> "Ön durum" bloğu o günün gerçeğini donduruyordu ve 945 sürüm boyunca
> güncellenmedi. 2026-08-25'te (v0.9.1382) HEAD'e karşı yeniden ölçüldü;
> dört ön-durum iddiasından **ikisi yanlış** çıktı. Bir sonraki okur:
> aşağıdaki "Ön durum" bloğuna GÜVENME, §0'daki komutları koştur.

## §0 — Ön durumu HER SEFERİNDE yeniden ölç

Kopyala-yapıştır, çıktıyı oku, sonra devam et:

```bash
# 1. v1.0 tag'i gerçekten yok mu (paralel oturum çakışması dersi)
git fetch --tags && git tag -l 'v1.0*' && git ls-remote origin 'refs/tags/v1.0*'

# 2. Bugün kaç göç var? (dokümanın "5 göç" varsayımı bayatlar)
ls migrations/

# 3. Boot yolunda spans'a giden ALTER var mı, ve tetikleyicisi ne?
grep -n "ALTER TABLE.*spans\|promotedAttrNeedsRepair" internal/chstore/promoted_attr.go

# 4. Zorunlu env eklendi mi? (log.Fatal sayısı ve COREMETRY_ okumaları)
grep -c "log.Fatal\|os.Exit" main.go
git diff v0.9.437..HEAD -- internal/config/ | grep -iE '^\+.*COREMETRY_'
```

## §0.1 — CANLI kapılar (kesimden ÖNCE, prod CH üzerinde)

Bunlar repodan okunamaz ve `go test` görmez. Hiçbiri kesimi teknik olarak
bloklamaz, ama biri kırmızıysa ROLLOUT ertelenmelidir.

**1. DDL kuyruğu boş mu?** Ertelenen ifadeler (24 MV CREATE + ~13 alter,
pod başına) oraya gider.
```sql
SELECT status, count() FROM system.distributed_ddl_queue GROUP BY status;
SELECT count() FROM system.mutations WHERE NOT is_done;
```
Yüzlerce Active/Inactive varsa **ERTELE** — sıradaki gerçek şema
değişikliğini (0010 ADIM 5, rollup 0005) de geciktirir.

**2. MV drop riskini sıfırla.** Altı probe de DOLU dönmeli; biri eksikse
o MV'nin upgrade dalı tetiklenir ve `dropCombinedMV` ANINDA drop eder
(recreate ertelenir → okuma-hatası penceresi):
`service_summary_5m.apdex_satisfied_state` · `db_summary_5m.db_name` ·
`db_caller_summary_5m.db_name` · `db_statement_summary_5m.slow_exemplar_state` ·
`trace_summary_5m.entry_route_state` · `duration_q_state` tipi **TDigest**
(TDigest OLMAYAN satır dönmemeli).

**3. `db_statement_summary_5m` wrapper kayması.** Bu upgrade defer
kapısını BYPASS ediyor — her boot senkron `conn.Exec` DROP+CREATE
(`store.go:4118`). `_local`de kolon VAR ama wrapper'da YOKSA, roll'ün her
adımında `/slow-queries` UNKNOWN_TABLE penceresi görür. Varsa elle bir kez
düzelt, sonra deploy.

**4. jwtSecret paylaşılıyor mu? — TEK GERÇEK LATENT HATA.**
`secret.yaml` tüm dosyayı `{{- if not .Values.secrets.existingSecret -}}`
ile sarıyor, yani çok-pod `fail` muhafızı existingSecret yolunda HİÇ
render edilmiyor; `deployment-distributed.yaml` anahtarı `optional: true`
okuyor ve boşsa her pod EPHEMERAL bir anahtar üretiyor. `sessionAffinity:
ClientIP` bunu bugüne dek maskeledi — **rollout maskeyi kaldıran olayın ta
kendisi.**
```bash
kubectl -n <ns> get deploy <rel>-api -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="COREMETRY_JWT_SECRET")].valueFrom.secretKeyRef}'
kubectl -n <ns> get secret <ad> -o jsonpath='{.data.jwt-secret}' | base64 -d | wc -c
```
Sıfır/yoksa: 4 api pod'u farklı anahtarla imzalıyor. api loglarında
`no jwt_secret configured` satırını da ara. **Paylaşılan anahtar
konmadan rollout BAŞLATMA.**

**5. Filtresiz delta'yı yeniden ölç.** Yol-filtreli `git diff` yetmez —
DDL üç yolun dışında da var (`api.go` putRetention ALTER TTL,
`clickhouse_health.go`, `main.go` reset-schema):
```bash
git diff <prod-tag>..HEAD | grep -E '^\+.*(ALTER|CREATE|DROP|MATERIALIZED)'
```

## §1 — Kesim adımları (sırayla, tek oturum)

1. **§0'ı koştur.** `v1.0*` boş değilse DUR.

2. **Zincir yazımını çevir — DÖRT satır, biri değil.**
   `CLAUDE.md` satır **37** (Versioning), **81** (`ship as v0.9.X+1`),
   **86** (`tag v0.9.X`), **92** (Commit format). CLAUDE.md her oturuma
   enjekte ediliyor; çevrilmeyen satır kesimden sonra hâlâ v0.9 ile ship
   etmeyi EMREDER.

3. **⚠ SÜRÜM ÜRETEN MAKİNEYİ ÇEVİR — bu adım atlanırsa kesim sessizce
   çatallanır.**
   `.claude/skills/release/SKILL.md:39` sonraki sürümü şöyle hesaplıyor:
   ```
   git tag --sort=-v:refname | grep -E '^v0\.9\.[0-9]+$' | head -1
   ```
   Regex'i `^v1\.0\.[0-9]+$` yap. Yapılmazsa 1.0.0'dan sonraki ilk
   `/release` **v0.9.1383** hesaplar — ve skill'in kendi "monotonik / tag'i
   tekrar kullanma" kontrolü YEŞİL kalır, çünkü gerçekten yeni bir tag'dir.
   Depoda `v0.9` yazımını regex olarak bekleyen tek çalıştırılabilir yer
   burasıdır.

   Aynı taramada şablon yazımlarını da çevir (bunlar çalıştırılabilir
   değil, ama reçetedir ve uygulanır): `.claude/skills/bugfix/SKILL.md`,
   `kuyruk/SKILL.md`, `api-route/SKILL.md`, `otlp-converter/SKILL.md`,
   ve `.claude/agents/coremetry-feature-shipper.md` (bu sonuncusu ayrıca
   `v0.5.X` ve `make docker-up` diyor — iki kat bayat).

4. **`charts/coremetry/Chart.yaml`: `version` VE `appVersion` → `1.0.0`**
   (helm-chart skill kuralı: ikisi birlikte).
   ⚠ İkisi BUGÜN ayrışık (`0.9.347` / `0.9.346`, ayrışma v0.9.1106'da tek
   satırlık kayma). Sonuç bugün canlı: checkout'tan `helm install` 1000+
   sürüm eski imaj çeker. Bu adım onu da kapatır.
   Chart sürümünü app'ten BAĞIMSIZLAŞTIRMA — `release.yml`'in
   "Sync Chart appVersion" adımı her tag'de ikisini de eşitliyor.

5. **Gate zinciri — CI'ın koştuğunun tamamı:**
   ```bash
   cd frontend && npx tsc --noEmit && npx vitest run && cd ..
   go build ./... && go vet ./... && go test ./...
   make audit                       # 🔴 = kesme
   ```
   `vitest` ATLANAMAZ: 325 test dosyası başka hiçbir yerde ölçülmüyor ve
   kırmızı bir vitest kesim gününde görünmez, yalnız tag push edildikten
   SONRA CI'da patlar. (`-race` ve `-tags=chsmoke` CI'da koşuyor; yerelde
   isteğe bağlı.)

6. **Commit `v1.0.0 — release` + tag `v1.0.0`**, sonra push + push --tags.
   `git ls-remote origin 'refs/tags/v1.0*'` ile TEK tag doğrula.
   ⚠ Commit ve tag'i AYRI komutlarda koşuyorsan her birinin çıkış kodunu
   OKU. v0.9.1381'de paralel bir ajan `.git/index.lock` tutarken commit
   başarısız oldu, zincirin devamı koştu ve tag bir ÖNCEKİ sürümün
   commit'ine bağlanıp uzağa gitti.

7. **İmaj ve chart — CI YAPIYOR, elle yapma.** `release.yml` tag push'unda
   build-arg'ları damgalıyor (`VERSION`, `VITE_APP_VERSION`) ve chart'ı
   paketleyip `oci://ghcr.io/<owner>/charts` altına push ediyor. Elle
   `docker build` hem CI ile çakışır hem `/release` skill'inin `make image`
   adımıyla çelişir. CI kırmızıysa ilgili job'ı yeniden koştur.

8. **Deploy.** Prosedürün ilk yazımında bu yarı HİÇ YOKTU:
   - Coremetry Deployment `maxUnavailable: 0` KALIR — düşerse OTel
     collector rollout'ta wedge olur ("zero addresses").
   - Rollout sonrası collector'ı **koşulsuz** restart et. (İlk yazım bunu
     "zero-addresses belirtisi yoksa gereksiz" diye opsiyonel gözleme
     indirmişti; operatörün duran kuralı bunun tersi.)
   - ⚠ **MV drop/recreate penceresi.** `store.go`'daki birleşik-MV
     upgrade'lerinde `dropCombinedMV` DOĞRUDAN `s.conn.Exec` kullanıyor
     (ANINDA drop), recreate ise `s.execDDL`den geçtiği için ERTELENİYOR.
     Aradaki pencerede o MV'yi okuyan uçlar hata döner. Kodun kendi yorumu
     bunu "canlı doğrulamada görüldü" diye kaydediyor.
   - ⚠ **Boot DDL'i lider-kapılı DEĞİL.** `s.migrate(ctx)` `chstore.New`
     içinde koşulsuz; Redis mutex yok. `replicaCount > 1` ya da
     `deployment.mode=distributed` ise her pod aynı ALTER / drop+recreate
     dizisini bağımsız yürütür.

     ⚠ **TEK-POD'A İNDİRMEYİN.** İlk yazımım bunu "en güvenli yol" diye
     öneriyordu; bu topolojide (ingest 12 replica) AKTİF ZARARLI: 12→1,
     `maxUnavailable: 0`'ın önlemek için var olduğu zero-addresses
     koşuluna ulaşmanın tek yoludur, ve 1→12 dönüşü onbir pod'u aynı anda
     boot ettirir.

     Yarışı zaten `maxUnavailable: 0` + `maxSurge: 1` sınırlıyor: üç rol
     ayrı Deployment ve her biri TEK pod rolluyor, yani en fazla üç
     eşzamanlı yeni pod olur — "16 pod aynı anda" değil.

     Asıl koruma ise DDL'in küme kipinde ERTELENMESİ
     (`deferMigrationDDL(clusterMode, spansExists)`): ifadeler boot'u
     bloklamaz, arka planda kuyruğa girer. Bu yüzden aşağıdaki DDL-kuyruk
     kapısı (§0.1) rollout'tan ÖNCE koşulmalı.

9. **Rollback.** spans üzerindeki DROP COLUMN mutasyonu ve MV inner-drop
   TEK YÖNLÜ. v1.0.0'dan v0.9.X'e dönülürse eski binary'nin ne göreceği
   yazılı değil — dönmeden önce ölç. (0003'ün rollback'i var, şema/MV
   tarafının yok.)

## §2 — Smoke checklist (deploy sonrası ilk 10 dakika)

- [ ] `/api/version` → buildVersion `v1.0.0`, `overridden:false`
- [ ] **Ertelenmiş DDL özeti** hatasız ("N uygulandı, 0 başarısız") +
      `system.distributed_ddl_queue` ve `system.mutations` temiz.
      ⚠ İlk yazım "boot logunda `[chstore]` ALTER satırları hatasız"
      diyordu; prod ertelemeli kipte o satırlar HİÇ BASILMIYOR ve arka plan
      goroutine'i hataları `continue` ile yutuyor. O madde doğrulama gücü
      taşımıyordu.
- [ ] /inbox doluyor; öncelik şeridi ve tür sayaçları tutarlı
- [ ] /problems (Exceptions) tam liste; bir grup detayı + Explain root cause
- [ ] /deploys zaman çizelgesi dolu; etki rozetleri en yeni deploylarda
- [ ] /services → bir servis → Overview RED + zoom çift-tık geri
- [ ] CoSRE: "dün gece neler oldu?" + bir takip sorusu ("peki X?")
- [ ] /ai sayfası çağrıları kaydediyor (explain + auto-explain yüzeyleri)
- [ ] Collector'lar restart sonrası metrik/trace basıyor

## §3 — 1.0 sürüm notlarına girmesi gerekenler

### Göçler — üç ayrı durum, karıştırma

1. **TAZE KURULUM hiçbir göç istemez.** Yeni kurulum 0009/0010'un
   düzelttiği şemayla doğar.

2. **YÜKSELTME — iki göç ZORUNLU.** İlk yazımın "Migration adımı
   gerektirmez" cümlesi buradan yanlıştı: `CREATE TABLE IF NOT EXISTS`
   var olan tabloya DOKUNMAZ, dolayısıyla yükseltilen kurulum
   kendiliğinden düzelmez ve fast-path gibi kendini kapatan bir mekanizma
   yoktur.
   - **0009 (state unify)** — yalnız ÇOK-SHARD'lı kurulumlarda zorunlu.
     Uygulanmazsa state tabloları (problems, alert_rules, users,
     system_settings, dashboards, incidents…) shard başına ayrı
     replikasyon grubundadır ve uygulama hangi host'a bağlanırsa onun
     dilimini görür (prod ölçümü: problems 633.236 ↔ 4.169). Boot yalnız
     nötr bir INFO basar, "verin bölünmüş" DEMEZ.
     Sihirbaz: `/api/admin/state-unify/{preflight,status,apply,cleanup}`.
   - **0010 (state repartition)** — TÜM yükseltmelerde zorunlu, **tek-node
     dahil**. problems/anomaly_events `PARTITION BY toDate(started_at)` +
     `ORDER BY id` taşıyor; started_at ORDER BY'da olmadığı için aynı id
     ikinci bir gün-partition'ına düştüğünde ReplacingMergeTree onu
     toplayamaz. Doğruluk TEK BİR SUNUCU AYARINA asılı:
     `do_not_merge_across_partitions_select_final=1` açıldığı an FINAL
     bayat satır servis eder ve started_at P1 açık-saat eşiğini beslediği
     için yaşlanmış bir problem sessizce geri iner. Boot uyarı basar ama
     DDL göndermez. Önkoşulu 0009.
     Sihirbaz: `/api/admin/state-repart/{preflight,status,apply,finalize,cleanup}`.
     Tek-node'da elle, `ON CLUSTER` cümleleri çıkarılarak.

3. **0001-0008 OPSİYONEL performans katmanı** — uygulanmazsa fast-path'ler
   kendini kapatır, `/api/rollup/red` 424 döner.
   **TEK İSTİSNA 0007:** 0002'yi v0.9.626 ÖNCESİ uygulamış kurulum 0007'yi
   de koşmalı, yoksa geniş rollup'ın channel/function boyutu sessizce boş
   kalır — bunu ölçen hiçbir probe YOK.

### Env

v0.9.355'ten beri yeni ZORUNLU env yok (2026-08-25'te yeniden doğrulandı).
Tek yeni üretim env'i **`COREMETRY_CH_MEM_FRACTION`** (v0.9.975): CH sorgu
bellek tavanı oranı, varsayılan `0.6`, `[0.1, 0.9]` arasına sıkıştırılır,
geçersiz değer FATAL değil WARNING.

**`COREMETRY_CH_PARALLEL_VIEWS`** (v0.10.511, dış denetim C6 ölçüm anahtarı,
İSTEĞE BAĞLI): spans INSERT'inde 19 MV'nin paralel itişi
(`parallel_view_processing`). Varsayılan `1` (v0.10.240 davranışı); `0`
sıralı itiş. Yalnız A/B ölçümü için: 24 saat `1`, 24 saat `0`; `system.query_log`
spans INSERT tepe/p99 belleği ve `pushing to view` zaman aşımları
karşılaştırılır. Karar kuralı: tepe bellek ≥ %30 düşer ve zaman aşımı artmazsa
`0`; artarsa `1` (çift satır riski v0.10.240'ın sebebiydi). Etkin değer boot
logunda `[chstore] parallel_view_processing=…` satırında.

## §4 — Kesim GÜNÜ operatörden gereken cevaplar

### 2026-08-25 kesimi için CEVAPLANDI

| Soru | Cevap | Sonucu |
|---|---|---|
| Prod sürümü | **v0.9.1385** | promotedAttrs **kolon** zinciri ateşlemez ⚠ aşağıdaki düzeltmeye bak |
| 0009 | **uygulandı** | state tabloları birleşik |
| 0010 | **uygulandı**, `_old` silinmesi bekliyor | şema taşındı; cleanup hijyen |
| Replica | **api 4 · ingest 12 · worker 1** = 16 pod | Roll rol-başına TEK pod (maxSurge 1) → en fazla 3 eşzamanlı |

**Ve kesim için belirleyici olan ölçüm:** prod v0.9.1385'te, 1.0.0 ise
HEAD'den kesiliyor ve `v0.9.1385..HEAD` şema deltası **SIFIR** —
`internal/chstore/`, `internal/chmigrate/`, `migrations/` altında tek bir
`CREATE`/`ALTER`/`DROP`/MV satırı yok, değişen dört dosya frontend.

Yani yukarıdaki DDL / göç / MV-penceresi uyarılarının hiçbiri BU kesimde
taşınacak bir şey bulmuyor. Uyarılar dosyada KALIYOR çünkü bir sonraki
kesim ESKİ bir sürümden yükseltebilir — ama bugünün riski onlar değil.

⚠ **"Delta sıfır ⇒ boot DDL göndermez" ÇIKARIMI YANLIŞTIR.** Boot git
diff'e bakmıyor, canlı `system.columns` / `system.tables` probe'larına
bakıyor. Üstelik `mvs` dilimi `planDDL` elemesinden HİÇ geçmiyor
(`store.go:3963` doğrudan `execDDL`): store.go'daki 24 adet
`CREATE MATERIALIZED VIEW IF NOT EXISTS` her boot çıkar. Elenemeyen
alter'lar da var (5× `spans MODIFY COLUMN`, 4× `spans ADD INDEX`,
`logs ADD INDEX`, `trace_snapshots MODIFY TTL`, 2× `system_settings
ALTER … DELETE`) — `ddl_skip_existing.go` yalnız `CREATE … IF NOT EXISTS`
ve `ADD COLUMN IF NOT EXISTS` kalıplarını tanıyor.

Bunu güvenli kılan şey delta DEĞİL, ERTELEME + rol-başına-tek-pod. Ama
ertelenen ifadeler DDL kuyruğuna gider, o yüzden §0.1 kapısı şart.

### Bir sonraki kesimde yeniden sorulacaklar

Bunlar repodan OKUNAMAZ; kesimden önce cevaplanmalı:

- **Prod'un koştuğu sürüm ≥ v0.9.624 mü?** ⚠ Bu sorunun ilk yazımı
  YANLIŞ bir sonuç çıkarıyordu ("spans'a hiçbir ifade gitmez"). Kod
  önceki DAĞITILMIŞ sürümü hiç okumuyor; 624 yalnız `promoted_attr.go`
  içinde bir yorum. `promoted_attr.go:294-296`'daki
  `ALTER TABLE spans ADD INDEX IF NOT EXISTS` KOŞULSUZ ve `execDDL`
  üzerinden gidiyor (planDDL elemesine girmiyor). Sürüm bilgisi yalnız
  `promotedAttrs` KOLON zincirinin ateşleyip ateşlemeyeceğini söyler;
  liste en son v0.9.624/625'te değişti. Prod ≥ 624 ise 1.0 yükseltmesinde
  spans'a hiçbir ifade gitmez. (İlk yazımın "spans'a dokunan ALTER yok"
  cümlesi KOŞULSUZ doğru değil — muhafız prod'un cluster kipini kapsamıyor.)
- **0009 durumu:** `GET /api/admin/state-unify/status`
- **0010 AŞAMA A ADIM 5 kapandı mı:** `GET /api/admin/state-repart/preflight`
  → `stage` alanı (`stage:"B"` = ADIM 5 bekliyor).
  ⚠ `/status` DEĞİL: o uç süreç-içi bir anlık görüntü döndürüyor
  (pod-yerel). 4 api replica'da hangi pod'a düştüğüne göre
  `{"running":false,"total":0}` gelir — yanlış bir "temiz" cevabı.
- **Prod replica sayısı** — >1 ise §1.8'deki boot DDL yarışı gerçek.
- v0.9 zincirinin son sürümü ne? (kesim anındaki HEAD)
