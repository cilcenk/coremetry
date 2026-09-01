// problemsRowLink — v0.10.221 kapısı (operatör-bildirimli: "Exceptions /
// Problems sayfasında da tıklayınca yeni sayfada açılabilsin, orta tuşla").
//
// Ne çiviliyor: /problems'ta problem satırları ve exception-grup satırları
// GERÇEK href taşıyor (?problem= / ?exc= sözleşmesi, problemLink.ts).
// Eski hâl `tr onClick → setSearchParams(replace)` idi: sol tık çalışıyor,
// orta tık / ⌘-tık ölü — tarayıcı <a> görmüyordu. Traces'teki v0.10.216 ile
// aynı sınıf; kapı da aynı şekil (kaynak taraması, kullanım biçimi).
//
// Satırın kendi onClick'i BİLEREK kalıyor: etkileşimli hücreler (checkbox,
// servis linki, runbook, atama, Triage, caret, eylemler) satır linkine
// sarılamaz (<a> içinde <a>/<button> geçersiz), onların boşluğuna tık
// satır onClick'iyle açar. Link'ler `replace` + stopPropagation taşır ki
// aynı tık iki kez gezinmesin ve geçmiş sözleşmesi (replace) korunsun.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const SRC = resolve(__dirname, '..');
const read = (p: string) => readFileSync(resolve(SRC, p), 'utf8');

describe('/problems satırları gerçek link', () => {
  it('problem satırı: düz hücreler problemDetailHref taşıyan <Link replace>', () => {
    const src = read('features/anomalies/ProblemsSection.tsx');
    expect(src).toContain('const href = problemDetailHref(location.pathname, searchParams, p.id);');
    const links = src.match(/<Link to=\{href\} replace className="row-link[^"]*" onClick=\{e => e\.stopPropagation\(\)\}>/g) ?? [];
    // Priority · Severity · Metric · Value · kural adı (inline) · Started · Status
    expect(links.length, 'düz hücre sayısı değişti — sözleşmeyi güncelle').toBe(7);
    // Satır onClick'i (etkileşimli hücrelerin boşluğu + klavye) yerinde.
    expect(src).toContain('onClick={() => openDetail(p.id)}');
  });

  it('exception satırı: düz hücreler excDetailHref taşıyan <Link replace>', () => {
    const src = read('features/anomalies/AnomaliesPage.tsx');
    expect(src).toContain('const excHref = excDetailHref(location.pathname, searchParams, g.fingerprint);');
    const links = src.match(/<Link to=\{excHref\} replace className="row-link" onClick=\{e => e\.stopPropagation\(\)\}>/g) ?? [];
    // State · type+message · occurrences · first seen · last seen
    expect(links.length, 'düz hücre sayısı değişti — sözleşmeyi güncelle').toBe(5);
    expect(src).toContain('<tr onClick={() => openExcDetail(g)}');
  });

  it('kendi linkini taşıyan hücreler satır linkine SARILMAZ (<a> içinde <a> yok)', () => {
    const ps = read('features/anomalies/ProblemsSection.tsx');
    // Servis linki hâlâ kendi başına (row-link içinde değil).
    expect(ps).toMatch(/<td>\s*\{\/\*[\s\S]*?\*\/\}\s*<SubjectLink service=\{p\.service\}/);
    const ap = read('features/anomalies/AnomaliesPage.tsx');
    expect(ap).toMatch(/<td>\s*\{\/\* v0\.9\.966[\s\S]*?<Link to=\{serviceHref\(g\.service/);
  });

  it('row-link inline varyantı CSS\'te (kural adı metni)', () => {
    const css = read('styles/globals.css');
    expect(css).toMatch(/\.row-link--inline \{[^}]*display: inline;/);
  });
});
