// ExplainBody.test.tsx — v0.10.165: kart anatomisi çalışma zamanında —
// Karar satırı, Kanıt satırı, oluklu kod bloğu (satır numarası sütunu, hata
// satırı vurgusu, dosya başlığı, mapper etiketi) gövdeden ÖNCE; stack çiti
// yerinde ve katlı; akış sürerken anatomi yok.
import { describe, it, expect } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import { ExplainBody } from './ExplainBody';

const TEXT = [
  '**Hata ve Anlamı**',
  '- PSQLException: kolon yok.',
  '',
  '**Kök Neden ve Sonraki Adım**',
  '- **LedgerEntryDao.insertBatch** 246. satırda eski kolona yazıyor. Kontrol: mapper.',
  '  ```java',
  '  // src/main/java/com/payments/ledger/dao/LedgerEntryDao.java:244-247',
  '  244| try {',
  '  >>> 246|   m.insert(e);',
  '  247| }',
  '  ```',
  '  ```xml',
  '  // src/main/resources/mappers/LedgerEntryMapper.xml:31-32',
  '  31| <insert id="insert">',
  '  32|   INSERT INTO ledger_entry (settlement_batch_id)',
  '  ```',
  '',
  '**Stacktrace Detayı**',
  '```',
  'org.postgresql.util.PSQLException: ERROR: column',
  ...Array.from({ length: 40 }, (_, i) => `\tat org.springframework.jdbc.core.JdbcTemplate.execute(JdbcTemplate.java:${300 + i})`),
  '```',
].join('\n');

describe('ExplainBody (v0.10.165)', () => {
  const html = renderToStaticMarkup(<ExplainBody text={TEXT} busy={false} evidence={{ spans: 3, traces: 2 }} />);
  it('Karar + Kanıt satırları en üstte; Karar cümlesi gövdede TEKRARLANMAZ, madde kalanı durur', () => {
    expect(html).toContain('cx-verdict');
    expect(html).toContain('LedgerEntryDao.insertBatch 246. satırda eski kolona yazıyor.');
    expect(html.split('246. satırda eski kolona yazıyor.').length - 1).toBe(1);
    expect(html).toContain('Kontrol: mapper.');
    expect(html).toContain('↑ kod alıntısı yukarıda');
    expect(html).toContain('Kanıt: 3 span · 2 trace');
    expect(html.indexOf('cx-verdict')).toBeLessThan(html.indexOf('cx-evidence'));
  });
  it('oluklu kod bloğu: numara sütunu, hata satırı vurgusu, dosya başlığı, mapper etiketi; önek gövdede yok; gövdeden ÖNCE', () => {
    expect(html).toContain('cm-md-code-no');
    expect(html).toContain('>246<');
    expect(html).toContain('cm-md-code-line hl');
    expect(html).toContain('LedgerEntryDao.java:244-247');
    expect(html).toContain('kaynak penceresi (mapper)');
    expect(html).not.toContain('246|');
    expect(html.indexOf('cm-md-code-no')).toBeLessThan(html.indexOf('Hata ve Anlamı'));
  });
  it('stack çiti yerinde (Stacktrace Detayı) ve katlı: N kare daha', () => {
    const at = html.indexOf('Stacktrace Detayı');
    expect(at).toBeGreaterThan(0);
    expect(html.slice(at)).toMatch(/\d+ kare daha/);
    expect(html.slice(at)).toContain('JdbcTemplate.java:300');
    expect(html.slice(at)).not.toContain('JdbcTemplate.java:339');
  });
  it('akış sürerken anatomi uygulanmaz (ham metin akar)', () => {
    const h = renderToStaticMarkup(<ExplainBody text={TEXT} busy evidence={{ spans: 3, traces: 2 }} />);
    expect(h).not.toContain('cx-verdict');
    expect(h).not.toContain('cx-evidence');
  });
  it('verdict={false} → Karar çizilmez, cümle gövdede kalır (kod kartı açıkken ilk kart)', () => {
    const h = renderToStaticMarkup(<ExplainBody text={TEXT} busy={false} verdict={false} />);
    expect(h).not.toContain('cx-verdict');
    expect(h).toContain('246. satırda eski kolona yazıyor.');
  });
  it('kanıt yoksa satır yok; Karar bölümü tek cümleyse Karar yok', () => {
    const h = renderToStaticMarkup(<ExplainBody text={'**Olası neden:** tek cümle.'} busy={false} evidence={{ spans: 0, traces: 0 }} />);
    expect(h).not.toContain('cx-evidence');
    expect(h).not.toContain('cx-verdict');
  });
});
