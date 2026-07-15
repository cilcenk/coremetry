# Audit — Settings `Row` taşması (LDAP Team attribute / Team regex)

2026-07-15 · HEAD `d180966d` (v0.8.537) · Salt-okunur inceleme, hiçbir dosya
değiştirilmedi. Kod: `frontend/src/pages/settings/shared.tsx`,
`LdapTab.tsx` + `Row`'un diğer kullanıcıları.

## Kısa hüküm

1. **Dört iddianın dördü de birebir doğru, drift yok.** `Row`'da `flexWrap`
   yok (`shared.tsx:23`); `Field2` `small` iken bile `minWidth: 200`
   dayatıyor (`:66`); LdapTab'da 5-field'lı satır var (`:173-195`);
   kapsayıcı `maxWidth: 800` (`:79`). Matematik: 5×200 + 4×12 =
   **1048px > 800** → **her ekran boyutunda** taşıyor.
2. **`Field2`'nin `minWidth: 200`'ü kendi içinde tutarsız:** `small`'ın
   flex-basis'i **180px**, yani minimum genişlik kendi tabanından
   **büyük**. `small` bayrağı işlevsiz — bu bir bug, tercih değil.
3. **Brief'in dosya listesi 8 değil 7.** `RetentionTab` listeye yanlış
   girmiş: oradaki `Row` bir JSX bileşeni değil, `type Row = { value,
   unit }` (`RetentionTab.tsx:13`) — `useState<Row>` generic parametresi.
   Değişiklikten **hiç etkilenmiyor**.
4. **`shared.tsx`'te `LDAPRow` ölü kod ve zaten `flexWrap: 'wrap'`
   taşıyor** (`:58-60`). Yani bu duvara daha önce toslanmış, sarmalayan
   bir varyant yazılmış, hiç kullanılmamış. Önerilen düzeltme onu
   gereksiz kılıyor → **sil**.
5. Blast radius asimetrik: `flexWrap` **7 dosyayı**, `minWidth` yalnız
   **3 dosyayı** (Field2 kullananlar) etkiliyor.

---

## 1. Kanıtlar

### 1.1 `Row` — `flexWrap` yok ✅

`shared.tsx:21-27`:
```tsx
export function Row({ children }: { children: ReactNode }) {
  return (
    <div style={{ display: 'flex', gap: 12, alignItems: 'flex-start' }}>
      {children}
    </div>
  );
}
```
Flex container'ın `flex-wrap` varsayılanı `nowrap` → sığmayan satır alt
satıra kaymaz, sıkışır ve taşar.

### 1.2 `Field2` — `small` işlevsiz ✅ (asıl bug)

`shared.tsx:62-72`:
```tsx
export function Field2({ label, hint, small, children }: {
  label: string; hint?: string; small?: boolean; children: ReactNode;
}) {
  return (
    <div style={{ flex: small ? '0 1 180px' : 1, minWidth: 200 }}>
```
`small` → `flex: 0 1 180px` (grow yok, shrink var, taban 180px). Ama
`minWidth: 200` **koşulsuz**. Flex algoritması `min-width`'i shrink'in
tabanı olarak kullanır → item asla 200px'in altına inemez. `small`'ın
180px'lik tabanı **hiçbir zaman ulaşılamıyor**. Bayrak ölü.

### 1.3 LdapTab — 5 field'lı satır ✅

`LdapTab.tsx:173-195`, `<Row>` içinde sırayla: **User attribute**,
**Email attribute**, **Display attribute**, **Team attribute**,
**Team regex** — hepsi `small`.

`LdapTab.tsx:79`: `<div style={{ maxWidth: 800 }}>`

Gerekli genişlik = 5 × `minWidth` 200 + 4 × `gap` 12 = **1048px**.
Kapsayıcı 800px. **Taşma ~248px, ekran boyutundan bağımsız.**

### 1.4 En kalabalık satır gerçekten burası ✅

`<Field2` sayısı, `<Row>` bloğu başına maksimum:

| Dosya | Max `Field2`/Row | Durum |
|---|---|---|
| **LdapTab.tsx** | **5** | **taşıyor** |
| KnowledgeTab.tsx | 3 | 3×200+24 = 624 ≤ 800 ✔ |
| ApiTokensTab.tsx | 2 | 2×200+12 = 412 ✔ |
| BrandingTab / ChannelModal / MaintenanceTab / SmtpTab | 0 | `Field2` değil, `components/ui/Field` kullanıyor |

### 1.5 `Row`'un gerçek kullanıcıları — 7 dosya

JSX olarak `<Row>` render edenler: `ApiTokensTab` (1), `BrandingTab` (4),
`ChannelModal` (4), `KnowledgeTab` (2), **`LdapTab` (16)**,
`MaintenanceTab` (1), `SmtpTab` (4).

`RetentionTab` **değil** (§Kısa hüküm 3).

### 1.6 `LDAPRow` — ölü kod, ve düzeltmenin emsali

`shared.tsx:58-60`:
```tsx
export function LDAPRow({ children }: { children: ReactNode }) {
  return <div style={{ display: 'flex', gap: 12, marginBottom: 10, flexWrap: 'wrap' }}>{children}</div>;
}
```
`grep -rn "LDAPRow" frontend/src` → **yalnız tanım satırı**. Hiçbir
tüketici yok. İçeriği tam olarak önerilen `Row` düzeltmesi
(+`marginBottom`). Bu, sorunun daha önce fark edildiğinin ve çözümün
yazılıp bağlanmadığının kanıtı.

## 2. Tasarım

### 2.1 `Row` — `flexWrap: 'wrap'`

`shared.tsx:23`:
```tsx
<div style={{ display: 'flex', gap: 12, alignItems: 'flex-start', flexWrap: 'wrap' }}>
```
**Neden regresyon değil:** `flex-wrap` yalnız satır **sığmadığında**
devreye girer. Bugün sığan hiçbir satırın layout'u değişmez — 7
dosyadaki her satır bugünkü hâliyle sığıyor (§1.4), LdapTab'ın 5'lisi
hariç ki o zaten taşıyor. Yani bu değişiklik **yalnız bozuk olanı**
etkiliyor.

### 2.2 `Field2` — koşullu `minWidth`

`shared.tsx:66`:
```tsx
<div style={{ flex: small ? '0 1 180px' : 1, minWidth: small ? 140 : 200 }}>
```
**140 gerekçesi:** `small` field'ların en dar içeriği **Team regex**
(`fontFamily: 'ui-monospace, monospace'`, placeholder `-([^-]+)$` =
9 karakter). Monospace 13px'te ~7.8px/karakter → ~70px metin + input
padding/border (~16px) ≈ 86px. 140px, placeholder'ı **kesmeden** ve
sarma kararına alan bırakarak taşıyor. 180'in (flex-basis) altında
kalması şart, yoksa `small` yine ölü kalır.

**Etki:** `Field2` yalnız 3 dosyada — LdapTab (29 kullanım),
KnowledgeTab (4), ApiTokensTab (2). `small` olmayan field'lar
`minWidth: 200`'de kalıyor → o satırlar birebir aynı.

**Sonuç (LdapTab 5'lisi):** 5 × 140 + 48 = **748 ≤ 800** → satır artık
sarmadan da sığar; flex algoritması item'ları ~150px'e oturtur.

### 2.3 LdapTab — satırı 3 + 2'ye böl (önerilen)

`flexWrap` tek başına 4+1 sarar (taban 180px: `4×180+36 = 756 ≤ 800`),
ki bu **Team attribute'u Team regex'ten ayırır** — oysa ikisi bir çift
ve hint'leri birbirine atıf yapıyor. Elle gruplama daha iyi:

```
<Row> User attribute · Email attribute · Display attribute </Row>   → 3×180+24 = 564 ✔
<Row> Team attribute · Team regex </Row>                            → 2×180+12 = 372 ✔
```
İkisi de sarmadan sığıyor, çift bir arada kalıyor, ve field'lar
`small`'ın 180px tabanında rahat nefes alıyor (§2.2'nin sıkıştırdığı
~150px yerine).

### 2.4 `LDAPRow`'u sil

Ölü kod (§1.6) ve `Row` düzeltildikten sonra tamamen gereksiz. CLAUDE.md:
*"Don't add backwards-compat shims when removing a feature."*

## 3. Değişecek dosyalar

| Dosya | Değişiklik |
|---|---|
| `frontend/src/pages/settings/shared.tsx` | `Row`'a `flexWrap: 'wrap'`; `Field2` `minWidth: small ? 140 : 200`; `LDAPRow` **silinir** |
| `frontend/src/pages/settings/LdapTab.tsx` | 5-field'lı `<Row>` → 3 + 2 iki `<Row>` |

**Backend yok. Tip/API değişikliği yok. Yeni bağımlılık yok.**

## 4. Doğrulama

### Otomatik
```
cd frontend && npx tsc --noEmit
cd frontend && npx eslint src     # zemin: 138 uyarı / 0 hata — ARTMAMALI
cd frontend && npm test           # zemin: 651 test
```
`LDAPRow` silinince tsc, kalan bir tüketici varsa **derleme hatası**
verir — silmenin güvenliği böyle kanıtlanır (grep + tsc, iki bağımsız
kanıt).

### Manuel — asıl doğrulama görsel

**Settings → LDAP** (asıl vaka):
1. Team attribute + Team regex satırı **taşmıyor**; ikisi de tam görünür.
2. Team regex placeholder'ı (`-([^-]+)$`) **kesilmeden** okunuyor.
3. Pencereyi daralt → field'lar alt satıra **sarıyor**, sıkışıp taşmıyor.

**`Row`'un diğer 6 kullanıcısı — regresyon kontrolü.** Her birinde
bakılacak tek şey: *satır bugünkü gibi tek satırda mı duruyor* (sığan
satırlarda `flexWrap` hiçbir şey değiştirmemeli):

| Sekme | Kontrol |
|---|---|
| Settings → API Tokens | 2-field'lı satır tek satırda |
| Settings → Branding | 4 `Row`, `Field`+`input` — layout aynı |
| Settings → Knowledge | 3-field'lı satır tek satırda (624 ≤ 800) |
| Settings → Maintenance | 1 `Row` — aynı |
| Settings → SMTP | 4 `Row` — aynı |
| Alerts → kanal modalı (`ChannelModal`) | 4 `Row`, select+input — aynı; **modal dar olduğu için sarma görülebilir, bu İYİLEŞME** |

`ChannelModal` tek dikkat noktası: modal genişliği sayfa gövdesinden dar,
yani bugün sıkışan bir satır varsa `flexWrap` onu sarar — davranış
değişir ama **iyileşme yönünde**. Yine de gözle bak.

**Tema:** değişiklik token'a dokunmuyor; dark/light/redhat'te fark
beklenmiyor.

### Kapı
`npx tsc --noEmit` → `npx eslint src` → `npm test` → `go build ./...` →
`go test ./...` → `make audit` → commit → tag → push → deploy.

## 5. Açık sorular — operatör

1. **`minWidth: small ? 140 : 200`** — 140 §2.2'de gerekçelendirildi
   (monospace regex placeholder'ı sığdırır). Onaylıyor musun?
2. **LdapTab'ı 3+2 bölelim mi (§2.3)?** `flexWrap` tek başına 4+1 sarar
   ve Team çiftini ayırır. Öneri: elle böl.
3. **`LDAPRow` silinsin mi?** Ölü kod; tek tüketicisi yok.

## Öneri

§2.1 + §2.2 + §2.3 + §2.4 tek release. İki dosya, backend'e dokunmuyor,
sığan satırlarda davranış birebir aynı. Asıl risk yok; `flexWrap` yalnız
bugün zaten bozuk olanı etkiliyor. Sistemik seviyede (`shared.tsx`)
yapıldığı için LDAP dışındaki 6 sekme de gelecekteki taşmalara karşı
korunuyor — brief'in istediği tam da bu.

**Onay bekliyor — implementasyona geçilmedi.**
