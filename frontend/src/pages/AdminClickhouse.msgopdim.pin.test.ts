// AdminClickhouse.msgopdim.pin.test.ts — v0.10.564.
//
// Sözleşme: messaging_summary_5m `operation` boyutunun YERİNDE geçişi bir
// sihirbaz panelidir ve deploy'dan ÖNCE koşulur. Panel ekrandan sessizce
// düşerse (mount silinir / storageKey değişir / api metodu adlandırması
// kayar) operatör "boot MV'yi düşürecek" uyarısını hiç görmez ve 90 günlük
// messaging kovaları bir sonraki deploy'da gider — bu yüzden sadece saf
// çekirdek değil, EKRANDA OLDUĞU pinli (feedback-tested-but-unreachable).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const SRC = resolve(__dirname, '..');
const read = (p: string) => readFileSync(resolve(SRC, p), 'utf8');
// Yorum satırlarını at: gate kendi açıklama metnini ısırmasın
// (feedback-gate-matches-its-own-text).
const code = (p: string) => read(p).replace(/^\s*\/\/.*$/gm, '').replace(/\{\/\*[\s\S]*?\*\/\}/g, '');

describe('messaging_summary_5m operation boyutu sihirbazı (v0.10.564)', () => {
  it('panel AttrIndexWizardPanel\'den SONRA mount edilir', () => {
    const src = code('pages/AdminClickhouse.tsx');
    const attr = src.indexOf('<AttrIndexWizardPanel />');
    const msg = src.indexOf('<MessagingOpDimWizardPanel />');
    expect(attr).toBeGreaterThan(-1);
    expect(msg).toBeGreaterThan(-1);
    expect(msg).toBeGreaterThan(attr);
  });

  it('durum tablosu kendi storageKey\'ini kullanır (0013/0014 ile çakışmaz)', () => {
    const src = code('pages/AdminClickhouse.tsx');
    expect(src).toMatch(/storageKey: 'ch-msg-opdim-status'/);
    // Anahtar başka bir panelde tekrar edilmiyor: iki tablo aynı kolon
    // genişliklerini paylaşırsa operatörün ayarı öteki panelde kayar.
    expect(src.match(/'ch-msg-opdim-status'/g)?.length).toBe(1);
  });

  it('panel üç uç noktayı da çağırır; rollback/materialize YOK', () => {
    const src = code('pages/AdminClickhouse.tsx');
    expect(src).toMatch(/api\.messagingOpDimStatus\(\)/);
    expect(src).toMatch(/api\.messagingOpDimPreflight\(\)/);
    expect(src).toMatch(/api\.messagingOpDimApply\(cluster\)/);
    expect(src).not.toMatch(/messagingOpDimRollback|messagingOpDimMaterialize/);
  });

  it('api.ts üç metodu doğru rotalarla taşır; apply POST + 330s', () => {
    const src = code('lib/api.ts');
    expect(src).toMatch(/messagingOpDimStatus: \(\) =>\s*\n?\s*get<[^>]*MessagingOpDimStatusResult>\('\/api\/admin\/messaging-opdim\/status'\)/);
    expect(src).toMatch(/messagingOpDimPreflight: \(\) =>\s*\n?\s*get<[^>]*MessagingOpDimPreflightResult>\('\/api\/admin\/messaging-opdim\/preflight'\)/);
    const applyIdx = src.indexOf("messagingOpDimApply");
    expect(applyIdx).toBeGreaterThan(-1);
    const apply = src.slice(applyIdx, applyIdx + 500);
    expect(apply).toMatch(/'\/api\/admin\/messaging-opdim\/apply'/);
    expect(apply).toMatch(/method: 'POST'/);
    expect(apply).toMatch(/timeoutMs: 330_000/);
    expect(apply).toMatch(/JSON\.stringify\(\{ cluster \}\)/);
  });

  it('types.ts iki arayüzü sözleşmedeki alanlarla taşır', () => {
    const src = read('lib/types.ts');
    const st = src.slice(src.indexOf('export interface MessagingOpDimStatusResult'));
    const stBody = st.slice(0, st.indexOf('\n}'));
    for (const f of ['cluster', 'mv', 'inner', 'objects', 'innerColumn', 'keyHasOperation',
      'queryHasOperation', 'mvColumn', 'bootWouldDrop', 'divergent', 'state', 'detail', 'generated']) {
      expect(stBody).toMatch(new RegExp(`\\n\\s*${f}[?]?:`));
    }
    expect(stBody).toMatch(/state: 'done' \| 'partial' \| 'missing' \| 'unknown'/);

    const pf = src.slice(src.indexOf('export interface MessagingOpDimPreflightResult'));
    const pfBody = pf.slice(0, pf.indexOf('\n}'));
    for (const f of ['clusters', 'suggestedCluster', 'supported', 'detail', 'mv', 'inner',
      'divergent', 'alreadyDone', 'statements', 'probeErrors']) {
      expect(pfBody).toMatch(new RegExp(`\\n\\s*${f}[?]?:`));
    }
  });
});
