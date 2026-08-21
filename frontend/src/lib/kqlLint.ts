// lib/kqlLint.ts — v0.9.1216 (Kibana paritesi, dilim 4). KQL kutusunun
// GÖNDERİM ÖNCESİ hafif sözdizimi denetimi. SAF + tablo-testli.
//
// Neden: bozuk bir sorgu bugün ES'e gidip "Query failed" olarak geri
// dönüyor — yani her dengesiz tırnak bir BAŞARISIZ _search turu. Denetim
// istemcide durdurunca hem hata gönderim ANINDA görünür hem ES turu hiç
// atılmaz (operatörün "elastic kullanımını artırma" şartının tersine,
// AZALTIR). Yalnız NESNEL kırıklar engellenir: dengesiz tırnak/parantez
// ve asılı boolean operatör — üçü de query_string'de sözdizimi hatası.
// Şüpheli-ama-geçerli hiçbir şey engellenmez (yanlış pozitif, operatörü
// kilitler).

const DANGLING_OP_RE = /(?:^|[\s(])(AND|OR|NOT)\s*$/;
const LEADING_OP_RE = /^\s*(AND|OR)(?:[\s)]|$)/;

export function kqlLint(q: string): string | null {
  const t = q.trim();
  if (!t) return null;

  let quotes = 0;
  let depth = 0;
  let inQuotes = false;
  for (let i = 0; i < t.length; i++) {
    const c = t[i];
    if (c === '"' && t[i - 1] !== '\\') {
      quotes++;
      inQuotes = !inQuotes;
      continue;
    }
    if (inQuotes) continue;
    if (c === '(') depth++;
    else if (c === ')') {
      depth--;
      if (depth < 0) return 'Fazla kapama parantezi — eşleşen "(" yok.';
    }
  }
  if (quotes % 2 === 1) return 'Kapanmamış tırnak — çift tırnağı kapatın.';
  if (depth > 0) return 'Kapanmamış parantez — ")" eksik.';
  if (DANGLING_OP_RE.test(t)) return 'Sonda asılı operatör (AND/OR/NOT) — sağına bir terim ekleyin.';
  if (LEADING_OP_RE.test(t)) return 'Sorgu AND/OR ile başlayamaz — soluna bir terim ekleyin.';
  return null;
}
