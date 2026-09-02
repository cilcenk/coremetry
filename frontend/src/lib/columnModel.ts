// columnModel.ts — v0.10.246 DataTable/ContextBar dilim 1 (audit §9/§11).
//
// Kalıcı sütun tercihi modeli: sıra + gizli + (tarayıcı-yerel) genişlik +
// düzen imzası. SAF; React/DOM yok. Kurallar (audit, panel kararı):
//   • bilinmeyen id düşer, YENİ id bildirilen konumuna (kolon tanımındaki
//     sırasına) girer — kayıtlı model kolon setinin eskimesine dayanır;
//   • hideable:false (kimlik kolonu) asla gizlenmez;
//   • düzen imzası (columnLayoutSig) uyuşmazsa YALNIZ genişlikler düşer,
//     order/hidden kalır — "her genişlik değişikliği herkesin kolon
//     seçimini sıfırlar" reddedildi;
//   • kaynak önceliği: URL `cols=` > sunucu tercihi > localStorage >
//     varsayılan; boş-dize `cols=` "yok" sayılır (Traces v0.9.841 davranışı).

export interface ColumnModel {
  v: 1;
  order: string[];
  hidden: string[];
  widths?: Record<string, number>;
  sig: string;
}

export interface ColumnSpec {
  id: string;
  hideable?: boolean; // false = kimlik kolonu
}

export type ColumnSource = 'url' | 'server' | 'local' | 'default';

export const COLUMN_MODEL_V = 1 as const;

/** Boş model — kolon tanımının sırası, hiçbiri gizli değil. */
export function defaultColumnModel(columns: ColumnSpec[], sig: string): ColumnModel {
  return { v: COLUMN_MODEL_V, order: columns.map(c => c.id), hidden: [], sig };
}

/**
 * reconcileColumnModel — kayıtlı modeli bugünkü kolon setiyle uzlaştırır.
 * Bilinmeyen id'ler düşer; yeni id'ler tanımdaki komşusundan hemen sonra
 * (tanım sırası korunarak) eklenir; gizlenemez kolonlar hidden'dan çıkar;
 * sig uyuşmazsa widths düşer, sig güncellenir.
 */
export function reconcileColumnModel(model: ColumnModel | null | undefined, columns: ColumnSpec[], sig: string): ColumnModel {
  const known = new Set(columns.map(c => c.id));
  if (!model || model.v !== COLUMN_MODEL_V) return defaultColumnModel(columns, sig);
  const kept = model.order.filter((id, i, arr) => known.has(id) && arr.indexOf(id) === i);
  const order = kept.slice();
  // Yeni kolon: tanımda kendinden önce gelen, sırada mevcut olan en yakın
  // komşunun arkasına; hiç yoksa tanım konumuna göre başa/sona.
  columns.forEach((c, idx) => {
    if (order.includes(c.id)) return;
    let insertAt = -1;
    for (let j = idx - 1; j >= 0; j--) {
      const pos = order.indexOf(columns[j].id);
      if (pos >= 0) { insertAt = pos + 1; break; }
    }
    if (insertAt < 0) insertAt = idx === 0 ? 0 : order.length;
    order.splice(insertAt, 0, c.id);
  });
  const hideable = new Map(columns.map(c => [c.id, c.hideable !== false]));
  const hidden = model.hidden.filter((id, i, arr) => known.has(id) && hideable.get(id) && arr.indexOf(id) === i);
  const out: ColumnModel = { v: COLUMN_MODEL_V, order, hidden, sig };
  if (model.widths && model.sig === sig) {
    const widths: Record<string, number> = {};
    for (const [id, w] of Object.entries(model.widths)) {
      if (known.has(id) && Number.isFinite(w) && w > 0) widths[id] = w;
    }
    if (Object.keys(widths).length) out.widths = widths;
  }
  return out;
}

/** visibleColumnIds — sıra eksi gizli. */
export function visibleColumnIds(model: ColumnModel): string[] {
  const hidden = new Set(model.hidden);
  return model.order.filter(id => !hidden.has(id));
}

/** toggleHidden — gizle/göster; gizlenemez kolon için model aynen döner. */
export function toggleHidden(model: ColumnModel, id: string, columns: ColumnSpec[]): ColumnModel {
  const spec = columns.find(c => c.id === id);
  if (!spec || spec.hideable === false || !model.order.includes(id)) return model;
  const hidden = model.hidden.includes(id) ? model.hidden.filter(h => h !== id) : [...model.hidden, id];
  return { ...model, hidden };
}

/** moveColumnTo — id'yi hedef konuma taşır (0..order.length-1). */
export function moveColumnTo(model: ColumnModel, id: string, index: number): ColumnModel {
  const from = model.order.indexOf(id);
  if (from < 0) return model;
  const order = model.order.slice();
  order.splice(from, 1);
  const to = Math.max(0, Math.min(order.length, index));
  order.splice(to, 0, id);
  return { ...model, order };
}

/**
 * parseColsParam — URL `cols=` → görünür id listesi; boş/yalnız-virgül →
 * null ("yok" sayılır, bir sonraki kaynağa düşer).
 */
export function parseColsParam(raw: string | null | undefined): string[] | null {
  if (raw == null) return null;
  const ids = raw.split(',').map(s => s.trim()).filter(Boolean);
  return ids.length ? Array.from(new Set(ids)) : null;
}

/** modelFromVisible — görünür id listesinden model (URL/varsayılan girişleri). */
export function modelFromVisible(visible: string[], columns: ColumnSpec[], sig: string): ColumnModel {
  const known = new Set(columns.map(c => c.id));
  const vis = visible.filter(id => known.has(id));
  const visSet = new Set(vis);
  const rest = columns.map(c => c.id).filter(id => !visSet.has(id));
  const hidden = rest.filter(id => columns.find(c => c.id === id)?.hideable !== false);
  return reconcileColumnModel({ v: COLUMN_MODEL_V, order: [...vis, ...rest], hidden, sig }, columns, sig);
}

export interface ColumnSourceInput {
  urlCols?: string | null;            // ?cols=
  server?: ColumnModel | null;        // /api/preferences
  local?: ColumnModel | null;         // localStorage
  columns: ColumnSpec[];
  sig: string;
}

/**
 * resolveColumnModel — kaynak öncelik tablosu (audit §11): URL > sunucu >
 * yerel > varsayılan. Her aday uzlaştırılır (eski model, yeni kolon seti).
 */
export function resolveColumnModel(input: ColumnSourceInput): { model: ColumnModel; source: ColumnSource } {
  const url = parseColsParam(input.urlCols);
  if (url) return { model: modelFromVisible(url, input.columns, input.sig), source: 'url' };
  if (input.server) return { model: reconcileColumnModel(input.server, input.columns, input.sig), source: 'server' };
  if (input.local) return { model: reconcileColumnModel(input.local, input.columns, input.sig), source: 'local' };
  return { model: defaultColumnModel(input.columns, input.sig), source: 'default' };
}

/** serializeColumnModel — sunucuya/LS'ye giden JSON (genişlikler HARİÇ: tarayıcı-yerel). */
export function serializeColumnModel(model: ColumnModel): string {
  return JSON.stringify({ v: model.v, order: model.order, hidden: model.hidden, sig: model.sig });
}

/** parseColumnModel — bozuk/yabancı JSON → null (tablo asla kilitlenmez). */
export function parseColumnModel(raw: unknown): ColumnModel | null {
  let v: unknown = raw;
  if (typeof raw === 'string') {
    try { v = JSON.parse(raw); } catch { return null; }
  }
  if (!v || typeof v !== 'object') return null;
  const o = v as Record<string, unknown>;
  if (o.v !== COLUMN_MODEL_V || !Array.isArray(o.order) || !Array.isArray(o.hidden)) return null;
  const strs = (a: unknown[]) => a.filter((x): x is string => typeof x === 'string' && x.length > 0 && x.length <= 64);
  const out: ColumnModel = { v: COLUMN_MODEL_V, order: strs(o.order), hidden: strs(o.hidden), sig: typeof o.sig === 'string' ? o.sig : '' };
  if (o.widths && typeof o.widths === 'object') {
    const widths: Record<string, number> = {};
    for (const [k, w] of Object.entries(o.widths as Record<string, unknown>)) {
      if (typeof w === 'number' && Number.isFinite(w) && w > 0) widths[k] = w;
    }
    if (Object.keys(widths).length) out.widths = widths;
  }
  return out;
}
