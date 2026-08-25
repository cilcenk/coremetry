import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { logsHref } from './logsUrl';

// logsServiceScopeGate — v0.9.1381.
//
// Bir servise kapsamlanan /logs pivotu YAPISAL `service=` kullanır,
// serbest metin `q=` DEĞİL.
//
// ÖLÇÜM (lokal CH 24.8, 24s pencere, 41.315 satır):
//     service_name = 'x'                 →  858 satır
//     body LIKE '%x%'                    →  366 satır
//     body LIKE '%service.name:"x"%'     →    0 satır
// ve tüm tabloda `countIf(body LIKE '%service.name%') = 0`. Yani dizge
// hiçbir gövdede geçmiyor: eşleşme yapısal olarak imkânsız, pivot HTTP
// 200 + boş liste dönüyor ve operatör "log yok" okuyor. ClickHouse
// VARSAYILAN arka uç (config.go, main.go `case "", "clickhouse"`) ve
// varsayılan Helm kurulumunda pod'a `COREMETRY_LOGS_BACKEND` hiç
// yazılmıyor.
//
// ── NEDEN BU KAPI VAR: DOĞRU GEREKÇE, YANLIŞ GENELLEME ──────────────────
//
// Üç dosya `q=`yi "bilinçli" diye işaretleyip v0.8.521'e atıf yapıyordu.
// O atıf gerçekti ama YANLIŞ GENELLEŞTİRİLMİŞTİ:
//
//   • v0.8.521'in kurduğu şey: ID-ŞEKİLLİ bir `q` kolonla DA eşleşir, bu
//     yüzden trace pivotu `q` üzerinden hem alanı hem gövdesi olan
//     kurulumları bulur. Bu HÂLÂ doğru ve hâlâ taşıyıcı — CH tarafında
//     `isBareHexID` dalı (internal/logstore/clickhouse.go) çıplak
//     32/16-hex iğneyi trace_id/span_id kolonuna yükseltiyor.
//   • v0.8.521'in KURMADIĞI şey: aynı muamelenin SERVİS ADI için de
//     geçerli olduğu. Öyle bir dal yok.
//
// Yanlış genelleme üç dosyada kopyalanmıştı, yani gelecekteki her
// "temizlik" `q=`yi doğru sanıp bu düzeltmeyi geri alacaktı. Kapı, o geri
// almayı görünür yapmak için var — bu düzeltmenin kendisi kadar önemli.
const SRC = resolve(__dirname, '..');

/**
 * `q=` içine servis kapsamı yazan siteler.
 *
 * ⚠ ŞERHLER DÜŞÜRÜLÜYOR. İlk yazımı ham metne bakıyordu ve kendi
 * dokümantasyonunu ısırdı: bu düzeltmeyi ANLATAN yorumlar (burada ve
 * ProblemDetail'de) deseni birebir içeriyor. Kapının kendi gerekçesini
 * ihlal sayması, onu ilk gün muafiyet listesine mahkûm ederdi.
 */
function stripComments(src: string): string {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .split('\n').map(l => l.replace(/\/\/.*$/, '')).join('\n');
}

export function serviceScopeInQ(src: string): string[] {
  const out: string[] = [];
  const code = stripComments(src);
  for (const line of code.split('\n')) {
    // `q:` ya da `q=` argümanına serviceLogQuery(...) veriliyorsa ihlal.
    if (/\bq\s*[:=][^,)\n]*serviceLogQuery\s*\(/.test(line)) out.push(line.trim().slice(0, 90));
    // Elle yazılmış hâli de aynı kusur — üreticiyi atlayarak kaçamaz.
    if (/\bq\s*[:=][^,)\n]*service\.name\s*:/.test(line)) out.push(line.trim().slice(0, 90));
  }
  return out;
}

function walk(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir)) {
    const p = join(dir, e);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (/\.tsx?$/.test(p) && !/\.test\.tsx?$/.test(p)) out.push(p);
  }
  return out;
}

describe('serviceScopeInQ — yüklem (sentetik)', () => {
  const cases: Array<[string, string, boolean]> = [
    ['üreticiyle q= ihlal', `logsHref({ window: w, q: serviceLogQuery(s) })`, false],
    ['elle yazılmış q= ihlal', `logsHref({ window: w, q: 'service.name:"x"' })`, false],
    ['yapısal service= temiz', `logsHref({ window: w, service: s })`, true],
    ['operatörün yazdığı serbest metin temiz', `logsHref({ window: w, q: userTyped })`, true],
    // v0.8.521'in korunan hâli: id-şekilli pivot `q` üzerinden gider ve
    // bu DOĞRU. Kapı ona dokunmuyor — dokunsaydı düzelttiğim bug'ın
    // yerine eski bir bug'ı geri koyardı.
    ['trace-id pivotu q= üzerinden TEMİZ', `logsHref({ window: w, q: traceId })`, true],
  ];
  for (const [name, src, clean] of cases) {
    it(name, () => expect(serviceScopeInQ(src).length === 0).toBe(clean));
  }
});

describe('logs servis kapsamı kapısı (v0.9.1381)', () => {
  it('hiçbir yüzey servis kapsamını q= içine yazmıyor', () => {
    const offenders: string[] = [];
    for (const abs of walk(SRC)) {
      for (const hit of serviceScopeInQ(readFileSync(abs, 'utf8'))) {
        offenders.push(`${abs.slice(SRC.length + 1)}: ${hit}`);
      }
    }
    expect(offenders).toEqual([]);
  });

  it('logsHref service= paramını GERÇEKTEN yazıyor', () => {
    // Kapı "service= kullan" diyor; o anahtar bir gün düşerse kapı
    // yüzeyleri işe yaramaz bir parametreye yönlendirmiş olur ve yine
    // yeşil kalır. (v0.9.1377'nin dersi: yazılan ama okunmayan param.)
    const href = logsHref({ window: { preset: '1h' }, service: 'checkout' });
    expect(new URL(href, 'http://x').searchParams.get('service')).toBe('checkout');
  });

  it('serviceLogQuery ADI ağaçta HİÇ geçmiyor (v0.9.1386: silindi)', () => {
    // ⚠ v0.9.1386 — BU KAPI BİR KAÇAĞI GÖZDEN KAÇIRDI. Yüklem yalnız
    // DOĞRUDAN biçimi (`q: serviceLogQuery(...)`) görüyordu; streams.tsx
    // ise kapsamı `clauses.push(serviceLogQuery(a.service))` ile bir
    // diziye koyup sonra `q`ye join'liyordu. Aynı kusur, dolaylı montaj,
    // ve kapı sessizce yeşil kaldı.
    //
    // Ders: yazımın GİTTİĞİ yeri denetlemek yetmiyor, ÜRETİLDİĞİ yeri
    // denetlemek gerekiyor. Yardımcı silindiği için değişmez artık en
    // basit hâlinde: adı hiç geçmemeli.
    const offenders: string[] = [];
    for (const abs of walk(SRC)) {
      if (/\bserviceLogQuery\b/.test(stripComments(readFileSync(abs, 'utf8')))) {
        offenders.push(abs.slice(SRC.length + 1));
      }
    }
    expect(offenders).toEqual([]);
  });

  it('ağaç gerçekten tarandı — boş küme tuzağı', () => {
    const withLogsHref = walk(SRC).filter(p => /logsHref\s*\(/.test(readFileSync(p, 'utf8'))).length;
    expect(withLogsHref).toBeGreaterThan(5);
  });
});
