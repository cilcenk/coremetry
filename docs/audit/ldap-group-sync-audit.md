# LDAP/AD Grup Senkronizasyonu — Audit (kod yok, onay bekliyor)

2026-07-12 · Her iddia kod okunarak doğrulandı (dosya:satır). DN örnekleri
sanitize edilmiştir (`DC=corp,DC=example`).

## 1. Mevcut kimlik zinciri

**Mevcut durum (kod-doğrulanmış):**
- JWT üç claim taşır: `uid`, `email`, `role` (auth.go:50-55); login CH `users`
  satırından üretir: `Issue(user.ID, user.Email, user.Role)` (api.go:5200).
- LDAP login'de kimlik = **e-posta**: `finalEmail` = dizinin `EmailAttribute`
  değeri, varsayılanı `userPrincipalName` (ldap.go:157-163); lowercase+trim
  normalize (api.go:5160, 5374). Girilen ad hem aynen hem UPN local-part
  olarak denenir (api.go:5351-5354).
- **`sAMAccountName` HİÇBİR YERDE persist edilmiyor**: `chstore.User`'da
  username/DN alanı yok (user.go:9-46); `res.User.Username` login sonrası
  atılıyor (api.go:5408-5419). `GetUserByEmail` exact match, lowercase
  yazımına dayanır (user.go:51-57).

**Sonuç → öneri → gerekçe:** Hedef çıktı `users: [sAMAccountName…]` bugünkü
kimlikle (e-posta/UPN) **doğrudan eşleşmez**. Öneri iki katman:
(a) senkron her kullanıcı için `sAMAccountName` + `userPrincipalName` +
`mail` üçlüsünü çeker; `Snapshot.UserGroups` anahtarları lowercase tüm
alias'larla kurulur (UPN'in local-part'ı dahil — login'deki aynı kural);
(b) kalıcı çözüm: `users` tablosuna `ldap_username LowCardinality(String)`
kolonu (login'de zaten elde, sadece yazılmıyor). Gerekçe: authz hot path
O(1) tek anahtar ister; e-posta kanonik kimlik kalır, kırılma olmaz.

## 2. Paket yerleşimi + arayüz

**Mevcut durum:** LDAP istemcisi, TLS/bind/config, `resolveUserFilter`,
`LDAP_MATCHING_RULE_IN_CHAIN` kullanımı **zaten `internal/ldap`'ta**
(ldap.go:167-173); `internal/auth` yalnız JWT/rol.

**Öneri:** `internal/auth/ldap` YERİNE **`internal/ldap/sync.go`** (aynı
paket). Gerekçe: yeni paket ikinci bir bağlantı/config kaynağı doğurur;
dial+TLS+bind+sanitize altyapısı tek yerde kalmalı (ldap.go:476-506).

**Arayüz (önerilen iyileştirmeyle):**
```go
type Syncer interface{ Sync(ctx context.Context) (*Snapshot, error) }

type Snapshot struct {
    Groups     map[string]Group    // anahtar: groupUID (bkz. §5)
    UserGroups map[string][]string // anahtar: normalize alias (bkz. §1) → groupUID listesi
    SyncedAt   time.Time
    Stats      SyncStats           // groups, users, pages, duration
}
type Group struct {
    UID string   // objectGUID (uuid string)
    CN  string
    DN  string
    Users []string // sAMAccountName, sıralı
}
```
Değişiklik gerekçesi: `SyncedAt`/`Stats` gözlemlenebilirlik (§9) ve UI
durum kartı için; `Group.UID/CN/DN` ayrımı §5 kararını taşır. Hata
sözleşmesi: `Sync` kısmî sonuç DÖNDÜRMEZ — sayfa hatasında tur iptal,
eski snapshot korunur (§7).

## 3. Kütüphane

**Mevcut durum:** `github.com/go-ldap/ldap/v3 v3.4.13` **zaten go.mod'da**
(go.mod:11), tek import noktası `internal/ldap`. Lisans **MIT** (module
cache LICENSE doğrulandı). `SearchWithPaging` API'si sürümde mevcut
(search.go:503) — repo'da henüz kullanılmıyor (tüm aramalar düz `Search`).

**Öneri:** aynı kütüphane, yeni bağımlılık yok. Gerekçe: bakımı aktif
(v3.4.x hattı), paging + matching-rule ihtiyaçlarının ikisini de karşılıyor.

## 4. Config şeması

**Mevcut durum:** `ldap.Config` system_settings `key='ldap'` JSON blob'unda
(ldap.go:391); CA **dosyadan değil** UI textarea PEM'inden (ldap.go:476-481);
**bind şifresi düz metin blob içinde** — bilinçli deployment kararı olarak
belgeli (ldap.go:13-15), API'ye asla geri dönmez (`__SET__` sentinel,
ldap.go:181-191). GC 3268/3269'a dair kod/hint yok; `Port` serbest olduğundan
3269+UseTLS teknik olarak çalışır ama test edilmemiş.

**Öneri:** mevcut `ldap` blob'u genişler (ikinci bağlantı tanımı YOK);
operatör şartı için `caFile` / `bindPasswordFile` / `bindPasswordEnv`
referans alanları eklenir — dolular inline alanları ezer, boşlar geri-uyum:

```yaml
ldap:
  url: ldaps://dc.corp.example:3269   # GC portu desteklenir (§AÇIK-1)
  caFile: /etc/coremetry/ldap-ca.pem  # boşsa mevcut caCert (PEM) alanı
  bindDN: CN=svc-coremetry,OU=Service,DC=corp,DC=example
  bindPasswordFile: /etc/coremetry/ldap-bind.pass  # veya bindPasswordEnv
  groupSync:
    enabled: true
    syncInterval: 30m
    timeout: 60s
    pageSize: 500                     # AD MaxPageSize=1000 altı, konfigüre edilebilir
    usersQuery:
      baseDN: OU=Users,DC=corp,DC=example
      scope: sub
      filter: "(objectClass=user)"    # {{username}}'sız ön-filtre semantiği (resolveUserFilter emsali)
    userNameAttribute: sAMAccountName
    groupMembershipAttribute: memberOf  # zincir kuralıyla kullanılır, §zorunlu-karar
    groups:
      includePrefixes:                # DN/OU prefix whitelist
        - "OU=DistributionGroups,OU=Groups,DC=corp,DC=example"
      excludePrefixes: []
```

**Zorunlu kararların gerekçesi (istenen):**
- **Zincir-tabanlı üyelik:** grup başına `(memberOf:1.2.840.113556.1.4.1941:=<groupDN>)`
  filtreli **paged USER araması** → dönen kullanıcılar o grubun (nested dahil)
  efektif üyeleri; ters çevirme yerine doğrudan group→users kurulur.
  `member;range=0-1499` retrieval'ı ve recursion tamamen devre dışı. Aynı
  OID login yolunda zaten kanıtlı (ldap.go:172).
- **LDAPS+GC 3269:** GC read-only ve forest-geneli — çoklu-domain ormanda
  tek porttan tüm kullanıcılar. `memberOf` ve `sAMAccountName` GC partial
  attribute set'inde replikedir. (Doğrulama: §AÇIK-1.)
- **Sayfalama:** `SearchWithPaging(req, pageSize)` (v3.4.13 hazır);
  varsayılan 500 — AD MaxPageSize 1000 sınırının altında güvenli.

## 5. Grup kimliği: DN vs objectGUID

**Mevcut durum:** rol eşlemesi DN/CN substring üzerinden (mapRole,
ldap.go:856-887) — kimlik kavramı yok.

**Öneri:** satır kimliği = **objectGUID** (octet-string → UUID string);
DN + CN görüntü/filtre alanı olarak yanında taşınır. Gerekçe: AD'de grup
rename/move'da DN değişir, objectGUID değişmez — ReplacingMergeTree
anahtarı stabil kalır, taşınan grup çift satır üretmez. Trade-off: GUID
insan-okur değil (UI hep CN/DN gösterir); whitelist/blacklist ve
GroupRoleMap **DN/CN üzerinden çalışmaya devam eder** (geri uyum),
GUID yalnız depolama/snapshot anahtarıdır.

## 6. ClickHouse şeması

**Mevcut durum:** state şablonu `ReplacingMergeTree(version) ORDER BY id`
+ FINAL okuma (users store.go:558-571, saved_views store.go:756-766).
`Array(String)` RMT içinde emsalli (topology_root_flows_5m,
store.go:1398-1408); admin-CRUD tablolarında liste alanları JSON-String
emsali de var (custom_links).

**Öneri:**
```sql
CREATE TABLE IF NOT EXISTS ldap_groups (
  group_uid  String,                          -- objectGUID (uuid str)
  dn         String,
  cn         LowCardinality(String),
  users      Array(String),                   -- sAMAccountName, sıralı
  deleted    UInt8 DEFAULT 0,                 -- tombstone (dizinden kaybolan grup)
  synced_at  DateTime64(9) DEFAULT now64(9),
  version    UInt64 DEFAULT toUnixTimestamp64Nano(now64(9))
) ENGINE = ReplacingMergeTree(version) ORDER BY group_uid
```
Gerekçe: RMT = whole-row upsert + FINAL okuma, mevcut disiplinle birebir;
TTL **gerekmez** (küçük state seti; audit-geçmişi hedef değil). Silinen
grup = tombstone (saved_views emsali) — her tur tam set yazılır, dönmeyen
UID'lere `deleted=1`. `Array(String)` seçimi: `has(users, ?)` ile
sorgulanabilir; JSON-string'e üstünlüğü bu. 10k+ üyeli dev gruplar için
üst sınır: §AÇIK-4.

## 7. Cache / fail-stale

**Mevcut durum:** lock-free hot-path swap emsali VAR — `acache.Store.policy
atomic.Value` (acache.go:232-279); periyodik worker + leader emsali
`LeaderHolder` (cache/leader.go) + `runExceptionRefresher` tick şablonu
(main.go:1150-1210); multi-pod config tazeleme `StartConfigRefresh` 30s
(ldap.go:398-415).

**Öneri / garanti:**
- Engine içinde `atomic.Pointer[Snapshot]`; **yalnız tamamen başarılı**
  `Sync` sonunda `Store()` — sayfa/bağlantı hatasında tur iptal, pointer'a
  dokunulmaz, `last_success_timestamp` değişmez → fail-stale otomatik.
- Boot hydrate: CH `ldap_groups FINAL` → snapshot; CH boşsa snapshot=nil
  semantiği "senkron henüz yok" — grup-tabanlı yetki kuralları
  değerlendirilmez, mevcut davranış (DefaultRole) sürer; **boş snapshot'a
  düşülüp herkes kilitlenmez** çünkü nil ≠ boş-üyelik.
- Multi-pod: sync yalnız leader'da (LeaderHolder, 90s TTL emsali);
  follower'lar 30s'de CH'den re-hydrate (StartConfigRefresh deseni).

## 8. Grup → rol eşlemesi

**Mevcut durum:** `GroupRoleMap []GroupRoleMapping` zaten `ldap` blob'unda
(ldap.go:101-108); eşleşme case-insensitive substring, en yüksek rol
kazanır, eşleşmeyen → `DefaultRole` (viewer) (ldap.go:856-887, 174-176).
UI'da düzenleme tablosu mevcut (LdapTab.tsx:262-284). OIDC/trusted-header
tarafında grup→rol yok.

**Öneri:** **aynı yerde kalır** — tek yetki kaynağı, mevcut UI. Senkron
yalnız üyelik verisi getirir; login'deki `mapRole` snapshot'tan gelen grup
DN'leriyle de beslenebilir hâle gelir (kazanç: `SkipMemberOfFetch=true`
kurulumlarda login-anı grup araması tamamen kalkar). Eşleşmeyen grup:
bugünkü sözleşme — role etkisi yok, kullanıcı DefaultRole'da; snapshot'ta
yine görünür (görünürlük ≠ yetki).

## 9. Gözlemlenebilirlik

**Mevcut durum:** `internal/selfobs` tam OTel SDK (noop-safe, selfobs.go:93-99);
CH sorguları `tracedConn` ile otomatik span'li (traced_conn.go:100-151);
sayaç deseni Ingester-atomics → `GetSystemStats` (api.go:1553-1604).
`selfobs.Meter()` için henüz hiç custom çağrı sitesi yok.

**Öneri:** span'lar — tur başına kök `ldap.groupsync` + grup başına alt
span `ldap.search.users` (attr: group_cn, pages, entries); CH yazımı
tracedConn'dan bedava. Metrikler (ilk `selfobs.Meter()` kullanımı — desen
burada kurulur): `ldap_sync_duration_seconds` (histogram),
`ldap_sync_groups_total` / `ldap_sync_users_total` (gauge),
`ldap_sync_errors_total` (counter), `ldap_sync_last_success_timestamp`
(gauge), `ldap_sync_identity_match_ratio` (gauge, §10). Ek: `/admin/stats`
SystemStats'a aynı alanlar (mevcut UI görünürlük deseni).

## 10. Teşhis — identity mismatch erken yakalama

**Mevcut durum:** en olası arıza sınıfı sync-OK-ama-eşleşme-sıfır
(§1'deki sAMAccountName↔email boşluğu). Bugün bunu gösterecek hiçbir
yüzey yok.

**Öneri (üç katman):**
1. **Otomatik overlap raporu:** her tur sonunda `UserGroups` anahtarları ×
   `users` tablosu kimlikleri kesişimi → `identity_match_ratio`; 0 ise
   tek satır WARN log ("sync başarılı ama hiçbir kullanıcı eşleşmedi —
   userNameAttribute/alias eşlemesini kontrol edin").
2. **Dry-run admin ucu:** `GET /api/admin/ldap/groupsync/preview` —
   senkronu YAZMADAN ilk N grup + örnek üyeler + overlap oranı döner
   (ayrı dosyada `registerLdapGroupSyncRoutes`, §11).
3. Inspect paneline "bu kullanıcı snapshot'ta hangi gruplarda" satırı.

## 11. Dosya listesi (api.go büyümüyor ✓)

| Dosya | Durum | Tahmini etki |
|---|---|---|
| `internal/ldap/sync.go` | yeni | ~350 satır — engine, paged chain search, snapshot, atomic swap |
| `internal/ldap/sync_test.go` | yeni | ~250 satır — fake conn testleri (§12) |
| `internal/ldap/ldap.go` | değişir | +~40 — Config groupSync alanları + caFile/bindPasswordFile okuma |
| `internal/chstore/ldap_groups.go` | yeni | ~120 — DDL + Upsert/Hydrate/tombstone (FINAL) |
| `internal/chstore/store.go` | değişir | +~10 — migrasyon listesine CREATE çağrısı |
| `internal/api/ldap_groupsync.go` | yeni | ~150 — registerLdapGroupSyncRoutes: özet GET, sync-now POST (admin+audit), preview GET |
| `internal/api/api.go` | değişir | **+1 satır** (register çağrısı) — büyümüyor |
| `main.go` | değişir | +~15 — worker guard'ında LeaderHolder + engine.Start |
| `frontend LdapTab.tsx` + `lib/types,api` | değişir (faz 2) | +~100 — groupSync ayar bölümü + durum kartı |

## 12. Test planı

**Mevcut durum:** `internal/ldap`'ta saf-fonksiyon testleri var
(applyTeamRegex, resolveUserFilter, team_candidates); `internal/auth`
coverage'ı düşük; LDAP ağ yolu hiç test edilmiyor (conn interface'i yok).

**Öneri:** `sync.go` bağlantıyı dar bir arayüz arkasına alır:
```go
type ldapSearcher interface {
    SearchWithPaging(req *goldap.SearchRequest, pageSize uint32) (*goldap.SearchResult, error)
}
```
Fake impl ile tablo-testler: (1) **paging** — 3 sayfalık fake sonuç tek
listeye iner, pageSize parametresi doğru geçer; (2) **zincir/ters çevirme**
— grup başına user-search sonuçları group→users map'ine doğru kurulur,
include/exclude prefix filtreleri uygulanır; (3) **fail-stale** — 2. tur
hata verdiğinde snapshot pointer'ı ve `SyncedAt` değişmez; (4) **kimlik
normalizasyonu** — UPN→local-part + lowercase alias'ları (§1 kuralları);
(5) **objectGUID parse** — octet-string→UUID; (6) **boot hydrate** — CH
satırlarından (tombstone'lar hariç) snapshot kurulumu; (7) chstore
tarafında tombstone/upsert semantiği saf test.

## AÇIK SORULAR

1. **GC 3269 doğrulaması:** kod engel koymuyor ama gerçek bir AD Global
   Catalog'a karşı hiç test edilmedi; GC partial attribute set'inde
   `memberOf`/`sAMAccountName` replike varsayımı ortamınızda doğrulanmalı
   (tek `ldapsearch` ile kontrol edilebilir — komutu implementasyon
   onayında veririm).
2. **Bind şifresi:** şartın "dosyadan/env'den, asla config'e gömülmez" —
   mevcut ürün davranışı system_settings blob'u (bilinçli karar,
   ldap.go:13-15). Öneri her ikisini desteklemek (dosya/env referansı
   doluysa kazanır). Mevcut kurulumun kırılmaması için blob yolu
   kaldırılMAmalı — onaylıyor musun, yoksa dosya/env zorunlu mu olsun?
3. **Grup listesi keşfi:** zincir araması grup BAŞINA koşar; kapsamdaki
   grupların listesi yine de bir kez enumerate edilmeli (öneri:
   `(objectClass=group)` + includePrefixes baseDN'leri üzerinde paged
   arama). Alternatif — grupları hiç enumerate etmeyip yalnız
   GroupRoleMap'te anılanları senkronlamak — daha ucuz ama "keşif"
   hedefini kısıtlar. Hangisi?
4. **Dev gruplar:** 10k+ üyeli grupta `users Array(String)` satırı ~1MB'a
   yaklaşabilir; üst sınır (ör. 50k üye, aşarsa WARN + kes) kabul mü?
5. **`users.ldap_username` kolonu:** §1(b) kalıcı eşleme için onay
   gerekiyor (distributed-safe day-one kuralıyla eklenir).
