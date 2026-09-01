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
    expect(body).toContain("<Link to={href} className={id === 'operation' ? 'row-link row-link--name' : 'row-link'}>");
    expect(body).not.toContain('onClick={() => openTrace');
    expect(body).not.toContain('onClick={(e) => { e.stopPropagation(); setExpanded');
  });

  it('href traceHref üzerinden (id + sayfa aralığı) — elle /trace?id= yok', () => {
    const src = traces();
    expect(src).toContain('const href = traceHref(t.traceId, { pageRange: range });');
    expect(src).not.toMatch(/`\/trace\?id=/);
  });

  it('K8s entity hücresi satır linkine sarılmıyor (<a> içinde <a> yok)', () => {
    const src = traces();
    expect(src).toContain('const ownLink = k8sOn && isTraceK8sCol(id);');
    expect(src).toContain('{ownLink ? cell : <Link to={href}');
    // Eski stopPropagation kaçağı: satır linki yokken gerekliydi, şimdi
    // varlığı "hücre hâlâ satır-tıkına sarılı" demek olurdu.
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

  it('row-link primitifi globals.css\'te tanımlı (hücre dolgusu + odak halkası)', () => {
    const c = css();
    expect(c).toContain('tbody td.row-cell { padding: 0; }');
    expect(c).toMatch(/\.row-link \{[^}]*display: block;[^}]*padding: 9px 12px;/);
    expect(c).toMatch(/\.row-link:focus-visible \{[^}]*outline/);
    expect(c).toMatch(/\.row-link--name:hover \{[^}]*text-decoration: underline/);
  });
});
