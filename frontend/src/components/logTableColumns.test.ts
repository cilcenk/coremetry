import { describe, it, expect } from 'vitest';
import { normalizeLogColumns, DEFAULT_LOG_COLUMNS } from './LogTable';

// v0.9.489 (operatör: "level kalkabilir; TIME cluster Message service ve
// pod şeklinde olabilir, trace de sonda; aynı pattern Logs sayfasında da")
// — kolon sözleşmesi pinleri.
describe('normalizeLogColumns (v0.9.489)', () => {
  it('varsayılan düzen: cluster · message · service · pod (level YOK)', () => {
    expect(DEFAULT_LOG_COLUMNS).toEqual(['cluster', 'message', 'service', 'pod']);
    expect(normalizeLogColumns(undefined)).toEqual(['cluster', 'message', 'service', 'pod']);
  });

  it("eski kayıtlı tercih / eski ?cols= (message'sız) → message SONA eklenir (eski anatomi)", () => {
    expect(normalizeLogColumns(['level', 'service', 'cluster', 'pod']))
      .toEqual(['level', 'service', 'cluster', 'pod', 'message']);
  });

  it('message listedeyse konumu korunur', () => {
    expect(normalizeLogColumns(['message', 'service']))
      .toEqual(['message', 'service']);
  });

  it('frame id\'leri (time/trace) gölgelenemez', () => {
    expect(normalizeLogColumns(['time', 'cluster', 'trace', 'message']))
      .toEqual(['cluster', 'message']);
  });

  it('level artık varsayılanda değil ama açıkça istenirse yaşar', () => {
    expect(normalizeLogColumns(['level', 'message'])).toEqual(['level', 'message']);
  });
});
