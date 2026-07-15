# Audit — Paylaş/Kopyala affordance tutarlılığı

**Tarih:** 2026-07-15 · **HEAD:** `c056d004` (v0.8.542) · **Kapsam:** frontend, read-only inceleme
**Tetikleyen:** Operatör: *"problems sayfasındaki share butonu ile bir trace sayfasındaki farklı"* + *"bu genel coremetry bütünlüğünü bozuyor"*

---

## Kısa hüküm

- **Operatör haklı, ve v0.8.540'ın "üç kopya tekleşti" iddiası EKSİKTİ.** `Trace.tsx:373` `<SharePopover />` birleştirmeye dahil edilmedi — adı farklı, `.share-btn` kullanmıyor, grep'e takılmadı. Dördüncü kopya orada duruyordu.
- **Fark hem görsel HEM davranışsal — ve davranışsal olan daha derin.** ShareButton tek tıkta kopyalar; SharePopover popover açar + network çağırır (`api.listTraceShares`). Bunlar *aynı kontrol değil*.
- **v0.8.542 görsel farkın yarısını (size) zaten kapattı.** Kalan görsel yarık: `variant="secondary"` (gri) vs `variant="accent"` (tinted). Bu kalemi yeniden kuyruğa alma.
- **SharePopover kaldırılamaz** — gerçek ek işi var: süreli public token mint etme (`api.go:566-568`, `PublicTrace.tsx`, rol ayrımı). Görsel hizalama evet, birleştirme hayır.
- **Tutarsızlık ölçüsü: 4 farklı "link kopyala" deseni, 8 bağımsız clipboard implementasyonu, 3 kez kopyalanmış fallback, 2 farklı flash süresi, 2 ikon ailesi (icons.tsx + lucide) + emoji + unicode glif.**
- **Bu audit sırasında 3 gerçek bug bulundu** — ikisi paylaşımla ilgisiz ama aynı aileden: `Logs` severity URL'e yazılmıyor (yanlış link üretir), `ApiTokensTab` tek-gösterimlik token'da sıfır geri bildirim.
- **`CopyButton` ayrı kalmalı** — farklı iş: değer kopyalar, link değil. Sınır net çizilebilir.
- Bu bir tasarım-dili işi, `api.go` konusuz. Backend değişikliği yok.

---

## 1. Envanter — her paylaş/kopyala yüzeyi

### 1a. Link paylaşan yüzeyler (asıl konu)

| # | Yüzey | Dosya:satır | Görünüm | Ne kopyalıyor | Flash | İkon | Fallback |
|---|---|---|---|---|---|---|---|
| 1 | `ShareButton` → Explore | `Explore.tsx:413` | `<Button variant="accent" size="sm">` | `window.location.href` | 1500ms, `.copied` tint | `IconLink size={13}` SVG | ✅ textarea |
| 2 | `ShareButton` → ProblemDetail (exception) | `ProblemDetail.tsx:191` | aynı | `window.location.href` | 1500ms | `IconLink` | ✅ |
| 3 | `ShareButton` → ProblemDetail (problem) | `ProblemDetail.tsx:349` | aynı | `window.location.href` | 1500ms | `IconLink` | ✅ |
| 4 | `ShareButton` → Logs | `Logs.tsx:642` | aynı | `window.location.href` | 1500ms | `IconLink` | ✅ |
| 5 | **`SharePopover` → Trace** | `Trace.tsx:373` (tanım `:907`, tetikleyici `:1007`) | **`<Button variant="secondary">`** (gri) + inline `style={{display:'inline-flex',gap:6}}` | href + **public token URL** | **tetikleyicide flash YOK**; içeride **2000ms**, inline `color` | `<IconLink/>` (size prop yok → 14) | ✅ **kendi kopyası** (`:891`+`:1135`) |
| 6 | `MetricPanel` menü "Copy link" | `MetricPanel.tsx:182` → `:114-119` | `<PanelMenuItem>⧉ Copy link</PanelMenuItem>` | `origin + href` (Explore deep-link) | 1500ms, **ayrık span** (`:227`) | **`⧉` unicode** | ❌ **yok** |

### 1b. Değer kopyalayan yüzeyler (kapsam sınırı — link değil)

| Yüzey | Dosya:satır | Görünüm | Ne kopyalıyor | Flash | İkon | Fallback |
|---|---|---|---|---|---|---|
| `CopyButton` (9 çağrı) | `CopyButton.tsx:7-44` | `.copy-btn` class, ham `<button>` | `value` prop (trace ID, JSON, stack, attr…) | 1500ms | **`⧉`/`✓` unicode** | ✅ textarea |
| ProblemDetail "Copy" (stack) | `ProblemDetail.tsx:176-179`, `:250` | `<Button variant="secondary" size="sm">` | stacktrace | 1500ms, **`.copied` yok → tint yok** | yok | ❌ |
| Profiling `CodeBlock` | `Profiling.tsx:380-396` | **ham `<button className="sec" style={{…}}>`** | code snippet | 1500ms | `✓` unicode | ❌ |
| SsoTab preset | `SsoTab.tsx:138-153`, `:181` | `<Button variant="secondary" size="sm">` | YAML | 1500ms | `✓` unicode | ❌ (ama `.catch()` var) |
| ApiTokensTab | `ApiTokensTab.tsx:79-82` | `<Button variant="secondary" size="sm">Kopyala</Button>` | API token | **YOK** | yok | ❌ |

**Ölü kod:** `Logs.tsx:13` — `import { CopyButton }` ama dosyada tek kullanım yok. `tsconfig*.json`'da `noUnusedLocals` tanımlı değil → `tsc --noEmit` yakalamıyor.
**Grep gürültüsü:** `scratchpad/wt-413/frontend/src/pages/Trace.tsx` — bayat worktree kopyası, untracked, build dışı.

---

## 2. Fark analizi — görsel mi, davranışsal mı?

**İkisi birden. Kanıt:**

### Görsel (v0.8.542 sonrası kalan)

| | ShareButton (`/problems`, `/explore`, `/logs`) | SharePopover (`/trace`) |
|---|---|---|
| variant | `accent` → `globals.css:1187` `background: var(--accent-bg); color: var(--accent2); border: 1px solid color-mix(…)` = **tinted çip** | `secondary` → `globals.css:302` `background: var(--bg3); color: var(--text)` = **gri** |
| size | `sm` | v0.8.542'de düzeltildi ✅ |
| copied stili | `className={copied?'copied':undefined}` → `button.accent.copied` yeşil tint (`globals.css:1203`) | inline `style={{color: internalCopied ? 'var(--ok)' : undefined}}` (`Trace.tsx:1031`) — **tint yok, `.copied` class'ı hiç kullanılmıyor** |
| layout | atom + `leftIcon` prop | elle `<IconLink/>` + `<span>` + inline `gap:6` — atom'un `leftIcon`'u tam bunu yapıyor |

### Davranışsal (daha derin)

| | ShareButton | SharePopover |
|---|---|---|
| tek tık sonucu | **anında kopyalar** | **popover açar** (`Trace.tsx:1008`); kopyalamak için **ikinci tık** |
| yan etki | yok | açılışta **network**: `api.listTraceShares` (`:953-956`) |
| kapsam | internal URL | internal + **public token mint** (TTL 1h/24h/7d/30d `:1050`) + aktif share listesi + Revoke |
| rol | yok | `canShare = !!user`, `canRevoke` = admin/editor (`:915-916`) |
| başarısızlıkta | sessiz, flash yok | **koşulsuz "Copied"** — `copyToClipboard` hiç throw etmez (`:1142`) → yanlış pozitif |

**Verdikt:** Operatör *renk* farkını gördü, ama altında yatan gerçek şu — bunlar aynı kontrol değil. `/trace`'deki bir **paylaşım yöneticisi**, diğerleri **tek-tık link kopyalayıcı**. Sadece `variant="accent"` eklemek görsel yarığı kapatır, "tek tık = kopyalandı" beklentisini kapatmaz.

---

## 3. Tutarsızlığın ölçüsü — sayılarla

| Ölçüm | Sayı | Kanıt |
|---|---|---|
| "Link kopyala" affordance deseni | **4** | ShareButton (accent+SVG) · SharePopover (secondary+SVG) · MetricPanel (`⧉` menü metni) · CopyButton (`⧉` glif) |
| Bağımsız clipboard implementasyonu | **8** | ShareButton · SharePopover · CopyButton · MetricPanel · ProblemDetail · Profiling · SsoTab · ApiTokensTab |
| Non-secure fallback kopyası | **3** | `CopyButton.tsx:17-26` · `ShareButton.tsx:47-56` · `Trace.tsx:1135-1144` — ShareButton yorumu "mirrors CopyButton" diyerek itiraf ediyor |
| Fallback'i HİÇ olmayan yüzey | **5** | MetricPanel · ProblemDetail · Profiling · SsoTab · ApiTokensTab |
| Farklı flash süresi | **2** (+1 yokluk) | 1500ms ×6 · **2000ms ×3 (hepsi SharePopover)** · flash yok ×1 (ApiTokensTab) |
| Farklı copied label | **4** | `Link copied` (Explore, default `ShareButton.tsx:37`) · `Copied` (diğer 3) · `✓ copied` (Profiling, SsoTab) · `✓` (CopyButton) |
| İkon ailesi | **4** | `icons.tsx` SVG · `lucide-react` (14 dosya) · unicode glif (`⧉ ✓ ⤢ ✎ ⟨⟩ ▦ ◔`) · emoji (`🔔 🔒 ⬇`) |
| `IconCopy` | **YOK** | `icons.tsx` grep → 0 eşleşme; `IconLink:87`/`IconCheck:109` var |
| icons.tsx ölü export | **8/18** | IconTrash, IconMail, IconList, IconDatabase, IconDashboard, IconClose, IconCircle, IconAlert — lucide aynı kavramı sağlıyor |
| Çift-implementasyon ikon kavramı | **10** | Check↔IconCheck · Bell↔IconBell · X↔IconClose · … Lucide kendi içinde de tutarsız (`TriangleAlert` vs `AlertTriangle`) |
| **Hand-rolled `<button … style={{`** | **21** / 16 dosya | Aşağıda |
| Ham `<button className="sec">` | **24** | atom variant'ını elle kopyalıyor |
| `<Button>` atom benimseme | **~%60** | 311 atom / 210 ham `<button>` |
| "Vurgulu ama primary değil" yolu | **8** | `variant="accent"` (tek tüketici ShareButton) · `.facet.on` · `.wf-group` · `.row-selected` · `--accent-soft` (23) · `color-mix` **12 farklı yüzde** · **hardcoded `rgba(56,139,253,…)` ×26** · `.sec`+inline bg |

**21 hand-rolled ihlal (CLAUDE.md "asla hand-rolled buton stili"):**
`service/ServiceLatencyHeatmap.tsx:82` · `settings/ZoomChannelPicker.tsx:114` · `dependencies/panels/OraclePanel.tsx:207` · `dependencies/panels/shared.tsx:68,143,564` · `Profiling.tsx:390` · `LogFieldsPanel.tsx:144` · `AdminAudit.tsx:286` · `explore/QueryRow.tsx:163` · `DensityToggle.tsx:65` · `dashboard/PanelEditor.tsx:156` · `BubbleUpPanel.tsx:153` · `MetricPanel.tsx:211,308` · `Sidebar.tsx:301,511,555` · `traces/shared.tsx:96` · `ui/Drawer.tsx:41` · `CorrelationContextDrawer.tsx:109`

Hepsi eşit değil: `AdminAudit.tsx:286` (`all:'unset'`) ve `Drawer.tsx:41` atom'un ifade edemediği şeyler. `Profiling.tsx:390` net drift — `className="sec"` + `.sec`'in zaten verdiği `--bg3`'ü tekrar yazıyor.

⚠️ **Grep tuzağı:** `<button style=` araması **0 döndürür ve yanıltıcıdır** — `style` hiçbir zaman ilk attribute değil. Doğrusu: `rg -U '<button\b[^>]*?style=\{\{'`.

---

## 4. Bu audit sırasında bulunan gerçek bug'lar

Tasarım dili kapsamı dışı ama aynı aileden — operatör isterse ayrı dilim olarak ship edilir.

### 4a. 🔴 Logs `severity` URL'e yazılmıyor → "Copy link" YANLIŞ link üretiyor

`severity` aktif filtre (sorguya giriyor `Logs.tsx:330,340`), chip'lerle değişiyor (`:760-765`) ama **`writeUrl` çağrısı yok** ve `writeUrl`'ün kendisi (`:263-276`) severity yazmıyor. URL→state import'u sabit sıfırlıyor (`:236` `severity: 0`), `urlSig`'e dahil değil (`:225-227`).

**Sonuç:** operatör ERROR chip'ine basıp Share derse, alıcı **All levels** görür. Sessiz yanlış-link. Yerel state sig-guard sayesinde bozulmuyor — kayıp yalnızca kopyalanan linkte.

**Diğer sayfalarda URL-state disiplini doğru** (doğrulandı): Trace `:115-123` (span+tab+range), Explore `:190-195` (tüm sorgu+range), ProblemDetail (`?problem=`/`?exc=`, `problemLink.ts:10,25`).

### 4b. 🔴 `ApiTokensTab.tsx:80` — tek-gösterimlik token, sıfır geri bildirim

```tsx
onClick={() => { void navigator.clipboard?.writeText(fresh); }}
```
Flash yok, `.catch()` yok, fallback yok. Üstündeki metin (`:73`): *"Token'ı ŞİMDİ kopyala — bir daha gösterilmeyecek"*. Non-secure context'te `navigator.clipboard` undefined → optional-chain sessizce no-op → operatör "kopyaladım" sanıp token'ı **kalıcı kaybeder**. Geri bildirimin en kritik olduğu yerde, geri bildirimi olmayan tek yüzey.

### 4c. 🔴 Non-secure context'te TypeError ile patlayan iki yüzey

- `ProblemDetail.tsx:178` — `navigator.clipboard?.writeText(stack).then(...)` → `undefined.then` → **TypeError**. `.catch` de yok → reject'te unhandled rejection.
- `Profiling.tsx:383` — aynı sınıf, `?.` bile yok → daha kötü.

**Not:** Coremetry düz HTTP ile servis edilen on-prem/air-gapped kurulumlarda çalışıyor → bu yollar teorik değil.

### 4d. 🟡 Fallback kalitesi bile tutarsız

`Trace.tsx:891-899` **en güçlüsü** — `writeText` *reject ederse de* fallback'e düşüyor. `ShareButton`/`CopyButton` yalnızca `writeText` **yoksa** düşüyor; reject → catch → sessiz ölüm. Yani kopyalanmış üç fallback birbirinin aynısı bile değil.

---

## 5. Çelişki hakemliği

Haritalama raporları iki noktada çeliştim, kod hakem:

1. **`.copy-btn` ↔ `button.accent` CSS çakışması var mı?** → **HAYIR.** Seçiciler ayrık: `.copy-btn`'i alan tek yer `CopyButton.tsx:39`, `accent`'i alan tek yer `Button.tsx:40`. İki class'ı birden taşıyan element yok. Tasarım dili olarak zıtlar (şeffaf hayalet glif vs dolgulu tint çip) ama bu bir kaskad çakışması değil, tutarlılık sorunu.
2. **Share butonu `size` sorunu açık mı?** → **HAYIR, v0.8.542 kapattı.** Bir rapor `md` diyor (HEAD `7e35f669`'da doğruydu), audit sırasında paralel oturum `c056d004`'ü indirdi. Yeniden kuyruğa alma.

⚠️ **Tag sırası yine tersine dönmüş:** `git merge-base --is-ancestor` teyitli → v0.8.542 commit'i v0.8.541'den ÖNCE, ikisi de `2026-07-15 22:59:01`. "Parallel-session tag collision" dersi tekrar etti — tag atmadan önce fetch+check.

**DOĞRULANAMADI:** tüm görsel iddialar (renk, tint, yükseklik) **koddan türetildi** — uygulama çalıştırılmadı, screenshot alınmadı.

---

## Öneri — tek tasarım dili

### Sınır çizgisi (net)

| Bileşen | Karar | Gerekçe |
|---|---|---|
| **`ShareButton`** | **Kanonik** — "bu görünümün linkini kopyala" | Zaten doğru şekle sahip: `variant`/`size`/`label`/`copiedLabel` |
| **`SharePopover`** | **KORUNUR, görsel olarak hizalanır** | Gerçek ek iş: süreli public token mint (`api.go:566-568`, `PublicTrace.tsx`, rol ayrımı). **Birleştirilemez.** İçindeki *internal-copy yarısı* ShareButton'a devredilir. |
| **`CopyButton`** | **AYRI KALIR** | Farklı iş: **değer** kopyalar (trace ID, JSON, stack), link değil. Inline glif affordance'ı doğru — 9 çağrı yeri satır içinde ID'nin yanında. Sadece `variant`/`size`/`label` props alarak elle-yazılan 5 yüzeyi yutar. |
| **`MetricPanel` "Copy link"** | **ShareButton ailesine katılır** | Link kopyalıyor, `⧉` glifiyle. Ama menü öğesi → görsel olarak menüde kalır, *kod yolu* paylaşılır. |

**Kısaca:** link → ShareButton · değer → CopyButton · süreli public link → SharePopover (görsel olarak ShareButton'a benzer, davranışı farklı kalır).

### Yeni token/renk İCAT EDİLMEZ

Hepsi mevcut: `variant="accent"` (`globals.css:1187`), `.copied` (`:1203`), `--accent-bg`/`--accent-border` (CSS-only disiplini korunur — 5 tüketici), `IconLink`/`IconCheck` (`icons.tsx:87/109`).
Tek istisna: `IconCopy` **yok**. Ya eklenir ya `IconLink` yeterli sayılır — **operatör kararı** (Açık Sorular §1).

### Backend

Yok. `api.go` büyümüyor.

---

## 6. Dilimleme — her dilim ayrı v0.8.X

Operatör hangi dilimi isterse o ship edilir. Sıra öneri, zorunluluk değil.

| # | Dilim | Dosya | Risk | Not |
|---|---|---|---|---|
| **S1** | **SharePopover tetikleyicisini hizala** — `variant="accent"`, `leftIcon={<IconLink/>}`, inline style sil | `Trace.tsx:1007-1012` | 🟢 düşük | **Operatörün bildirdiği bug.** 5 satır. Popover davranışı dokunulmaz. |
| **S2** | Logs `severity` URL'e yaz — `writeUrl` + `urlSig` + import | `Logs.tsx:236,263-276,760-765` | 🟡 orta | **Gerçek correctness bug.** URL-state sig-guard'a dokunuyor → v0.8.253/256/265 bug sınıfı. Regresyon testi şart. |
| **S3** | ApiTokensTab kopyalama düzelt — flash + catch + fallback | `ApiTokensTab.tsx:79-82` | 🟢 düşük | Token kaybı riski. En yüksek etki/satır oranı. |
| **S4** | Non-secure TypeError'ları kapat | `ProblemDetail.tsx:178`, `Profiling.tsx:383` | 🟢 düşük | S5 yapılırsa otomatik çözülür |
| **S5** | **Fallback'i tekleştir** — `lib/clipboard.ts` (Trace'in güçlü sürümü kanonik), 8 yüzey oraya bağlanır | yeni dosya + 8 dosya | 🟡 orta | 3 kopya → 1. **Davranış değişikliği:** 5 yüzey ilk kez fallback kazanır (iyileşme, ama yeni yol) |
| **S6** | `CopyButton`'a `variant`/`size`/`label`/`copiedLabel` props + 5 elle-yazılan yüzeyi devret | `CopyButton.tsx` + ProblemDetail, Profiling, SsoTab, ApiTokensTab, MetricPanel | 🔴 **yüksek** | 9 mevcut çağrı yerinin görünümünü bozma riski. **Mockup-first öneririm.** |
| **S7** | SharePopover internal-copy → `<ShareButton>` gömme; `copyToClipboard`/`fallbackCopy` (`Trace.tsx:891/1135`) ölür | `Trace.tsx` | 🟡 orta | S5'ten sonra yapılırsa trivial. 4. kopya burada ölür. |
| **S8** | Flash süresi 2000→1500ms (SharePopover ×3) + Explore `copiedLabel="Copied"` | `Trace.tsx:962,974,987`, `Explore.tsx:413` | 🟢 düşük | Saf tutarlılık. 4 satır. |
| **S9** | SharePopover a11y — `useOutsideClose` (dosyada zaten `:18/:277`), Esc, `aria-label`, `aria-modal` | `Trace.tsx:942-949,1014` | 🟢 düşük | **Trace.tsx kendi standardını ihlal ediyor** — aynı dosya Esc'i `:205`'te kullanıyor |
| **S10** | Ölü import sil (`Logs.tsx:13`) + `noUnusedLocals` aç | `Logs.tsx`, `tsconfig*.json` | 🟡 orta | `noUnusedLocals` **tüm kod tabanını** tarar → kaç hata çıkacağı **DOĞRULANAMADI**. Önce ölçülmeli. |
| **S11** | `MetricPanel` `⧉` → `IconLink`; menüdeki 6 unicode glif (`⤢ ✎ ⟨⟩ ⧉ ▦ ◔`) | `MetricPanel.tsx:182,211,308` | 🟡 orta | Görsel; **mockup-first** |
| **S12** | **İkon sistemi kararı** — icons.tsx mi lucide mi? 8 ölü export + 10 çift kavram + emoji temizliği | geniş | 🔴 **yüksek** | Ayrı bir iş. Bu audit'in kapsamı değil, sadece ölçüyor. |
| **S13** | 21 hand-rolled `<button style={{` temizliği | 16 dosya | 🔴 **yüksek** | Hepsi düzeltilemez (`AdminAudit:286` `all:'unset'`, `Drawer:41`). Ayrı audit. |
| **S14** | 26 hardcoded `rgba(56,139,253,…)` → token | tsx geneli | 🔴 **yüksek** | **Light/redhat temada yanlış renk** (dark `--accent` donmuş). `globals.css:1201` yorumu bunu zaten biliyor. Ayrı iş. |

**Minimum cevap operatöre:** S1 tek başına şikâyeti kapatır. **Önerim: S1 + S3 + S8** — üçü de düşük risk, toplam ~15 satır, üç ayrı tag.

---

## 7. Doğrulama

**Zemin (değişiklik öncesi ölçülecek):**
```
cd frontend && npx tsc --noEmit          # 0 hata
cd frontend && npx eslint .              # zemin 138 warning / 0 error
cd frontend && npm test                  # zemin 651 geçen
```

**Her dilim için:**

| Dilim | Otomatik | Gözle — hangi sayfa, ne bakılacak |
|---|---|---|
| S1 | tsc | `/trace/<id>` → Share butonu **tinted** (Correlate ◆ / Export JSON ile aynı boy+tonda). Tık → popover hâlâ açılıyor. **Üç temada da** (dark/light/redhat). |
| S2 | **regresyon testi zorunlu** — pure-function, table-driven, header `v0.8.X` (kanonik: `cache_key_test.go`) | `/logs` → ERROR chip → Share → linki **incognito**'da aç → ERROR seçili gelmeli. Chip'i kapat → URL'den `severity` düşmeli. |
| S3 | tsc | `/settings` → API Tokens → yeni token → Kopyala → flash görülmeli. **Düz HTTP** (secure context değil) ile de dene. |
| S4/S5 | tsc + npm test | `/problems` stack Copy · `/profiling` CodeBlock — **düz HTTP'de** çalışmalı, console'da TypeError olmamalı |
| S6 | tsc + **9 çağrı yerinin tamamı gözle** | Trace ID yanı · LogTable trace ID + JSON · SpanDetail stack + attr · Monitors · Profile · MetricPanel query — hiçbiri büyümemeli/kaymamalı |
| S7 | tsc | `/trace/<id>` → Share → "Copy current URL" → yeşil tint + `Copied` |
| S8 | tsc | 4 sayfada flash süreleri aynı hissettirmeli |
| S9 | tsc | `/trace/<id>` → Share → **Esc kapatmalı**, dışarı tık kapatmalı, focus geri dönmeli |
| S10 | tsc (`noUnusedLocals` açıkken hata sayısını **önce ölç**) | — |

**Playwright kullanılmayacak** — gözle kontrol operatöre bırakılır (`feedback-playwright-cadence`).

---

## 8. Açık sorular

1. **`IconCopy` eklensin mi?** `icons.tsx`'te yok. S6/S11 için ya eklenir ya `IconLink` "kopyalama" için de yeterli sayılır. Semantik olarak farklı kavramlar (link ≠ kopya).
2. **`CopyButton`'ın inline glif affordance'ı korunacak mı?** 9 çağrı yerinin çoğu satır içi (ID'nin yanı). Tinted çipe çevirmek görsel gürültü yaratır. **Önerim: korunsun** — ama bu, ailenin iki farklı görünüme sahip olacağı anlamına gelir. Operatör onaylıyor mu?
3. **SharePopover "tek tık = kopyalandı" beklentisi kapatılsın mı?** Ör. tetikleyici doğrudan kopyalayıp *yanında* bir `▾` ile popover açsın (split button). Bu bir **düzen değişikliği** → `feedback-exception-detail-classic` uyarınca **mockup-first**, önermiyorum, sadece soruyorum.
4. **S2 (Logs severity) bu audit'in kapsamında mı, ayrı bugfix mi?** Correctness bug'ı, tasarım dili değil. `/bugfix` akışına daha uygun olabilir.
5. **S12/S13/S14 ayrı audit mi?** Üçü de "tek tasarım dili"nin parçası ama her biri bu işten büyük. Ölçüldüler, kuyruğa alınmadılar.
6. **`scratchpad/wt-413/` silinsin mi?** Bayat worktree, untracked, grep gürültüsü. S1 uygulanırsa senkronlanmayacak.

---

**Onay bekliyor — implementasyona geçilmedi.**
