// Trace.grpcmsgs.pin.test.ts — v0.10.577.
//
// gRPC per-message event filtresinin KAPSAM SINIRI. Filtre yalnız Logs
// sekmesine ait; ham `eventRows` waterfall'ın satır-içi log çiplerini
// beslemeye devam etmeli. Bu ayrım tek bir değişken adına dayanıyor ve
// yanlış olanı geçirmek TİP HATASI VERMEZ (ikisi de LogRow[]) — yani
// tsc, lint ve mevcut testlerin hiçbiri yakalamaz. Sessiz sızıntı.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';

const SRC = readFileSync(new URL('./Trace.tsx', import.meta.url), 'utf8');

// Yorumlar hariç okunur: bu dosyanın yorumları değişken adlarını ANLATIYOR,
// tarama onlara takılırsa yanlış yeşil verir (aynı tuzağa bu oturumda iki
// kez düşüldü — cancellation.test.ts'in stripComments notu).
const CODE = SRC.replace(/\/\/.*$/gm, '').replace(/\/\*[\s\S]*?\*\//g, '');

describe('gRPC mesaj event filtresi — kapsam sınırı', () => {
  it('waterfall çipleri HAM listeyi alır', () => {
    expect(CODE).toMatch(/perSpanLogSignals\(\s*logsQuery\.data\?\.logs,\s*eventRows\s*\)/);
    expect(CODE).not.toMatch(/perSpanLogSignals\([^)]*shownEventRows/);
  });

  it('Logs paneli SÜZÜLMÜŞ listeyi alır', () => {
    expect(CODE).toMatch(/eventRows=\{shownEventRows\}/);
  });

  it('sekme sayacı da süzülmüş listeyi sayar — etiket panelle çelişmesin', () => {
    expect(CODE).toMatch(/logs\.length \+ shownEventRows\.length/);
  });

  it('tercih localStorage anahtarıyla kalıcı', () => {
    expect(CODE).toMatch(/STORAGE_KEYS\.traceShowGrpcMsgs/);
  });

  it('varsayılan GİZLİ — tercih ancak açıkça "1" ise gösterir', () => {
    expect(CODE).toMatch(/getRaw\(STORAGE_KEYS\.traceShowGrpcMsgs\) === '1'/);
  });
});
