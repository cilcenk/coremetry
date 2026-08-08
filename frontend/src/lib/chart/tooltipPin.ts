// tooltipPin.ts (Grafana-parite #2) — tıkla→tooltip sabitle (pin) çekirdeği.
// Grafana'nın "click to pin tooltip" davranışı: çizim alanına düz tık hover
// tooltip'ini DONDURUR (imleç gezse de kalır, metin seçilebilir); ikinci tık
// ya da Esc çözer. Karar mantığı saf + testli (tooltipPin.test.ts); DOM'a
// dokunan iki küçük yardımcı (applyPinStyle/clearPinStyle) preset'lerin
// tekrarını önlemek için burada ama test kapsamı dışında (tri̇vyal stil).
//
// Preset-başına tetik (en az çatışmalı tasarım — her preset kendi mevcut
// tık sözleşmesini korur):
//   • OverviewChart / TimeChart: düz tık (çizim alanı tıkı boştaydı).
//   • TimeSeriesPanel: düz tık; exemplar ◆ isabeti ÖNCELİKLİ (trace açar,
//     pin devreye girmez).
//   • MultiLineChart: onBucketClick YOKKEN düz tık; onBucketClick varken
//     (spike→exemplar düz tıkın sahibi) Alt+tık PİNLER — bucket-click
//     dinleyicisi Alt'lı tıkı atlar, iki jest çakışmaz. PİNLİYKEN düz tık
//     yine UNPIN eder (bucket dinleyicisi pinli tıkı işlemez) — aşağıdaki
//     "tık / Esc çözer" ipucu her preset'te doğru kalır.
// Drag kuyruğu: drag-zoom/brush bırakışı da bir `click` üretir; preset
// mousedown→click yatay mesafesini ölçüp dragPx geçirir (uPlot select'i
// setSelect hook'unda sıfırlandığı için u.select.width burada güvenilmez).
// Çift-tık kuyruğu: dblclick'in iki click'i de buraya düşer; detail > 1
// olan tık pin durumuna DOKUNMAZ (pin-unpin flaşı olmasın) — preset'in
// u.over dblclick dinleyicisi pin'i deterministik çözer (zoom-geri sonrası
// bayat pinli tooltip kalamaz; jitter'lı re-pin senaryosu kapanır).

export type PinDecision =
  | { action: 'pin'; idx: number }
  | { action: 'unpin' }
  | { action: 'ignore' };

// decidePinClick — çizim alanı tıkının pin durumuna etkisi.
//   çift-tık click'i (detail > 1)        → ignore (dblclick dinleyicisi çözer)
//   drag kuyruğu (dragPx >= eşik)        → ignore (pin'e DOKUNMA — zoom jesti)
//   zaten pinli                          → unpin (ikinci tık çözer)
//   imleç veri noktasında değil (idx yok)→ ignore (boş tooltip pinlenmez)
//   aksi halde                           → pin (imlecin veri index'i sabitlenir)
export function decidePinClick(args: {
  pinnedIdx: number | null;
  cursorIdx: number | null | undefined;
  // mousedown→click yatay px mesafesi; >= dragThresholdPx = drag kuyruğu.
  dragPx?: number | null;
  // Preset'in brush/zoom eşiğiyle AYNI değer geçilir (OVC/MLC/TSP default 4,
  // TC 2 = buildCursorOpts minWidthPx). >= karşılaştırma selectRangeSec'in
  // `width < minWidthPx → null` kuralının tam tümleyeni: zoom'un ateşlediği
  // her drag pin'den elenir, tek px'lik "hem zoom hem pin" boşluğu kalmaz.
  dragThresholdPx?: number;
  // MouseEvent.detail — 2+ = çift-tık dizisinin click'i.
  detail?: number;
}): PinDecision {
  const { pinnedIdx, cursorIdx, dragPx, dragThresholdPx = 4, detail } = args;
  if (detail != null && detail > 1) return { action: 'ignore' };
  if (dragPx != null && dragPx >= dragThresholdPx) return { action: 'ignore' };
  if (pinnedIdx != null) return { action: 'unpin' };
  if (cursorIdx == null || cursorIdx < 0) return { action: 'ignore' };
  return { action: 'pin', idx: cursorIdx };
}

// ── decidePinGesture (v0.9.792) — MODİFİYELİ pin + tık ZİNCİRİ yönlendirmesi ─
//
// CorePanel'in çizim alanı tıkı DOLU: ◆ exemplar isabeti > bucket-tık >
// panel eylemi (v0.9.744/789 sırası). Düz tıkı pin'e vermek o zinciri
// kırardı, o yüzden pin tetiği MODİFİYELİ: Shift+tık (operatör onaylı jest)
// ya da Alt+tık (MLC v1 kas hafızası, MultiLineChart:387).
//
// Bu fonksiyon decidePinClick'in ÜSTÜNE tek bir soruyu ekler: "bu tıkı pin
// mi sahiplendi, yoksa çağıranın kendi zinciri mi işlemeli?" Dört cevap:
//   • pin / unpin  → pin durumu değişti, tık BURADA bitti.
//   • swallow      → jest pin'e aitti ama bir şey değişmedi (sürükleme
//                    kuyruğu, çift-tık click'i, boş imleç). Tık YUTULUR:
//                    Shift'li bir sürükleme bir exemplar çekmecesi açamaz.
//   • passthrough  → pin ilgilenmiyor; çağıran ◆/bucket/panel zincirini
//                    aynen sürdürür.
//
// MLC v1 sözleşmesinin (:387) birebiri PİNLİYKEN görülür: pin varken HER
// tık — düz olsun modifiyeli olsun — unpin'e gider ve bucket'a DÜŞMEZ.
// "tık / Esc çözer" ipucu böylece her preset'te doğru kalır.
export type PinGesture =
  | { action: 'pin'; idx: number }
  | { action: 'unpin' }
  | { action: 'swallow' }
  | { action: 'passthrough' };

export function decidePinGesture(args: {
  pinnedIdx: number | null;
  cursorIdx: number | null | undefined;
  // Pin tetikleyen modifiyeler — ikisi de aynı kapıya çıkar.
  shiftKey?: boolean;
  altKey?: boolean;
  dragPx?: number | null;
  dragThresholdPx?: number;
  detail?: number;
}): PinGesture {
  const wantsPin = !!args.shiftKey || !!args.altKey;
  // Modifiyesiz tık + pin yok = pin'in işi değil; zincir çağıranda sürer.
  if (!wantsPin && args.pinnedIdx == null) return { action: 'passthrough' };
  const d = decidePinClick(args);
  if (d.action === 'unpin') return { action: 'unpin' };
  if (d.action === 'pin') return { action: 'pin', idx: d.idx };
  return { action: 'swallow' };
}

// ── DOM yardımcıları (saf değil; preset'lerde 4× kopyayı önler) ─────────────

// Pin görsel ipucu — tooltip içeriği pin süresince yeniden yazılmadığından
// başa eklenen satır kalıcıdır; unpin sonrası ilk setCursor innerHTML'i
// yeniden yazar ve satır kendiliğinden gider (clearPinStyle yine de siler).
const PIN_HINT_HTML =
  '<div class="tt-pin-hint" style="display:flex;align-items:center;gap:5px;' +
  'margin-bottom:4px;padding-bottom:4px;border-bottom:1px solid var(--border);' +
  'color:var(--text2);font-size:10px">\u{1F4CC} sabit — tık / Esc çözer</div>';

// applyPinStyle — tooltip'i pinli moda al: fare girebilir (metin kopyalanır),
// kenarlık accent'e döner, başa 📌 ipucu satırı eklenir. İçerik/pozisyon
// preset'in setCursor guard'ı sayesinde donuk kalır.
export function applyPinStyle(tip: HTMLElement): void {
  tip.style.pointerEvents = 'auto';
  tip.style.borderColor = 'var(--accent)';
  if (!tip.querySelector('.tt-pin-hint')) tip.insertAdjacentHTML('afterbegin', PIN_HINT_HTML);
}

// clearPinStyle — pin stilini geri al + tooltip'i gizle (bir sonraki hover
// taze içerikle açar). hide: OVC/TC display:none kullanır, MLC/TSP opacity.
// borderColor '' → inline override kalkar, CSS/inline taban rengi geri gelir.
export function clearPinStyle(tip: HTMLElement, hide: 'display' | 'opacity'): void {
  tip.style.pointerEvents = 'none';
  tip.style.borderColor = '';
  tip.querySelector('.tt-pin-hint')?.remove();
  if (hide === 'display') tip.style.display = 'none';
  else tip.style.opacity = '0';
}
