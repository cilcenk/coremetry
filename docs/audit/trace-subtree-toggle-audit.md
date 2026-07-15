# Audit — TraceWaterfall'da Alt+click ile subtree aç/kapa

2026-07-15 · HEAD `816fd667` (v0.8.535) · Salt-okunur inceleme, hiçbir dosya
değiştirilmedi. Tek dosya: `frontend/src/components/TraceWaterfall.tsx` (644
satır).

## Kısa hüküm

1. **Brief'in premisi tutmuyor: `expandAll` / `collapseAll` / `parentIds`
   kod tabanında YOK.** Ne TraceWaterfall'da, ne başka bir frontend
   dosyasında, ne çalışma ağacında, ne de git geçmişinde. Bu iş
   "mevcut memo'yu yeniden kullan" değil, **sıfırdan** yapılacak.
2. Gerçek engel `children` map'inin `rows` useMemo'sunun **local'inde**
   olması (`:224`). Subtree toplama ona erişemiyor.
3. Önerilen: `children` + `roots` ayrı bir `tree` memo'suna çıkarılsın
   (bağımlılık **sadece `spans`** — bugün `collapsed`/`groupSimilar`
   değiştiğinde de gereksiz yeniden kuruluyor; ayırmak bir mikro-perf
   kazancı da veriyor).
4. Sentetik `group:` id'leri sorunun **can alıcı** kısmı: `collapsed`
   seti hem gerçek span id'leri hem `group:${parentId}:${i}:${key}`
   taşıyor (`:302`, `:324`). Alt+expand bunları temizlemezse subtree
   "açıldı" ama grup satırları kapalı kalır. Çözüm: **prefix kuralı**
   — id formatı parent'ı taşıdığı için `group:${descendantId}:` ile
   eşleşenler silinir. Ek veri yapısı gerekmiyor.
5. Doğrulama: **vitest var** (54 test dosyası) ama hepsi saf mantık;
   DOM/component testi yok. Bu yüzden subtree toplama **saf fonksiyona**
   çıkarılıp tablo-driven test edilmeli; Alt+click'in kendisi manuel.

---

## 1. Mevcut durum — brief ile farklar

| Brief diyor | Gerçek |
|---|---|
| "header'a `expandAll`/`collapseAll` ekledim" | **Yok.** `grep -rn "expandAll\|collapseAll" frontend/src/` → 0 eşleşme |
| "`parentIds` memo'su var" | **Yok.** 0 eşleşme |
| "`toggle(id, e)` tek span'ı açıp kapatıyor" | ✅ Doğru — `:381-386` |
| "`children: Map<string, SpanRow[]>` dfs'in local'inde" | ✅ Doğru — `:224`, `rows` useMemo'su içinde |
| "sentetik `group:...` id'leri var" | ✅ Doğru — `:302` |
| "`repKids` mantığı" | ✅ Doğru — `:303`, `:327` |

`git log -S "expandAll" --all -- frontend/` iki commit döndürüyor
(`cacfbe4b` silinmiş `Topology.tsx`, `471d8089` v0.5.178 business flows) —
**ikisi de TraceWaterfall değil**. `git status frontend/` temiz. Yani o
kod bu repoya hiç girmemiş; büyük olasılıkla commit'lenmemiş bir oturumda
kaldı.

**Sonuç:** header'da global aç/kapa yok. Bu audit **yalnız** satır
toggle'ının Alt+click davranışını kapsıyor. Header düğmeleri isteniyorsa
ayrı kalem.

## 2. İlgili kod

**State** (`:206-209`):
```tsx
const [collapsed, setCollapsed] = useState<Set<string>>(initialCollapsed);
useEffect(() => { setCollapsed(initialCollapsed); }, [initialCollapsed]);
```

**Ağaç kurulumu** — `rows` useMemo'sunun ilk 10 satırı (`:223-232`):
```tsx
const map = new Map(spans.map(s => [s.spanId, s]));
const children = new Map<string, SpanRow[]>(spans.map(s => [s.spanId, []]));
const roots: SpanRow[] = [];
for (const s of spans) {
  if (s.parentSpanId && map.has(s.parentSpanId)) children.get(s.parentSpanId)!.push(s);
  else roots.push(s);
}
children.forEach(c => c.sort((a, b) => a.startTime - b.startTime));
```
useMemo bağımlılığı `[spans, collapsed, groupSimilar]` (`:341`) — yani
`children` **her collapse tıklamasında** yeniden kuruluyor. Ağaç yalnız
`spans`'a bağlı; ayırmak hem paylaşımı açar hem bu israfı bitirir.

**Toggle** (`:381-386`) ve butonu (`:536-540`):
```tsx
const toggle = (id: string, e: React.MouseEvent) => {
  e.stopPropagation();
  const next = new Set(collapsed);
  if (next.has(id)) next.delete(id); else next.add(id);
  setCollapsed(next);
};
```
```tsx
<button className="wf-toggle" onClick={e => toggle(s.spanId, e)}
        aria-label={isCol ? 'Expand' : 'Collapse'}
        title={isCol ? 'Expand' : 'Collapse'}>
```

**Sentetik grup satırı** (`:302-324`):
```tsx
const synthId = `group:${id}:${i}:${entry.key}`;
const repKids = children.get(entry.rep.spanId) ?? [];
...
if (collapsed.has(synthId)) return;
```
Kritik: `id` = gerçek parent span id'si, `entry.key` =
`serviceName + '\x01' + displayName` (`:246`). Yani sentetik id **kendi
parent'ının gerçek id'sini string olarak taşıyor**.

## 3. Tasarım

### 3.1 `tree` memo'su (ayırma)

`children` + `roots` + `map`, `spans`'a bağlı ayrı bir memo'ya çıkar:
```tsx
const tree = useMemo(() => { /* :223-232'nin aynısı */ return { map, children, roots }; }, [spans]);
```
`rows` useMemo'su `tree`'yi tüketir; bağımlılığı `[tree, collapsed, groupSimilar]`
olur. **Davranış değişmiyor** — sadece kurulum yeri değişiyor. Brief'in
"hangisi mevcut yapıyı daha az bozuyorsa" sorusunun cevabı bu: ayrı bir
yardımcı fonksiyon `spans`'ı ikinci kez gezmek zorunda kalırdı; memo
ayırmak hem tek kaynak bırakıyor hem israfı düşürüyor.

### 3.2 Subtree toplama — saf fonksiyon

Modül seviyesinde, component dışında (test edilebilirlik için):
```tsx
// Gerçek span id'leri; sentetik group: id'leri DEĞİL.
export function collectSubtreeIds(
  children: Map<string, SpanRow[]>, rootId: string,
): string[] {
  const out: string[] = [];
  const stack = [rootId];                    // iteratif — derin trace'te
  while (stack.length) {                      // stack overflow yok
    const id = stack.pop()!;
    out.push(id);
    for (const k of children.get(id) ?? []) stack.push(k.spanId);
  }
  return out;
}
```
İteratif, çünkü mevcut `dfs` (`:286`) recursive ve binlerce derinlikte
patlayabilir; yeni kodda o riski almayalım.

### 3.3 Alt+click davranışı

```tsx
const toggle = (id: string, e: React.MouseEvent) => {
  e.stopPropagation();
  const next = new Set(collapsed);
  const isCol = next.has(id);
  if (!e.altKey) {                       // bugünkü davranış — dokunulmuyor
    if (isCol) next.delete(id); else next.add(id);
    setCollapsed(next); return;
  }
  const realId = resolveRealId(id);      // §3.4
  const sub = collectSubtreeIds(tree.children, realId);
  if (isCol) {
    // AÇ: subtree'deki gerçek id'ler + altlarındaki sentetik grup id'leri
    const subSet = new Set(sub);
    for (const c of next) {
      if (subSet.has(c)) { next.delete(c); continue; }
      const p = groupParentOf(c);        // §3.4
      if (p && subSet.has(p)) next.delete(c);
    }
    next.delete(id);                     // sentetik id ise kendisi de
  } else {
    // KAPA: tıklanan + children'ı olan tüm descendant'lar
    next.add(id);
    for (const d of sub) if ((tree.children.get(d)?.length ?? 0) > 0) next.add(d);
  }
  setCollapsed(next);
};
```

Semantik gerekçe: **kapatmada** descendant'ları da sete eklemek şart,
yoksa kullanıcı sonra bir seviye açtığında altı yine açık gelir —
istenen "subtree kapalı" hissi kaybolur.

### 3.4 Sentetik id'lerin ele alınması

İki küçük saf yardımcı:
```tsx
// "group:<parentId>:<i>:<key>" → "<parentId>"; gerçek id ise null.
export function groupParentOf(id: string): string | null {
  if (!id.startsWith('group:')) return null;
  const rest = id.slice(6);
  const cut = rest.indexOf(':');
  return cut < 0 ? null : rest.slice(0, cut);
}
```
⚠️ **Doğrulanması gereken varsayım:** bu, span id'lerinin `:` içermediğini
varsayar. OTel span id'i 16 hex karakter → `:` içermez. `map`'teki
anahtarlar backend'den geldiği için pratikte güvenli; yine de
`groupParentOf` **yalnız `group:` önekli id'lerde** çağrılıyor, gerçek
id'lere dokunmuyor.

Sentetik satırın toggle'ına Alt+click gelirse: `resolveRealId(synthId)`
= o grubun **rep**'inin span id'si olmalı (satırın altında gerçekten
`repKids` var — `:303`). Bunu id'den türetemeyiz (id `key` taşıyor, rep
id'sini değil). İki seçenek:

| | Yaklaşım | Bedel |
|---|---|---|
| **(a)** | `Row`'a `repSpanId?: string` alanı ekle; `:312`'de doldur | +1 alan, sıfır hesap. **Önerilen** |
| (b) | Alt+click'i sentetik satırlarda yok say (sadece gerçek span'larda) | Daha basit ama tutarsız UX |

(a) öneriliyor: `Row` zaten `groupCount`/`groupTotalDur` gibi grup
alanları taşıyor (`:318-322`), bir alan daha maliyetsiz.

### 3.5 Keşfedilebilirlik

`:538-539`:
```tsx
aria-label={isCol ? 'Expand · Alt+click for subtree' : 'Collapse · Alt+click for subtree'}
title={isCol ? 'Expand · Alt+click for subtree' : 'Collapse · Alt+click for subtree'}
```
Not: Mac'te Option, Win/Linux'ta Alt — `e.altKey` **ikisini de** yakalar,
platform ayrımı gerekmiyor. Metinde "Alt" yazmak Mac kullanıcısı için
hafif yanıltıcı; alternatif "⌥/Alt+click". Operatör tercih etsin.

## 4. Değişecek dosyalar

| Dosya | Değişiklik |
|---|---|
| `frontend/src/components/TraceWaterfall.tsx` | `tree` memo ayrımı; `collectSubtreeIds`/`groupParentOf` modül-seviyesi export; `toggle` altKey dalı; `Row.repSpanId`; toggle `title`/`aria-label` |
| `frontend/src/components/TraceWaterfall.subtree.test.ts` (yeni) | §5 testleri |

**Backend değişikliği yok. Tip/API değişikliği yok.** `TraceWaterfall`'ı
tüketen üç yüzey (`PublicTrace.tsx`, `TraceCompare.tsx`,
`AggregatedStructure.tsx`) prop imzası değişmediği için etkilenmiyor.

## 5. Doğrulama

### Test (vitest — `npm test`)
`collectSubtreeIds` ve `groupParentOf` saf; tablo-driven test edilir:

1. **Düz zincir** A→B→C→D: `collectSubtreeIds(A)` = 4 id.
2. **Dallanma**: iki kardeş alt-ağaç, ikisi de toplanıyor.
3. **Yaprak**: `collectSubtreeIds(leaf)` = `[leaf]` (kendisi dahil).
4. **Bilinmeyen id**: boş children → `[id]`, patlamıyor.
5. **Derinlik**: 5000-derinlik zinciri — iteratif olduğu için stack
   overflow yok (recursive implementasyon burada düşer; bu testin
   varlık sebebi).
6. `groupParentOf('group:abc123:0:svc\x01GET /x')` = `'abc123'`;
   `groupParentOf('abc123')` = `null`.
7. **Collapse semantiği** (saf reducer'a çıkarılırsa): Alt+collapse
   sonrası children'ı olan her descendant sette; yaprakları yok.

`ServicePicker.test.ts` dışında component testi yok; DOM'a girmiyoruz.

### Manuel (deploy sonrası, `/trace/<id>`)
- Derin bir trace aç → bir orta düğümde **Alt+click** → tüm alt ağaç tek
  seferde kapanıyor; tekrar Alt+click → hepsi açılıyor.
- **Normal click** hâlâ tek seviye — regresyon yok.
- `groupSimilar` **açıkken**: bir grup satırının altındaki gerçek span'da
  Alt+click çalışıyor; grup satırının kendisinde Alt+click rep'in
  subtree'sini açıp kapıyor (§3.4a) ve **grup satırları kapalı kalmıyor**
  (prefix temizliği çalışıyor mu — bu maddenin asıl sınavı).
- `defaultCollapsed` ile açılan yüzeyde (varsa) Alt+expand kök seviyeden
  çalışıyor.

### Kapı
`cd frontend && npx tsc --noEmit` → `npm test` → `go build ./...` (etkisiz
ama kapı) → `make audit` → commit → tag → push → deploy.

## 6. Açık sorular — operatör

1. **Header'daki global `expandAll`/`collapseAll` isteniyor mu?** Kodda
   yok. İstenirse ayrı kalem (bu audit kapsamı dışı); §3.1'in `tree`
   memo'su onun da zeminini hazırlıyor.
2. **Sentetik grup satırında Alt+click**: §3.4 (a) mı (rep'in subtree'si,
   `repSpanId` alanı) yoksa (b) mi (yok say)?
3. **İpucu metni**: "Alt+click" mi, Mac'i de anan "⌥/Alt+click" mi?

## Öneri

§3.1 + §3.2 + §3.3 + §3.4(a) + §3.5, **tek release** (`v0.8.536`* —
zincirdeki sıraya göre numara kayabilir). Tek dosya, backend'e
dokunmuyor, mevcut `toggle` davranışı `altKey` dalının dışında birebir
korunuyor. Risk düşük; asıl sınav `groupSimilar` açıkken prefix
temizliği, o da manuel maddede izole edilmiş.

**Onay bekliyor — implementasyona geçilmedi.**
