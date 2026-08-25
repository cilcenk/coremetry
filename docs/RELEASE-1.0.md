# v1.0.0 Kesim Prosedürü

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
     dizisini bağımsız yürütür. Tek-pod'a indirip yükseltmek en güvenli yol.

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

## §4 — Kesim GÜNÜ operatörden gereken cevaplar

Bunlar repodan OKUNAMAZ; kesimden önce cevaplanmalı:

- **Prod'un koştuğu sürüm ≥ v0.9.624 mü?** Boot yolundaki
  `ALTER TABLE spans` yalnız `promotedAttrs` listesi değiştiğinde ateşler;
  liste en son v0.9.624/625'te değişti. Prod ≥ 624 ise 1.0 yükseltmesinde
  spans'a hiçbir ifade gitmez. (İlk yazımın "spans'a dokunan ALTER yok"
  cümlesi KOŞULSUZ doğru değil — muhafız prod'un cluster kipini kapsamıyor.)
- **0009 durumu:** `GET /api/admin/state-unify/status`
- **0010 AŞAMA A ADIM 5 kapandı mı:** `GET /api/admin/state-repart/status`
- **Prod replica sayısı** — >1 ise §1.8'deki boot DDL yarışı gerçek.
- v0.9 zincirinin son sürümü ne? (kesim anındaki HEAD)
