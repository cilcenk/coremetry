# Audit — Share butonunun rengi (Grafana gibi belirgin olsun)

2026-07-15 · HEAD `64dc520502a766f46c20c624513dd460ed405934` · Her iddia
dosya:satır ile kod okunarak doğrulandı. Kontrast oranları hex'lerden
WCAG 2.x bağıl parlaklık (relative luminance) formülüyle elle hesaplandı;
alpha'lı değerler belirtilen parent arka planı üzerine kompozit edilerek
çözüldü. Kod değiştirilmedi.

## Kısa hüküm

- **Operatörün CSS iddiası DOĞRU, satır numarası YANLIŞ:** `.share-btn`
  ~1146-1153'te değil, **globals.css:1167-1174**'te. İçerik birebir
  iddia edildiği gibi nötr.
- **Yorum gerçekten yalnız DAVRANIŞI tarif ediyor** — "Grafana-style"
  ifadesi URL kopyalama davranışına ait, renge dair hiçbir şey demiyor
  (`ShareButton.tsx:4-7`). Operatörün okuması doğru.
- **KANIT:** `.share-btn`'in üç deklarasyonu `button.sec`
  (globals.css:302) ile **byte-byte aynı**. "Diğer ikincil butonlarla
  birebir aynı" iddiası mecazi değil, literal.
- **⚠️ EN ÖNEMLİ BULGU — kullanım yeri iddiası YANLIŞ:** Paylaşılan
  `ShareButton` **üç yerde değil, TEK yerde** (`Explore.tsx:413`).
  `ProblemDetail.tsx:45` **kendi yerel** `ShareButton`'ını tanımlıyor
  (import etmiyor), `Logs.tsx:46` üçüncü bir `LogShareButton` tanımlıyor
  — **ikisi de `.share-btn` kullanmıyor**, `<Button variant="secondary">`
  kullanıyor. `.share-btn` CSS'ini değiştirmek **3 butonun 1'ini** boyar.
- **Emsal zaten var, üstelik Seçenek A'nın tam kendisi:** çıplak
  `button` (globals.css:263-268) = `background: var(--accent); color:
  #fff` ve `Button.tsx:30` `primary: ''` → yani **A = `<Button
  variant="primary">`, sıfır yeni CSS**. `.share-btn` hand-rolled ve
  CLAUDE.md "asla hand-rolled buton stili" kuralını ihlal ediyor.
- **Kontrast operatörün beklediğinin TERSİ çıktı:** A için `#fff` LIGHT
  temada sorunsuz (5.19:1 ✓), **DARK temada kalıyor (3.34:1 ✗ AA)**.
  Endişe edilen tema yanlış temaydı.
- **A'nın gizli maliyeti:** `:hover` ve `.copied` state'lerinin İKİSİ de
  solid zeminle kırılıyor (hover butonu griye çeviriyor, `.copied`
  butonu şişkin-soliddan şeffafa "söndürüyor"). A, CSS tweak'i olarak
  yapılırsa üç state birden yeniden yazılmalı.
- **Öneri: B (tinted chip)** — §6. Belirleyici sebep kontrast değil,
  `ProblemDetail.tsx:203`'te Share'in **gerçek primary aksiyonun
  (`Resolve`) tam yanında** durması: A orada iki solid mavi buton yan
  yana koyar ve Grafana#84110'un eleştirdiği hatanın birebir kendisini
  üretir.

## 1. Drift kontrolü — iddia vs HEAD

| Operatör iddiası | HEAD'deki gerçek | Hüküm |
|---|---|---|
| `.share-btn` CSS ~1146-1153 | **globals.css:1167-1174** | ✗ **~21 satır drift — düzeltildi** |
| CSS nötr: `--bg3`/`--text`/`--border` | birebir öyle (satır 1169) | ✓ DOĞRU |
| Üstteki yorum davranışı tarif ediyor | birebir öyle (satır 4-7) | ✓ DOĞRU |
| `ShareButton` 3 yerde: ProblemDetail ×2 + Explore | **1 yerde** (Explore); diğer ikisi ayrı yerel bileşen | ✗ **YANLIŞ — §3** |
| `.copied` yeşil `--ok` | kısmen: `color`/`border-color` `--ok`, **`background` hardcoded** `rgba(63,185,80,.12)` | ~ KISMEN |
| eslint zemini 138 uyarı / 0 hata | `npx eslint src --ext .ts,.tsx` → `138 problems (0 errors, 138 warnings)` | ✓ DOĞRU |

**Yorum gerçekten davranış mı tarif ediyor?** Evet. `ShareButton.tsx:4-7`:

```
/**
 * Grafana-style share button — copies the current URL (with all encoded
 * page state) to the clipboard and flashes a confirmation.
 */
```

"Grafana-style" niteliği "copies the current URL … flashes a
confirmation" cümlesinin öznesi. Renk/görsel iddiası yok. Operatörün
"bu sadece DAVRANIŞI tarif ediyor" okuması **doğru**.

## 2. `.share-btn`'in TAM mevcut CSS'i + state'ler

`frontend/src/styles/globals.css:1167-1174` (birebir):

```css
.share-btn {
  display: inline-flex; align-items: center;
  background: var(--bg3); color: var(--text); border: 1px solid var(--border);
  padding: 5px 12px; font-size: 12px; cursor: pointer;
  border-radius: 6px; transition: background .12s, border-color .12s, color .12s;
}
.share-btn:hover { background: var(--bg2); border-color: var(--accent); color: var(--accent2); }
.share-btn.copied { background: rgba(63,185,80,.12); border-color: var(--ok); color: var(--ok); }
```

**Kanıt — `.share-btn` = `button.sec` klonu.** globals.css:302:

```css
button.sec { background: var(--bg3); color: var(--text); border: 1px solid var(--border); }
```

Üç deklarasyon da birebir aynı. `.share-btn` (0-1-0 özgüllük) çıplak
`button` kuralını (0-0-1) ezdiği için buton `--accent` yerine `--bg3`
alıyor. Yani operatörün "ayrışmıyor" şikâyeti **CSS düzeyinde literal
olarak doğru**: Share butonu ile herhangi bir `.sec` butonu aynı
piksellerdir.

### 2a. Yeni baz renkle state'ler hâlâ ayırt edilebilir mi?

**Seçenek A (solid accent) ile → İKİSİ DE KIRILIR:**

- `:hover` → `background: var(--bg2)`. Baz solid maviyken hover butonu
  **griye çevirir** — ters/kırık. (Karşılaştır: çıplak `button:hover`
  doğru yapıyor: `background: var(--accent2)`, globals.css:268.)
- `.copied` → `background: rgba(63,185,80,.12)`. Baz solid maviyken
  `.copied` arka planı **%12 alfa yeşile** düşürür; parent zemin
  (Explore'da `--bg2`) sızdığı için buton solid dolgudan neredeyse
  şeffafa **"söner"**. Görsel olarak buton kaybolmuş gibi görünür —
  1 saniyelik onay flash'ı için ciddi regresyon.

→ A'yı `.share-btn` CSS'ini düzenleyerek yapmak **üç state'in de**
yeniden yazılmasını gerektirir (`.copied` solid `--ok` + `#fff` metin
olmalı). A'yı `<Button variant="primary">` ile yapmak (§5) hover'ı
bedavaya doğru getirir, ama `.copied` yine elle çözülmeli.

**Seçenek B (tinted chip) ile → İKİSİ DE ÇALIŞIR:**

- `:hover` → tint'i koyulaştırmak yeterli (ör. %10 → %18 mix). Mevcut
  `border-color: var(--accent)` + `color: var(--accent2)` zaten uyumlu.
- `.copied` → mavi tint'ten yeşil tint'e **hue kayması**; iki taraf da
  tint olduğu için geçiş sakin, "sönme" yok. Ayırt edilebilirlik
  korunur. **B mevcut `.copied` deseniyle doğal uyumlu.**

### 2b. `.copied`'ın hardcoded yeşili — mevcut, ev-çapında, blokör DEĞİL

`rgba(63,185,80,.12)` = dark temanın `--ok` (#3fb950) değerinin %12'si;
light (`#1a7f37`) ve redhat (`#3e8635`) temalarında **yanlış yeşil**
gösterir — CLAUDE.md'nin "token-level tema" kuralına aykırı. Ancak bu
`.share-btn`'e özgü bir kusur değil: aynı literal **13 ayrı yerde**
kullanılıyor (`Login.tsx:159`, `settings/AiTab.tsx:266`,
`settings/LdapTab.tsx:319`, `settings/BrandingTab.tsx:185`,
`settings/MaintenanceTab.tsx:64`, `dashboard/PanelRenderer.tsx:636`,
`dependencies/DetailDrawer.tsx:506`, `dependencies/panels/shared.tsx:417`,
`dependencies/panels/OraclePanel.tsx:54`, `PublicStatus.tsx:208` …).
**Bu audit'in kapsamı dışında bir ev-ödevi** — bu değişikliğe
bağlanmamalı, ayrı bir temizlik kalemi olarak kuyruğa girmeli.

## 3. Kullanım yerleri — operatörün iddiası YANLIŞ

`grep -rn "ShareButton" frontend/src/` çıktısının okuması:

| Yer | Bileşen | `.share-btn` mı? | Oturduğu zemin |
|---|---|---|---|
| `pages/Explore.tsx:11` (import), **`:413`** (render) | **paylaşılan** `@/components/ShareButton` | ✅ **EVET** | `--bg2` panel (Explore.tsx:395) içindeki `ZONE_FIRST` toolbar (`:366`) |
| `features/anomalies/ProblemDetail.tsx:203` | **yerel** `ShareButton` (`:45`) | ❌ HAYIR | `.rb-bar` (globals.css:1665, kendi background'ı yok) → `#content` (`:252`, background yok) → `body` = **`--bg0`** (`:183`) |
| `features/anomalies/ProblemDetail.tsx:361` | aynı yerel bileşen | ❌ HAYIR | aynı — `--bg0` |
| `pages/Logs.tsx:663` | **yerel** `LogShareButton` (`:46`) | ❌ HAYIR | (bu audit'te izlenmedi — **DOĞRULANAMADI**) |

**Yani ortada üç ayrı Share butonu var, ortak bileşen yalnız birinde
kullanılıyor.** `ProblemDetail.tsx:45-57` şunu yapıyor:

```tsx
function ShareButton() {
  const [copied, setCopied] = useState(false);
  ...
  return (
    <Button variant="secondary" size="sm" onClick={share}
      leftIcon={<Link2 size={13} strokeWidth={1.75} />}>
      {copied ? 'Copied' : 'Share'}
    </Button>
  );
}
```

`ProblemDetail.tsx:17` `Button`'ı import ediyor ama
`@/components/ShareButton`'ı **etmiyor** — isim çakışması yok, çünkü
yerel tanım gölgeliyor. `Logs.tsx:46` de üçüncü bir varyant
(`⧉ Copy link` / `✓ Copied`, `variant="secondary"`).

**Operatörel sonuç:** `.share-btn` rengini değiştirmek **yalnız
Explore'u** etkiler. Operatör Problems ve Logs sayfalarında hâlâ nötr
gri bir Share görecek ve "yapmadın" diyecek. İstek "Share butonu
belirgin olsun" ise **kapsam üç bileşenin tekleştirilmesidir**, tek
satır CSS değil. Bu, onay gerektiren bir kapsam kararı (§8/S1).

### 3a. Solid accent bu zeminlerde nasıl durur?

- **Explore.tsx:413** — `ZONE_FIRST` bir **filtre toolbar'ı**: `Source`
  segmented kontrolü + `SavedViewsBar` + Share. `--bg2` (sakin) zemin.
  Solid accent burada sayfanın **en yüksek sesli** öğesi olur; oysa
  toolbar'ın işi filtrelemek, paylaşmak değil. Görsel hiyerarşiyi
  tersine çevirir.
- **ProblemDetail.tsx:203 — BELİRLEYİCİ DURUM.** Share, gerçek primary
  aksiyonun *tam yanında* duruyor:

```tsx
<span className="spacer" />
<ShareButton />
{isAdmin && (state === 'new' || state === 'regressed' || state === 'acknowledged') && (
  <>
    {state !== 'acknowledged' && <button className="sec" onClick={() => act('acknowledged')}>Acknowledge</button>}
    <button className="sec" onClick={() => act('ignored')}>Ignore</button>
    <button onClick={() => act('resolved')}>Resolve</button>
```

`<button onClick={() => act('resolved')}>` — **class'sız**, yani çıplak
`button` kuralı (globals.css:263) → **zaten solid `--accent` + `#fff`**.
Share solid accent yapılırsa admin bir operatör `.rb-bar`'da **iki solid
mavi buton yan yana** görür ve Share, `Resolve` ile eşit görsel ağırlıkta
olur. Bu, Grafana#84110'daki "çok prominent" eleştirisinin **repo
içindeki somut, birebir örneği**: yardımcı bir affordance, yıkıcı primary
aksiyonla aynı sesle bağırıyor. `ProblemDetail.tsx:361`'de aynı desen
(`Acknowledge`, `variant="secondary"`).

## 4. Üç temada token değerleri + kontrast

### 4a. Token matrisi (birebir)

| Token | dark `:root` (`:1`) | light `[data-theme="light"]` (`:65`) | redhat `[data-theme="redhat"]` (`:115`) |
|---|---|---|---|
| `--accent` | `#388bfd` (`:22`) | `#0969da` (`:79`) | `#0066cc` (`:129`) |
| `--accent2` | `#58a6ff` (`:23`) | `#0550ae` (`:80`) | `#0066cc` (`:130`) |
| `--accent-bg` | `#15243b` (`:50`) | `#ddf4ff` (`:98`) | `#e7f1fa` (`:146`) |
| `--accent-border` | `#1f3a5e` (`:51`) | `#b6e3ff` (`:99`) | `#bee1f4` (`:147`) |
| `--accent-soft` | `rgba(56,139,253,.16)` (`:37`) | `rgba(9,105,218,.10)` (`:92`) | `rgba(0,102,204,.08)` (`:141`) |
| `--bg0` / `--bg2` / `--bg3` | `#0d1117` / `#21262d` / `#2d333b` | `#ffffff` / `#eaeef2` / `#d0d7de` | `#f0f0f0` / `#f5f5f5` / `#ebebeb` |
| `--ok` | `#3fb950` (`:25`) | `#1a7f37` (`:82`) | `#3e8635` (`:132`) |

**Not:** redhat'te `--accent2 === --accent` (`#0066cc`) — B seçeneğinde
metin ile kenarlık aynı hue'da, ayırt edici gücü diğer iki temadan az.

### 4b. Kontrast — Seçenek A: `#fff` on `var(--accent)`

`.share-btn` `font-size: 12px` normal ağırlık → WCAG **AA normal metin
eşiği 4.5:1** geçerli (AA-large 3.0:1 değil; large = 18pt/24px veya
14pt/18.66px bold).

| Tema | `--accent` | Bağıl parlaklık L | `#fff` kontrastı | AA (4.5:1) |
|---|---|---|---|---|
| **dark** | `#388bfd` | 0.2639 | **3.34:1** | ❌ **KALIYOR** |
| light | `#0969da` | 0.1522 | **5.19:1** | ✅ geçiyor |
| redhat | `#0066cc` | 0.1386 | **5.57:1** | ✅ geçiyor |

**Operatörün hipotezi tersine döndü.** Endişe "özellikle LIGHT temada
`--accent` yeterince koyu mu?" idi. Light **en rahat geçen** temalardan
(5.19). Kalan **dark** — çünkü `#388bfd` parlak/doygun bir mavi ve
beyaz metni taşıyamıyor (3.34, eşiğin %26 altında).

**Kritik bağlam — bu YENİ bir kusur değil.** globals.css:263-264 zaten
`background: var(--accent); color: #fff` ile **uygulamadaki HER primary
butonu** aynı 3.34:1 ile dark temada boyuyor (`Resolve` dahil,
ProblemDetail.tsx:208). Yani A'yı seçmek yeni bir a11y borcu yaratmaz;
**mevcut, uygulama-çapında bir borcu bir buton daha yayar**. Aynı
şekilde "hardcoded `#fff` tema-agnostik değildir" itirazı doğru ama A'ya
özgü değil — status quo bu. Bu borcu kapatmak (ör. dark `--accent`'i
koyultmak ya da primary metnini token'lamak) **ayrı, token düzeyinde,
uygulama-geneli bir karar**; bu audit'e bağlanmamalı.

### 4c. Kontrast — Seçenek B: `--accent2` on `color-mix(--accent 10%, transparent)`

Alfa şeffaf olduğu için efektif zemin parent'a bağlı. Explore toolbar'ı
= `--bg2` (Explore.tsx:395), ProblemDetail `.rb-bar` = `--bg0`.

| Tema | zemin | kompozit zemin | metin `--accent2` | kontrast | AA |
|---|---|---|---|---|---|
| **dark** | `--bg2` `#21262d` | `rgb(35,48,66)` | `#58a6ff` | **5.28:1** | ✅ |
| **dark** | `--bg0` `#0d1117` | `rgb(17,29,46)` | `#58a6ff` | **6.71:1** | ✅ |
| **light** | `--bg2` `#eaeef2` | `rgb(212,225,240)` | `#0550ae` | **5.72:1** | ✅ |
| **redhat** | `--bg2` `#f5f5f5` | `rgb(221,231,241)` | `#0066cc` | **4.45:1** | ⚠️ **sınırda** |

**redhat 4.45:1**, AA eşiğinin **0.05 altında** — pratik olarak sınırda,
teknik olarak kalıyor. Sebebi: redhat'te `--accent2 === --accent` ve PF
mavisi (`#0066cc`) çok açık bir zemin (`#f5f5f5` %90) üzerinde. Üç
token-uyumlu çare (yeni renk icat etmeden):
1. tint'i %10 → %14 çıkar (zemin koyulaşır, kontrast artar),
2. redhat'te metni `--accent` yerine mevcut daha koyu bir token'a bağla,
3. `font-weight: 600` ekle (algısal ağırlık; WCAG eşiğini değiştirmez).

Operatör onayı gerekiyor (§8/S3).

### 4d. Karşılaştırmalı hüküm

| | dark | light | redhat | en kötü |
|---|---|---|---|---|
| **bugünkü** (`--text` on `--bg3`) | ~11:1 | ~13:1 | ~15:1 | çok yüksek (ama ayrışmıyor) |
| **A** (`#fff` on `--accent`) | **3.34 ❌** | 5.19 ✅ | 5.57 ✅ | **3.34** |
| **B** (tint) | 5.28 ✅ | 5.72 ✅ | **4.45 ⚠️** | **4.45** |

İkisi de bugünkünün altında (kaçınılmaz — "ayrışmak" kontrast
harcamaktır). Ama **A'nın en kötüsü eşiğin %26 altında; B'nin en kötüsü
%1 altında.** Kontrast ekseninde B net üstün.

## 5. Emsal — atom zaten var, A zaten uygulanmış durumda

- **`components/ui/Button.tsx:16`** → `type Variant = 'primary' |
  'secondary' | 'danger' | 'ghost'`. **Evet, `variant` var.**
- **`Button.tsx:29-34`** → `primary: ''` (class yok!), `secondary:
  'sec'`, `danger: 'danger'`, `ghost: 'ghost'`.
- Yani `primary` = **class'sız `<button>`** = globals.css:263-268:

```css
button {
  background: var(--accent); color: #fff; border: none;
  border-radius: 6px; padding: 5px 14px; font-size: 13px; cursor: pointer;
  transition: background .15s; font-family: inherit;
}
button:hover:not(:disabled) { background: var(--accent2); }
```

**Bu, Seçenek A'nın harfi harfine kendisi** — `background: var(--accent)`
+ `color: #fff` + doğru hover. `Button.tsx:3-9` yorumu tasarımı
açıklıyor: atom, `globals.css` kurallarının tipli kabuğu.

**Share butonu neden atomu kullanmıyor? Hand-rolled olduğu için.**
`ShareButton.tsx:32-35`:

```tsx
<button className={'share-btn' + (copied ? ' copied' : '')}
  onClick={onClick}
  title="Copy a shareable link to this view"
  style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
```

Ham `<button>` + özel class + inline `style`. **CLAUDE.md ile net
çelişki:** "Buttons/fields — one design language: shared `<Button
variant size>` atom (`components/ui/Button.tsx`) … **Never hand-roll
`<button style={{…}}>`**". `.share-btn` hem `button.sec`'i kopyalıyor
(§2) hem inline style taşıyor — iki ihlal. İlginç şekilde **yerel
`ProblemDetail.ShareButton` (`:52`) atomu doğru kullanıyor**; ortak
bileşen kullanmıyor.

**Sonuç:** A'nın "doğru" uygulaması yeni CSS yazmak değil,
**`.share-btn`'i silip `<Button variant="primary" size="sm">`'e
geçmek**. Bu tek hamlede hand-rolled ihlali de kapanır. `.copied`
state'i yine ayrıca çözülmeli (§2a).

## 6. Öneri — **Seçenek B (tinted chip)**

**Gerekçe, ağırlık sırasıyla:**

1. **Görsel hiyerarşi (belirleyici, kontrastan önce gelir).**
   `ProblemDetail.tsx:203`'te Share, `Resolve` ile (`:208`, çıplak
   `button` = solid accent) aynı satırda. A → iki solid mavi yan yana;
   yardımcı affordance yıkıcı primary ile eşit ses. Bu tam olarak
   Grafana#84110'un eleştirdiği şey ve bizde **teorik değil, satırı
   belli**. B ayrışmayı sağlar ama primary'yi taçlandırmaz — Share
   "vurgulu ama primary değil" katmanına oturur, ki doğru semantiği bu.
   Explore'da da (`:413`) filtre toolbar'ının en sesli öğesi Share
   olmamalı.
2. **Kontrast (§4d).** B'nin en kötü senaryosu 4.45 (eşiğin %1 altı,
   token-uyumlu düzeltilebilir); A'nın en kötüsü 3.34 (eşiğin %26 altı,
   dark = varsayılan tema). A'nın kusuru **varsayılan temada** ortaya
   çıkıyor — en çok görülen tema.
3. **State uyumu (§2a).** B mevcut `:hover` ve `.copied` desenleriyle
   doğal uyumlu (tint→tint hue kayması). A her iki state'i de kırıyor;
   özellikle `.copied` solid→şeffaf "sönme" üretiyor.
4. **Emsal ailesi.** `.wf-group` (`:723-735`) dışında aynı color-mix
   tint deseni zaten repoda: `.facet.on` (`:506`, `--accent-bg` +
   `--accent-border` + `--accent2`), `.row-selected` (`:340-341`),
   `.b-mut` (`:566`). "Vurgulu ama primary değil" için **ev standardı
   B'dir**; A ise "primary" katmanının rezerve edilmiş dili.
5. **Yeni renk icat edilmiyor.** B, `--accent`/`--accent2`'yi
   `color-mix` ile kullanır; üç temada da token'dan türer — CLAUDE.md
   "token-level tema" kuralına tam uyum. A ise hardcoded `#fff`
   getirir (status quo olsa da, yeni kod için geriye adım).

**A'yı seçme senaryosu:** Operatör "Grafana'nın bugünkü hali" ısrarındaysa
A meşru — ama o zaman **doğru yolu §5'tir**: `.share-btn` silinir,
`<Button variant="primary" size="sm">` kullanılır (sıfır yeni CSS, hover
bedava, hand-rolled ihlali kapanır), `.copied` solid `--ok` + `#fff`
olarak yeniden yazılır, ve dark 3.34:1 **bilinçli kabul edilir** (mevcut
tüm primary butonlarla tutarlı borç).

**Önerilen kapsam (§3'ün zorunlu sonucu):** Değişiklik ne olursa olsun
**önce üç Share butonu tek bileşene indirilmeli**, yoksa operatör 3
sayfanın 1'inde sonuç görür. Tercih edilen sıra:
1. `ProblemDetail.tsx:45-57` ve `Logs.tsx:46-70` yerel tanımları silinir;
   ikisi de `@/components/ShareButton`'ı import eder (Logs'un metni
   `⧉ Copy link` ve zengin `title`'ı prop'a taşınır).
2. Ortak `ShareButton` B tint'ini alır — tercihen `.share-btn`'i
   koruyarak (tint `.sec`'in dili değil, ayrı bir katman; atoma
   `variant="accent"` eklemek daha temiz ama atom API'sini genişletir →
   §8/S2).
3. `.copied` mevcut haliyle kalır (B ile uyumlu); hardcoded yeşil ayrı
   kalem (§2b).

## 7. Doğrulama planı

**Kapı (zorunlu):**
- `cd frontend && npx tsc --noEmit` → 0 hata.
- `cd frontend && npx eslint src --ext .ts,.tsx` → **138 uyarı / 0 hata**
  zemininin üstüne çıkılmamalı (bugün doğrulandı). Yerel bileşenler
  silinirse `react-refresh/only-export-components` uyarı sayısı
  **düşebilir** — düşüş kabul, artış blokör.
- `go build ./...` → değişiklik yalnız frontend ise etkilenmez, yine de
  release kapısı gereği koşulur.
- `make audit` → 🔴 yok.

**Gözle kontrol — 3 kullanım yeri × 3 tema = 9 hücre:**

| Yer | Ne kontrol edilecek |
|---|---|
| `/explore` (`Explore.tsx:413`) | Share, `Source` segmented + `SavedViewsBar` yanında ayrışıyor mu; **toolbar'ın en sesli öğesi OLMAMALI**; `--bg2` zemininde tint okunuyor mu |
| `/problems` → problem detay (`ProblemDetail.tsx:361`) | `--bg0` zemininde tint; `Acknowledge` (`variant="secondary"`) ile karışmıyor mu |
| exception-group detay (`ProblemDetail.tsx:203`, **admin rolüyle**) | **EN KRİTİK HÜCRE** — Share ile `Resolve` (solid accent) yan yana; Share `Resolve`'u bastırmamalı, hiyerarşi: Resolve > Share > Acknowledge/Ignore |
| `/logs` (`Logs.tsx:663`) | tekleştirme sonrası metin/`title` korunmuş mu (viewer rolü dahil, v0.8.102 davranışı) |

**Her hücrede üç tema** (`data-theme` yok = dark / `light` / `redhat`) ve
**her butonda üç state**: idle → hover → `.copied` (tıkla, 1.5-2 sn
flash'ı izle). Özellikle:
- **dark + A** seçilirse: `#fff` on `#388bfd` gözle okunabilir mi (ölçüm
  3.34 diyor ki hayır);
- **redhat + B**: 4.45 sınırı gözle nasıl (`#f5f5f5` zemin, PF mavisi);
- **`.copied` geçişi**: buton "sönüyor" mu (A'nın regresyonu).

**Playwright sürülmeyecek** — hazır olunca URL + kontrol listesi
operatöre verilir.

## 8. Açık sorular

1. **S1 — Kapsam (blokör).** `.share-btn` yalnız Explore'u besliyor
   (§3). Üç Share butonunu tek bileşene indirmeyi onaylıyor musunuz,
   yoksa **sadece Explore'daki** mi boyansın? (Sadece Explore = üç
   sayfada tutarsız Share; tekleştirme = 3 dosya dokunuşu, `/spec`
   eşiğinde.)
2. **S2 — Uygulama şekli.** B tint'i (a) `.share-btn` class'ında mı
   yaşasın (mevcut yapı korunur, hand-rolled ihlali sürer), yoksa (b)
   `Button.tsx`'e yeni bir `variant="accent"` mi eklensin (atom API'si
   genişler ama CLAUDE.md "one design language" kuralı tam kapanır ve
   gelecekteki "vurgulu ama primary değil" butonlar için dil doğar)?
   (b)'yi tercih ederim.
3. **S3 — redhat 4.45:1.** Sınır (%1 altı) kabul mü, yoksa §4c'deki üç
   token-uyumlu çareden biri uygulansın mı (tint %14 / daha koyu metin
   token'ı / `font-weight: 600`)?
4. **S4 — A ısrarı hâlinde.** Grafana'nın solid hali şartsa: dark
   temadaki **3.34:1**'i (mevcut tüm primary butonlarla tutarlı borç)
   bilinçli kabul ediyor musunuz; ve `ProblemDetail:203`'te Share'in
   `Resolve` ile aynı görsel ağırlığa gelmesi kabul mü?
5. **S5 — hardcoded yeşil.** `rgba(63,185,80,.12)` 13 yerde (§2b);
   ayrı bir "token temizliği" kalemi olarak kuyruğa alınsın mı?

## Öneri (özet)

**B — tinted chip.** Belirleyici sebep kontrast değil hiyerarşi:
`ProblemDetail.tsx:203`'te Share zaten solid-accent `Resolve`'un yanında
duruyor; A oraya ikinci bir solid mavi koyup Grafana#84110'un eleştirisini
birebir üretir. Kontrast da B'yi destekliyor (en kötü 4.45 vs 3.34) ve B
mevcut `:hover`/`.copied` state'lerini kırmadan, yeni renk icat etmeden,
repodaki `.wf-group`/`.facet.on`/`.row-selected` tint ailesine oturuyor.
Ama **önce S1**: kapsam tekleştirilmezse değişiklik üç sayfanın birinde
görünür.

**Onay bekliyor — implementasyona geçilmedi.**
