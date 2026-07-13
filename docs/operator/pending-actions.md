# Operatör bekleyen aksiyonlar (2026-07-13)

Kod tarafı hazır; bu adımlar operatör ortamında yapılır. Tüm değerler
örnek/placeholder — gerçek domain/DN'leri kendi ortamından koy.

## 1. Prod'u v0.8.528'e yükselt — ÖNCELİKLİ (auth bug)

v0.8.528 bir **yetki bug'ını** düzeltiyor: LDAP kullanıcısına Users
sayfasından admin verildikten sonra re-login'de viewer'a düşüyordu.
Prod'daki tüm elle-atanmış roller bu yüzden risk altında.

Prod delta (v0.8.500 → v0.8.528) içindeki operatör-görünür işler:
- v0.8.521 — Trace → "open in Logs" trace'i bulamıyordu (link q= + kolon eşleşmesi)
- v0.8.524 — LDAP Inspect yazılan kullanıcıyı bulmuyordu (filtre sarma)
- v0.8.526/527 — yerleşik AD grup senkronizasyonu (yeni özellik)
- v0.8.528 — LDAP manuel admin grant re-login'de korunuyor (bu bug)

Yükseltme kendi pipeline'ınla; image tag `v0.8.528`.

## 2. COREMETRY_PUBLIC_URL env'i set et — bildirim linkleri için ŞART

Bildirim gövdelerindeki "Open in Coremetry" / problem derin linki
YALNIZ bu env doluyken üretilir. Boşsa link satırı hiç çıkmaz.

```
COREMETRY_PUBLIC_URL=https://coremetry.<senin-domain>
```

Deployment env'ine ekle, pod restart. Link `<PUBLIC_URL>/problems?problem=<id>`
şeklinde üretilir.

## 3. Collector — deploy ailesi için image tag (muhtemelen YAPILDI)

`service.version` prod'da hep aynı geldiğinden deploy geçmişi bozuktu.
Coremetry'nin sürüm çözümü şu zinciri dener:
`service.version → container.image.tag → k8s.container.image.tag →
helm/k8s label'ları`. Ekran görüntünde `container.image.tag=
release.20260710.1` geliyordu — yani k8sattributes bu attribute'u
zaten set ediyorsa **ek bir şey gerekmez**, deploy ailesi çalışır.

Hâlâ boşsa collector'a k8sattributes processor'unda:
```yaml
processors:
  k8sattributes:
    extract:
      metadata:
        - container.image.tag        # deploy sürümünü buradan alır
        - k8s.pod.name
        - k8s.deployment.name
```
Doğrulama (CH'de birden çok distinct değer görmelisin):
```sql
SELECT DISTINCT res_values[indexOf(res_keys,'container.image.tag')]
FROM spans WHERE time >= now() - INTERVAL 1 HOUR LIMIT 10
```

## 4. LDAP grup senkronu — config + Preview (yeni özellik)

Settings → LDAP / AD → **Group sync** bölümü (v0.8.527 UI). Alanlar
(örnek değerlerle):

| Alan | Örnek |
|---|---|
| Enable group sync | ✓ |
| Sync interval | `30m` |
| Users base DN | `OU=Users,DC=corp,DC=example` |
| Groups base DN | `OU=Groups,DC=corp,DC=example` |
| Username attr | `sAMAccountName` |
| Include prefixes | `OU=DistributionGroups,OU=Groups,DC=corp,DC=example` |

**Akış:** doldur → **Save** → form altındaki **Önizle (dry-run)** ile
yazmadan overlap oranına bak → oran iyiyse **⟳ Şimdi senkronla**.
`matchRatio=0` kırmızı uyarısı çıkarsa userNameAttribute/alias eşlemesi
users.email veya users.ldap_username ile tutmuyor demektir.

### GC 3269 doğrulama (bir kez, senkron öncesi)
Global Catalog portunda gerekli attribute'lar replike mi:
```
ldapsearch -H ldaps://<dc-host>:3269 -D "<bindDN>" -W \
  -b "<baseDN>" -s sub "(sAMAccountName=<kendi-kullanıcın>)" \
  sAMAccountName userPrincipalName mail memberOf
```
Dört alan da dolu dönerse GC portu yeterli; `memberOf` boşsa 389/636
(domain portu) kullan — config'te yalnız port değişir.

## 5. Team attribute — tıkla-seç (v0.8.523)

Ekip bilgisi yanlış (TEKNOLOJİ) geliyorsa: Settings → LDAP → **Kullanıcı
incele** → kendi kullanıcı adın → tablodaki **Ekip adayları** sütununda
doğru değere tıkla (ör. `SY-Dijital Bankacılık`) → Team attribute/regex
otomatik dolar → **Save** → çıkış/giriş. Sonra katalog UG/SY takım
etiketlerini kullanıcıların gerçek team değerleriyle hizala.
