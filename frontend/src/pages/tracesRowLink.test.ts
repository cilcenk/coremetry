// tracesRowLink — v0.10.216 kapısı (operatör-bildirimli: "Traces'te trace'e
// orta tuşla basınca yeni sekmede açılsın; frame içinde olmasın").
//
// Ne çiviliyor: /traces satırının DOM'da GERÇEK bir link olduğu. Eski hâl
// `td onClick → navigate(...)` idi: sol tık çalışıyor, orta tık / ⌘-tık /
// sağ tık "yeni sekmede aç" ölü — tarayıcı bir <a> görmüyordu. tsc, eslint
// ve make audit bu farkı göremez (ikisi de geçerli React); tek kapı bu.
//
// İkinci yarı (memory: düzeltmenin ikinci yarısı): ▸ ön-izleme kolonu ve
// satır-altı MiniWaterfall kutusu da bu sürümde silindi — geri "açıvermek"
// (bir import, bir leading prop) burada kırılır.
//
// Üçüncü kapı: <a> içinde <a> geçersiz HTML — K8s entity hücreleri kendi
// linklerini taşır ve satır linkine SARILMAZ (ownLink dalı). Dal kaybolursa
// tarayıcı iç <a>'yı dışarı taşır, entity linki satır linkini keser.
import { describe, it, expect } from 'vitest';
import { existsSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const SRC = resolve(__dirname, '..');
const traces = () => readFileSync(resolve(SRC, 'pages/Traces.tsx'), 'utf8');
const css = () => readFileSync(resolve(SRC, 'styles/globals.css'), 'utf8');

describe('/traces satırı gerçek bir link', () => {
  it('hücreler <Link className="row-link"> ile sarılı, td onClick→navigate yok', () => {
    const src = traces();
    const i = src.indexOf('renderRow={(t) =>');
    expect(i, 'renderRow kayboldu — kapı BAYAT, yeniden bul').toBeGreaterThan(-1);
    const body = src.slice(i, i + 1600);
    expect(body).toContain("<Link to={href} state={{ from: loc.pathname + loc.search }} className={id === 'operation' ? 'row-link row-link--name' : 'row-link'}>");
    expect(body).not.toContain('onClick={() => openTrace');
    expect(body).not.toContain('onClick={(e) => { e.stopPropagation(); setExpanded');
  });

  it('/trace breadcrumb\'ı listeye state ile döner (v0.10.219)', () => {
    // Satır Link'i liste URL'sini `state.from` olarak taşır; Trace.tsx onu
    // traceBackHref ile okur. Biri kaybolursa breadcrumb çıplak /traces'e
    // düşer — sessiz filtre kaybı.
    const trace = readFileSync(resolve(SRC, 'pages/Trace.tsx'), 'utf8');
    expect(trace).toContain('<Link to={traceBackHref(location.state)}>Traces</Link>');
    // Kullanım biçimi aranıyor, sözcük değil (yorum tarihçe olarak anıyor).
    expect(trace).not.toContain('onClick={() => navigate(-1)}');
  });

  it('href traceHref üzerinden (id + sayfa aralığı) — elle /trace?id= yok', () => {
    const src = traces();
    expect(src).toContain('const href = traceHref(t.traceId, { pageRange: range });');
    expect(src).not.toMatch(/`\/trace\?id=/);
  });

  it('K8s hücresi düz metin → satır linkine sarılır; hücre içinde ikinci <a> yok (v0.10.330)', () => {
    const src = traces();
    // v0.10.330 (operatör): entity çipi kalktı, hücre düz metin — artık
    // <a> içinde <a> tehlikesi yok, hücre diğerleri gibi satır linkine sarılır.
    expect(src).toContain('const ownLink = false;');
    expect(src).toContain('{ownLink ? cell : <Link to={href}');
    expect(src).not.toContain('className="mono sec"');
    expect(src).not.toContain('onClick={e => e.stopPropagation()}>{v}</Link>');
  });

  it('▸ ön-izleme kolonu + MiniWaterfall kutusu geri gelmedi', () => {
    const src = traces();
    // Sözcük değil KULLANIM aranıyor (memory: kapı kendi metnini ısırır —
    // v0.9.645 ve v0.10.216 yorumları bileşenin adını tarihçe olarak anıyor).
    expect(src).not.toContain("from '@/components/traces/MiniWaterfall'");
    expect(src).not.toContain('<MiniWaterfall');
    expect(src).not.toContain('leading={[30]}');
    expect(src).not.toContain('setExpanded(');
    expect(existsSync(resolve(SRC, 'components/traces/MiniWaterfall.tsx'))).toBe(false);
  });

  // v0.10.225 (operatör-bildirimli: "Traces listesi çerçeve içinde, iç scroll
  // çıkıyor"). Kök neden: `[data-density=…] tbody td` (0,1,2) `tbody
  // td.row-cell { padding: 0 }` ile aynı özgüllükte ve SONRA geliyordu →
  // yoğunluk açıkken çift dolgu → satır > 36 px → VirtualTable (yükseklik
  // n × 36) iç kaydırma çubuğu. Üç kapı: (a) row-cell sıfır dolgusu her
  // yoğunlukta kazanır; (b) her yoğunluğun td dolgusunun .row-link ikizi var
  // ve AYNI değer; (c) sanal tabloda .row-link 36 px'e çakılı ve Traces.tsx'in
  // rowHeight'ı da 36 — ikisi birlikte değişmeli.
  it('yoğunluk ayarı row-cell sıfır dolgusunu ezemez; her yoğunluğun row-link ikizi var', () => {
    const c = css();
    expect(c).toContain('tbody td.row-cell, [data-density] tbody td.row-cell { padding: 0; }');
    const tdRules = [...c.matchAll(/\[data-density="([a-z]+)"\] tbody td \{ padding: ([^;]+); \}/g)];
    expect(tdRules.length, 'yoğunluk td kuralları kayboldu — kapı BAYAT').toBeGreaterThanOrEqual(3);
    for (const [, density, pad] of tdRules) {
      const twin = new RegExp(`\\[data-density="${density}"\\] \\.row-link \\{ padding: ${pad.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}; \\}`);
      expect(c, `${density}: .row-link dolgusu td ile aynı değil`).toMatch(twin);
    }
  });

  it('sanal tabloda row-link 36 px\'e çakılı ve Traces.tsx rowHeight={36}', () => {
    const c = css();
    expect(c).toMatch(/\.vt-scroll tbody td\.row-cell > \.row-link \{[^}]*height: 36px;[^}]*line-height: 18px;/);
    expect(traces()).toContain('rowHeight={36}');
    expect(traces()).toContain('height={44 + displayRows.length * 36}');
  });

  it('row-link primitifi globals.css\'te tanımlı (hücre dolgusu + odak halkası)', () => {
    const c = css();
    expect(c).toContain('tbody td.row-cell { padding: 0; }');
    expect(c).toMatch(/\.row-link \{[^}]*display: block;[^}]*padding: 9px 12px;/);
    expect(c).toMatch(/\.row-link:focus-visible \{[^}]*outline/);
    expect(c).toMatch(/\.row-link--name:hover \{[^}]*text-decoration: underline/);
  });
});
