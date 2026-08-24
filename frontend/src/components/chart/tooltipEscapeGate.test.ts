import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { escapeHTML } from '../../lib/utils';

// tooltipEscapeGate — v0.9.1371. DEPOLANMIŞ XSS yüzeyi.
//
// uPlot tooltip'leri `innerHTML` ile kuruluyor ve seri etiketini şablona
// doğrudan gömüyorlardı:
//
//     `<b>${r.text}</b>`                      ← kaçış YOK
//     `<span title="${lbl}">`                 ← öznitelikten çıkış
//
// Pod adları için zararsız görünüyor, ama Explore'da group-by yapıldığında
// o kanala KEYFİ SPAN ÖZNİTELİK DEĞERLERİ giriyor — enstrümante edilen
// herhangi bir alan (HTTP header, kullanıcı adı, query param) crafted bir
// değer taşırsa, o grafiğe bakan operatörün tarayıcısında çalışır. Tehdit
// modeli teorik değil: `lib/utils.ts`'teki escapeHTML'in KENDİ şerhi tam
// bunu yazmış — "kötü niyetli bir ingester `service.name = \"<script>\"`
// gönderebilir ve render anında XSS tetikler". Yardımcı bu sınıf için
// yazılmıştı; çağrı yerleri onu kullanmıyordu.
//
// ÖLÇÜM (v0.9.1371) — ve bu kapının asıl varlık sebebi. İlk sayımım
// "dört tooltip kurucusundan üçü açık, `viz/TimeSeriesPanel` doğru
// yapıyor" idi, çünkü o dosya escapeHTML'i import ediyor ve İKİ yerde
// çağırıyordu. Kapı ilk çalıştırmasında onu da yakaladı: AYNI dosyanın
// AYNI tooltip'inde `r.text` ve `r.color` kaçışsızdı. Yani "import var"
// da "bir çağrı var" da güvenlik kanıtı değil — ikizler satır satır
// yaşıyor. Dosya sayarak değil, interpolasyon sayarak bakmak gerekti.
//
// KAPI: `innerHTML` şablonlarında VERİ TÜREVLİ hiçbir ifade kaçışsız
// interpolate edilemez. "Veri türevli" = bir nesne alanı (`r.text`,
// `near.kind`) ya da böyle bir alandan atanmış yerel değişken. Sayıdan
// üretilen biçimlendirici çıktıları (`hh`, `ts`) kanal değil, kapsam
// dışı — muafiyet listesiyle değil, YÜKLEMLE eleniyor; isim listesi
// tutmak kapının kendisini bakım yüküne çevirirdi.
const SRC = resolve(__dirname, '..', '..');

/**
 * unescapedInterpolations — saf yüklem, dosya metni girer, ihlaller çıkar.
 *
 * Saf tutuluyor ki SENTETİK girdiyle sınanabilsin. Kapıyı yalnız canlı
 * ağaç üzerinde koşturmak, ağaç yeşilken BOZUK bir kapıyı çalışan bir
 * kapıdan ayırt edilemez kılar — daha önce tam bu tuzağa düştüm.
 */
export function unescapedInterpolations(src: string): string[] {
  // Tek seviye yerel çözümleme: `const x = <ifade>`.
  const assigned = new Map<string, string>();
  for (const m of src.matchAll(/\bconst\s+([A-Za-z_$][\w$]*)\s*=\s*([^\n]*)/g)) {
    assigned.set(m[1], m[2]);
  }
  // Veri türevli = bir alan okuması (`r.text`). Ardından `(` gelen isim bir
  // METOT çağrısıdır (`d.getHours()`), alan okuması değil — çıktısı
  // biçimlendirici ürünü, kanal değil.
  // `(?![\w$])` ŞART: onsuz motor geri izleyip `d.getHours` yerine
  // `d.getHour` + `s` eşleştiriyor, metot çağrısı alan okuması sanılıyor.
  const FIELD_READ = /\b[A-Za-z_$][\w$]*\.[A-Za-z_$][\w$]*(?![\w$])(?!\s*\()/;
  // KOŞUL konumundaki alan okuması değer TAŞIMAZ, yalnız SEÇER:
  //   const c = near.kind === 'error' ? 'var(--err)' : 'var(--accent2)'
  // burada `near.kind` hangi LİTERAL'in seçileceğine karar veriyor, sonuç
  // dizgesine girmiyor. Karşılaştırma operandlarını eleyip kalanı
  // sınamak, bunu ihlal saymadan
  //   const txt = near.label ? `${near.kind} · ${near.label}` : near.kind
  // gibi gerçekten veri TAŞIYAN üçlüleri yakalıyor.
  //
  // İlk yazımım burayı ters kurmuştu — "düz zincir dışındaki her sağ taraf
  // erişim dışı, dolayısıyla güvenli". Mutasyon denetimi tam bunu yakaladı:
  // `txt`'in kaçışını geri aldığımda kapı YEŞİL kaldı. Erişim sınırını
  // ŞEKLE göre değil, DEĞER akışına göre çizmek gerekiyormuş.
  const stripConditions = (s: string) =>
    s.replace(/\b[A-Za-z_$][\w$]*\.[A-Za-z_$][\w$]*(?![\w$])\s*(===|!==|==|!=|>=|<=|>|<)/g, ' ');

  const safe = (expr: string, depth = 0): boolean => {
    if (expr.includes('escapeHTML(')) return true;
    if (FIELD_READ.test(stripConditions(expr))) return false;
    const bare = expr.trim().match(/^([A-Za-z_$][\w$]*)$/);
    if (bare && depth < 2) {
      const rhs = (assigned.get(bare[1]) ?? '').trim();
      if (rhs) return safe(rhs, depth + 1);
    }
    return true; // biçimlendirici çıktısı / sabit
  };

  const out: string[] = [];
  for (const line of src.split('\n')) {
    if (!line.includes('${')) continue;
    if (!/innerHTML|`\s*<[a-z]/.test(line)) continue;
    for (const m of line.matchAll(/\$\{([^}]*)\}/g)) {
      if (!safe(m[1])) out.push(`\${${m[1]}}`);
    }
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

describe('unescapedInterpolations — yüklem (sentetik)', () => {
  // Örnekler parçalardan kuruluyor: kapı kendi dokümantasyonunu ısırmasın
  // diye değil — bu dosya zaten `.test.ts` olduğu için yürüyüşün dışında —
  // ama okurken hangi ŞEKLİN sınandığı görünsün diye.
  const T = (body: string) => 'el.innerHTML = `<div>' + body + '</div>`';

  const cases: Array<[string, string, boolean]> = [
    ['çıplak alan erişimi ihlal', T('${r.text}'), false],
    ['kaçışlı alan erişimi temiz', T('${escapeHTML(r.text)}'), true],
    ['öznitelik içi alan erişimi ihlal', 'x.innerHTML = `<b title="${r.label}">`', false],
    ['iç içe alan erişimi ihlal', T('${near.traceId.slice(0, 8)}'), false],
    ['kaçışlı iç içe temiz', T('${escapeHTML(near.traceId.slice(0, 8))}'), true],
    ['sayısal biçimlendirici temiz', T('${fmtTooltipTime(tMs / 1000, step)}'), true],
    ['escapeHTML ile atanmış yerel temiz', 'const lbl = escapeHTML(r.label);\n' + T('${lbl}'), true],
    ['alandan atanmış yerel İHLAL', 'const lbl = r.label;\n' + T('${lbl}'), false],
    ['sayıdan atanmış yerel temiz', 'const hh = d.getHours().toString();\n' + T('${hh}'), true],
    // Erişim sınırını çizen iki ayırt edici vaka — mutasyon denetimi bu
    // ikisini ayıramayan bir yüklemi yakaladı, o yüzden ikisi de burada.
    ['koşul konumundaki alan (literal değer) temiz',
      "const c = near.kind === 'error' ? 'var(--err)' : 'var(--ok)';\n" + T('${c}'), true],
    ['üçlü İÇİNDEN veri taşıyan yerel İHLAL',
      'const txt = near.label ? `x ${near.kind}` : near.kind;\n' + T('${txt}'), false],
    ['innerHTML olmayan şablon kapsam dışı', 'const q = `service.name = ${r.label}`', true],
  ];
  for (const [name, src, wantClean] of cases) {
    it(name, () => {
      expect(unescapedInterpolations(src).length === 0).toBe(wantClean);
    });
  }
});

describe('tooltip innerHTML kaçış kapısı (v0.9.1371)', () => {
  it('ağaçta kaçışsız veri-türevli interpolasyon YOK', () => {
    const offenders: string[] = [];
    for (const abs of walk(SRC)) {
      const src = readFileSync(abs, 'utf8');
      if (!src.includes('innerHTML')) continue;
      for (const hit of unescapedInterpolations(src)) {
        offenders.push(`${abs.slice(SRC.length + 1)}: ${hit}`);
      }
    }
    expect(offenders).toEqual([]);
  });

  it('yürüyüş gerçekten innerHTML kuran dosya buluyor — boş küme tuzağı', () => {
    const n = walk(SRC).filter(p => readFileSync(p, 'utf8').includes('innerHTML')).length;
    expect(n).toBeGreaterThanOrEqual(4);
  });

  it('escapeHTML gerçekten etkisiz kılıyor — öznitelik dahil', () => {
    expect(escapeHTML('<img src=x onerror=alert(1)>')).not.toContain('<img');
    // Renk bir style="" içine giriyor: tırnak kaçmazsa öznitelikten çıkılır.
    expect(escapeHTML('red" onload="x')).not.toContain('"');
  });
});
