// logFieldTypes.ts — v0.10.280 (log-search audit Dilim 1b): alan paneli tip
// rozeti. Backend /api/logs/fields `types` haritasında ES mapping tipini
// (keyword/text/long/date/boolean/ip…) ya da CH sabit şemasını döner; burada
// tek harfli glif + okunur etiket'e indirgenir. Saf — tablo-testli.

export interface LogFieldGlyph { glyph: string; label: string }

export function logFieldGlyph(type: string | undefined): LogFieldGlyph | null {
  switch (type) {
    case 'keyword': case 'ip': case 'constant_keyword': case 'wildcard':
      return { glyph: 'k', label: 'keyword' };
    case 'text': case 'match_only_text':
      return { glyph: 't', label: 'text' };
    case 'long': case 'integer': case 'short': case 'byte': case 'double': case 'float':
    case 'half_float': case 'scaled_float': case 'unsigned_long':
      return { glyph: '#', label: 'number' };
    case 'date': case 'date_nanos':
      return { glyph: '◷', label: 'date' };
    case 'boolean':
      return { glyph: '◐', label: 'boolean' };
  }
  return type ? { glyph: '·', label: type } : null;
}
