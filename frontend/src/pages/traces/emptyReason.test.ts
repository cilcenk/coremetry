import { describe, it, expect } from 'vitest';
import { tracesEmptyReason, type TracesEmptyInput, type TracesEmptyReason } from './emptyReason';

// v0.10.530 — Operator-reported (prod): 1 saatlik pencerede "TTL'i aştı"
// denildi, oysa arama metni ham span'lerde geçmiyordu. Ayrım yalnız
// sunucunun yüklemsiz ham sayımıyla yapılabilir; yoksa iddia değil olasılık.
describe('tracesEmptyReason', () => {
  const base: TracesEmptyInput = { narrowed: false, service: 'api-gateway', search: 'UPDATE', mvSpans: 2_400_000, serviceSpans: undefined };
  const cases: Array<[string, Partial<TracesEmptyInput>, TracesEmptyReason]> = [
    ['daraltıldı → bakılamadı, ötekilerin önünde',       { narrowed: true, serviceSpans: 0 },      'narrowed'],
    ['ham span var, eşleşme yok → yüklem (prod vakası)', { serviceSpans: 1_900_000 },             'predicate'],
    ['ham span YOK, MV var → saklama/ingest boşluğu',    { serviceSpans: 0 },                     'aged'],
    ['sunucu ölçemedi → olasılık, iddia değil',          {},                                      'unmeasured'],
    ['servis yok → genel',                               { service: '' , serviceSpans: 0 },       'generic'],
    ['arama yok → genel (eski kapı korunur)',            { search: '', serviceSpans: 0 },         'generic'],
    ['MV boş → genel',                                   { mvSpans: 0, serviceSpans: 0 },         'generic'],
    ['MV okunamadı (null) → genel',                      { mvSpans: null, serviceSpans: 0 },      'generic'],
    ['MV yükleniyor (undefined) → genel',                { mvSpans: undefined, serviceSpans: 0 }, 'generic'],
  ];
  for (const [name, over, want] of cases) {
    it(name, () => expect(tracesEmptyReason({ ...base, ...over })).toBe(want));
  }
});
