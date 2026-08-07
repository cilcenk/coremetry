// promqlToken (v0.9.766) — PromQL autocomplete'in saf çekirdeği:
// imleç altındaki metrik-adı token'ı ve yerine-yazma. OTel noktalı
// adlar (http.server.request.duration) tek token sayılır. Süslü
// parantez içi (label matcher) Faz 2'nin işi — orada ad önerilmez.

const TOKEN_CH = /[A-Za-z0-9_.:]/;

export interface PromqlToken {
  text: string;  // token içeriği
  start: number; // metin içinde başlangıç
  end: number;   // bitiş (exclusive) — imleç konumu
}

// promqlTokenAt — imlecin HEMEN solundaki token (imleç token içindeyse
// başa kadar geriler). Token yoksa / süslü parantez içindeyse null.
export function promqlTokenAt(text: string, pos: number): PromqlToken | null {
  if (pos < 0 || pos > text.length) return null;
  // Süslü parantez içi mi? pos'tan geriye en yakın { / } hangisi.
  for (let i = pos - 1; i >= 0; i--) {
    const c = text[i];
    if (c === '}') break;
    if (c === '{') return null; // matcher içi — Faz 2
  }
  let start = pos;
  while (start > 0 && TOKEN_CH.test(text[start - 1])) start--;
  if (start === pos) return null;
  const tok = text.slice(start, pos);
  // Sayıyla başlayan şey metrik adı değildir (5m, 0.95 gibi).
  if (/^[0-9]/.test(tok)) return null;
  return { text: tok, start, end: pos };
}

// replaceToken — token'ı adla değiştirir; yeni imleç konumunu döndürür.
export function replaceToken(text: string, tok: PromqlToken, name: string): { text: string; pos: number } {
  const next = text.slice(0, tok.start) + name + text.slice(tok.end);
  return { text: next, pos: tok.start + name.length };
}

// ── Faz 2 (v0.9.771) — süslü parantez içi label KEY/VALUE bağlamı ──────────
// promqlTokenAt'in null döndürdüğü yer tam olarak burasının başladığı yer:
// `metric{` ile eşleşen `}` arasında imleç varsa, önerilecek şey metrik adı
// değil label anahtarı ya da değeridir. Saf ve tırnak-farkında: değerin
// içindeki virgül matcher'ı bölmez, değerin içindeki `!=` operatör sanılmaz.

export interface PromqlLabelCtx {
  metric: string;                 // '{' öncesindeki tanımlayıcı ('' ise null döndürülür)
  phase: 'key' | 'value';
  key?: string;                   // value fazında: operatörün solundaki anahtar (trim)
  partial: string;                // tamamlanmakta olan parça
  start: number; end: number;     // partial'ın metindeki aralığı (replace hedefi)
  quoted: boolean;                // value fazında açık tırnak var mı
}

// Matcher operatörleri — aynı konumda UZUN olan kazanır ('=~' vs '=').
const LABEL_OPS = ['=~', '!~', '!=', '='] as const;

// openBraceBefore — imlecin içinde bulunduğu EN İÇTEKİ eşleşmemiş '{'.
// İleri yönlü tarama: geriye doğru "en yakın { bul" saf hâli tırnak içindeki
// süslüye ('route="/a{b"') takılırdı; ileri tarama tırnak durumunu zaten
// taşıdığı için ikisini tek geçişte doğru yapar. Yoksa -1.
function openBraceBefore(text: string, pos: number): number {
  const stack: number[] = [];
  let inQ = false;
  for (let i = 0; i < pos; i++) {
    const c = text[i];
    if (inQ) {
      if (c === '\\') { i++; continue; } // kaçış: sıradaki karakter veri
      if (c === '"') inQ = false;
      continue;
    }
    if (c === '"') inQ = true;
    else if (c === '{') stack.push(i);
    else if (c === '}') stack.pop();
  }
  return stack.length ? stack[stack.length - 1] : -1;
}

// lastTopLevelComma — [from,to) aralığındaki, TIRNAK DIŞINDAKİ son virgül.
// Yoksa -1. `{a="x,y` içindeki virgül segmenti bölmez.
function lastTopLevelComma(text: string, from: number, to: number): number {
  let inQ = false, last = -1;
  for (let i = from; i < to; i++) {
    const c = text[i];
    if (inQ) {
      if (c === '\\') { i++; continue; }
      if (c === '"') inQ = false;
      continue;
    }
    if (c === '"') inQ = true;
    else if (c === ',') last = i;
  }
  return last;
}

// findOp — segmentteki ilk matcher operatörü (index + uzunluk). Soldan sağa
// tarar ve her konumda önce iki-karakterlileri dener; bir tırnak açılınca
// durur. Böylece `a="x!=y` içindeki `!=` operatör sanılmaz — operatör her
// zaman ilk tırnaktan ÖNCEDİR.
function findOp(seg: string): { at: number; len: number } | null {
  for (let i = 0; i < seg.length; i++) {
    if (seg[i] === '"') return null;
    for (const op of LABEL_OPS) {
      if (seg.startsWith(op, i)) return { at: i, len: op.length };
    }
  }
  return null;
}

// promqlLabelContext — imleç bir label-matcher listesinin içindeyse ne
// önerileceğini söyler. Değilse (süslü yok / kapanmış / metrik adı yok) null;
// çağıran o zaman Faz 1'in metrik-adı yoluna düşer.
export function promqlLabelContext(text: string, pos: number): PromqlLabelCtx | null {
  if (pos < 0 || pos > text.length) return null;
  const brace = openBraceBefore(text, pos);
  if (brace < 0) return null;
  // '{' öncesindeki bitişik tanımlayıcı = metrik adı. Çıplak selector
  // (`{job="x"}`) Faz kapsamı dışı: neyin etiketlerini soracağımızı bilemeyiz.
  let ms = brace;
  while (ms > 0 && TOKEN_CH.test(text[ms - 1])) ms--;
  const metric = text.slice(ms, brace);
  if (!metric) return null;

  // Aktif segment = son üst-seviye virgülden sonrası (yoksa '{' sonrası).
  const comma = lastTopLevelComma(text, brace + 1, pos);
  const segStart = comma >= 0 ? comma + 1 : brace + 1;
  const seg = text.slice(segStart, pos);

  const op = findOp(seg);
  if (!op) {
    // Anahtar fazı — segmentin baş boşlukları partial'a dahil değil.
    let lead = 0;
    while (lead < seg.length && /\s/.test(seg[lead])) lead++;
    const partial = seg.slice(lead).trim();
    const start = segStart + lead;
    return { metric, phase: 'key', partial, start, end: start + partial.length, quoted: false };
  }

  const key = seg.slice(0, op.at).trim();
  const rest = seg.slice(op.at + op.len);
  // Operatörün sağında AÇIK kalmış tırnak var mı — varsa partial o tırnaktan
  // sonra başlar (değerin içindeki boşluk anlamlıdır, trim YOK).
  let inQ = false, openAt = -1;
  for (let i = 0; i < rest.length; i++) {
    const c = rest[i];
    if (inQ) {
      if (c === '\\') { i++; continue; }
      if (c === '"') { inQ = false; openAt = -1; }
      continue;
    }
    if (c === '"') { inQ = true; openAt = i; }
  }
  let partial: string;
  if (inQ) {
    partial = rest.slice(openAt + 1);
  } else {
    // Tırnaksız: `route=|` — baş boşlukları at, gerisi partial.
    let lead = 0;
    while (lead < rest.length && /\s/.test(rest[lead])) lead++;
    partial = rest.slice(lead);
  }
  return { metric, phase: 'value', key, partial, start: pos - partial.length, end: pos, quoted: inQ };
}

// applyLabelKey — partial'ı seçilen anahtarla değiştirir; imleç anahtarın
// sonunda kalır (operatörü operatör yazar, biz varsaymayız).
export function applyLabelKey(text: string, ctx: PromqlLabelCtx, key: string): { text: string; pos: number } {
  const next = text.slice(0, ctx.start) + key + text.slice(ctx.end);
  return { text: next, pos: ctx.start + key.length };
}

// applyLabelValue — değeri TIRNAKLI yazar ve imleci kapanış tırnağının
// SONRASINA koyar: bir sonraki tuş vuruşu ',' ya da '}' olabilsin. Tırnak
// zaten açıksa yalnız kapanışı ekler; kapanış da zaten oradaysa (imleç var
// olan tırnakların arasında) hiç eklemez, sadece üstünden atlar.
export function applyLabelValue(text: string, ctx: PromqlLabelCtx, value: string): { text: string; pos: number } {
  const esc = value.replace(/\\/g, '\\\\').replace(/"/g, '\\"');
  if (ctx.quoted) {
    const closed = text[ctx.end] === '"';
    const next = text.slice(0, ctx.start) + esc + (closed ? '' : '"') + text.slice(ctx.end);
    return { text: next, pos: ctx.start + esc.length + 1 };
  }
  const next = text.slice(0, ctx.start) + '"' + esc + '"' + text.slice(ctx.end);
  return { text: next, pos: ctx.start + esc.length + 2 };
}
