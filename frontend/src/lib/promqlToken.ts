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
