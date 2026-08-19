// stepPreview — ⚙ çipinin arkasındaki tool çıktısının ÇİZİLEBİLİR hâli
// (v0.9.1181, AI Faz 4.3). SAF; testi stepPreview.test.ts.
//
// Neden ayrı dosya: karar ağacı ("bu JSON tabloya döner mi") tamamen saf ve
// tamamen kenar durumlardan ibaret — boş dizi, karışık anahtarlar, iç içe
// değerler, kırpılmış (dolayısıyla ayrıştırılamaz) gövde. React içine
// gömülseydi hiçbiri koşturularak denenemezdi.
//
// Tasarımın tek kuralı: ASLA uydurma. Ayrıştırılamayan gövde HAM gösterilir;
// "veri bozuk" deyip gizlemek ya da tahmini bir şekle zorlamak, bu dilimin
// varlık sebebinin (kanıtı görünür kılmak) tam tersi olurdu.

export type StepPreviewView =
  | { kind: 'table'; cols: string[]; rows: string[][]; note?: string }
  | { kind: 'text'; text: string };

// Tabloya dönmeye değer satır sayısı tavanı. Çipin altındaki blok bir
// veri sayfası değil, bir kanıt bakışı; 50 satırdan sonrası zaten
// kaydırma demek ve ham JSON o noktada daha dürüst (kırpmayı gizlemez).
const MAX_TABLE_ROWS = 50;

function isPrimitive(v: unknown): boolean {
  return v === null || ['string', 'number', 'boolean'].includes(typeof v);
}

function cell(v: unknown): string {
  if (v === null || v === undefined) return '';
  if (typeof v === 'string') return v;
  return JSON.stringify(v);
}

/**
 * pickRows — gövdenin içindeki "satır dizisi"ni bulur.
 *
 * İki şekil kabul edilir çünkü tool'lar ikisini de üretiyor:
 *   [ {...}, {...} ]                 → doğrudan
 *   { rows: [...] } / { items: [...] } / tek dizi alanı → sarmalanmış
 * İkincisi tek-dizi-alanı kuralına bağlı: iki dizi alanı varsa hangisinin
 * "asıl" olduğu tahmin işidir ve tahmin etmiyoruz.
 */
function pickRows(v: unknown): Record<string, unknown>[] | null {
  if (Array.isArray(v)) {
    return v.every(r => r && typeof r === 'object' && !Array.isArray(r))
      ? (v as Record<string, unknown>[])
      : null;
  }
  if (!v || typeof v !== 'object') return null;
  const arrays = Object.values(v as Record<string, unknown>).filter(Array.isArray);
  if (arrays.length !== 1) return null;
  return pickRows(arrays[0]);
}

export function parseStepPreview(preview: string, truncated = false): StepPreviewView {
  const raw = (preview ?? '').trim();
  if (!raw) return { kind: 'text', text: '' };

  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    // Kırpılmış gövde neredeyse HER ZAMAN buraya düşer (yapının ortasından
    // kesilmiştir) ve bu doğru davranış: yarım JSON'u "onarmak" olmayan
    // veriyi uydurmak olurdu.
    return { kind: 'text', text: raw };
  }

  const rows = pickRows(parsed);
  if (!rows || rows.length === 0) {
    return { kind: 'text', text: JSON.stringify(parsed, null, 2) };
  }
  if (rows.length > MAX_TABLE_ROWS) {
    return { kind: 'text', text: JSON.stringify(parsed, null, 2) };
  }
  // Sütunlar: BÜTÜN satırların anahtarlarının birleşimi, ilk görülme
  // sırasında. İlk satırı örnek almak, sonraki satırlarda beliren bir
  // alanı sessizce yutardı.
  const cols: string[] = [];
  for (const r of rows) {
    for (const k of Object.keys(r)) if (!cols.includes(k)) cols.push(k);
  }
  // İç içe değer taşıyan satırlar tabloya sığmaz; hücrede JSON parçası
  // göstermek okunmaz olur, o yüzden gövdenin tamamı ham gider.
  const flat = rows.every(r => Object.values(r).every(isPrimitive));
  if (!flat || cols.length === 0) {
    return { kind: 'text', text: JSON.stringify(parsed, null, 2) };
  }
  return {
    kind: 'table',
    cols,
    rows: rows.map(r => cols.map(c => cell(r[c]))),
    note: truncated ? 'kırpılmış gövdeden' : undefined,
  };
}

/** fmtBytes — kırpma etiketindeki boy ("4.0 KB'ın ilk 4 KB'ı"). */
export function fmtPreviewBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0 B';
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}
