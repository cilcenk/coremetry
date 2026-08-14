// Kapı A + Kapı B — yıkıcı eylem sözleşmesi (v0.9.1010, M7 / C4).
//
// ORİJİNAL BELİRTİ (C4): sunucuya yazan, geri alınamayan, hiçbir onay
// taşımayan ÜÇ site vardı ve üçü de göz taramasıyla bulunmuştu —
// depoda onları yakalayacak hiçbir şey yoktu:
//   · Trace.tsx `api.revokeTraceShare` — linki elinde tutan herkes
//     ANINDA 404 görüyordu, token geri getirilemez
//   · TeamRoutingTab alias silme — `void save(next)` = anında persist,
//     tıkla-ve-gitti
//   · streams.tsx `api.deleteAnomalySilence` — atom bile değildi,
//     çıplak `<button className="btn-chip-x">`
//
// KAPI A: bir handler gövdesi `api.<yıkıcı>()` çağırıyorsa AYNI
// gövdede bir `confirm(` da bulunmalı. "Aynı gövde" şartı süslü
// parantez dengelemesiyle kesin çıkarılabiliyor; denetimde 27/27 site
// doğru sınıflandı.
//
// KAPI B: yıkıcı bir handler'a bağlanan `<Button>` kırmızı dili
// taşımalı (`danger` ya da `ghost-danger`). Girdisi Kapı A'nın
// çıktısı: yalnız gövdesinde yıkıcı bir `api.*` bulunan handler'lar
// denetleniyor — böylece YEREL TASLAK silmeleri (PanelEditor'ın
// "Delete panel"i, Dashboard'un panel/satır silmeleri) kapsam dışı
// kalıyor. Onlar sunucuya yazmıyor, `Save`e kadar geri alınabilir ve
// onay istemek gürültü olur.
//
// KAPININ İKİ ÖLÇÜLMÜŞ SINIRI — mutasyonla bulundu, tahminle değil:
//
//   1. PROP ÜZERİNDEN GEÇEN HANDLER kaynak seviyesinde takip edilemez.
//      streams.tsx'te bu somut olarak ısırdı: onay önce butonun
//      içindeydi, `onUnmute` prop olarak geçtiği için yıkıcı çağrı ile
//      onay ayrı gövdelerde kalıyordu. Çözüm kapıyı gevşetmek değil,
//      ONAYI EYLEMİN YANINA taşımaktı — kapı burada tasarımı düzeltti.
//      Kalan benzer siteler (Monitors → MonitorCard, Users satırı)
//      onaylarını kendi handler gövdelerinde taşıdıkları için geçiyor.
//
//   2. OMISSION İLE SİLME görünmez. `TeamRoutingTab`ın alias silmesi
//      `api.delete*` çağırmıyor: alias'ı nesneden çıkarıp TÜM nesneyi
//      PUT ediyor (`void save(next)`). Kaynak seviyesinde bu bir
//      "kaydet"ten ayırt edilemez. O site v0.9.1010'da elle onaya
//      bağlandı ama KAPI ONU KORUMUYOR; onay kaldırılırsa bu test
//      yeşil kalır. Aynı şekil (whole-object PUT ile silme) doğduğu
//      her yerde el yordamı gerekiyor.
//
// "Tam kilit" diye satılan bir kapı, kapatmadığı deliği gizler.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { stripTsComments } from '../../styles/zLayers.test';

const SRC = resolve(__dirname, '..', '..');

// Sunucuya yazan ve geri alınamayan çağrılar. `disable*` BİLEREK yok:
// yumuşak kapatma geri alınabilir (Alerts listesinden tek tıkla).
//
// İKİ YAZILIŞ DA SAYILIYOR ve bu ölçülerek öğrenildi: kapının ilk
// taslağı yalnız `api.delete*` arıyordu, oysa yıkıcı sitelerin çoğu
// çağrıyı React Query hook'u üzerinden yapıyor
// (`const del = useDeleteSLO(); … del.mutateAsync(id)`). Mutasyon turu
// bunu ortaya çıkardı: Slos'un Delete'ini `secondary`ye çevirdim ve
// kapı YEŞİL kaldı — çünkü o dosya taramanın görüş alanında hiç yoktu.
// Tek yazılışı aramak, ikinci yazılışa geçmiş her dosyayı sessizce
// ölçmemek demek.
const DESTRUCTIVE_API = /\bapi\.(delete|revoke|purge|bulkDelete)[A-Z]\w*\s*\(/;
const DESTRUCTIVE_HOOK = /\buse(Delete|Revoke|Purge|BulkDelete)[A-Z]\w*\s*\(/;

/** `const del = useDeleteSLO();` → ['del'] */
function destructiveMutationVars(src: string): string[] {
  return [...src.matchAll(/\bconst\s+(\w+)\s*=\s*use(?:Delete|Revoke|Purge|BulkDelete)[A-Z]\w*\s*\(/g)]
    .map(m => m[1]);
}

/** Bir gövde yıkıcı bir SUNUCU çağrısı tetikliyor mu? */
function bodyIsDestructive(body: string, vars: string[]): boolean {
  if (DESTRUCTIVE_API.test(body)) return true;
  return vars.some(v => new RegExp(`\\b${v}\\.mutateAsync?\\s*\\(`).test(body));
}

/** Dosya seviyesinde yıkıcı yol var mı? */
function fileIsDestructive(src: string): boolean {
  return DESTRUCTIVE_API.test(src) || DESTRUCTIVE_HOOK.test(src);
}

function walk(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    if (e.name === 'node_modules' || e.name === 'dist') continue;
    const p = join(dir, e.name);
    if (e.isDirectory()) walk(p, out);
    else if (/\.tsx?$/.test(e.name) && !/\.test\.tsx?$/.test(e.name)) out.push(p);
  }
  return out;
}

/**
 * Bir dosyadaki her fonksiyon/arrow gövdesini süslü parantez
 * dengeleyerek çıkarır. Kaba ama bu iddia için yeterli: aradığımız şey
 * "şu iki dize AYNI gövdede mi", iç içe gövdeler de ayrıca sayıldığı
 * için en dar eşleşme daima bulunuyor.
 */
function bodies(src: string): string[] {
  const out: string[] = [];
  const re = /(?:=>|\)\s*)\{/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(src))) {
    const start = src.indexOf('{', m.index);
    let depth = 0;
    for (let i = start; i < src.length; i++) {
      if (src[i] === '{') depth++;
      else if (src[i] === '}') {
        depth--;
        if (depth === 0) { out.push(src.slice(start, i + 1)); break; }
      }
    }
  }
  return out;
}

const FILES = walk(SRC).map(p => ({
  rel: p.slice(SRC.length + 1),
  src: stripTsComments(readFileSync(p, 'utf8')),
}));

// Yıkıcı çağrıyı SARAN en dar gövde. Bu, "handler" tanımının kaynak
// seviyesindeki en iyi yaklaşımı.
function narrowestBodies(src: string): string[] {
  const vars = destructiveMutationVars(src);
  return bodies(src)
    .filter(b => bodyIsDestructive(b, vars))
    // En dar olanı seç: aynı çağrıyı içeren birden fazla iç içe gövde
    // varsa, dıştakinde onay olması yeterli SAYILMAMALI — ama iç
    // gövdede olması yeterlidir. Bu yüzden uzunluğa göre sıralayıp
    // en kısa eşleşmeyi bırakıyoruz.
    .sort((a, b) => a.length - b.length);
}

// GEREKÇEYE göre anahtarlanmış muafiyetler — satıra göre DEĞİL (bir
// import satırı eklenince kayan girdiler bu depoda daha önce testi
// alakasız bir yerde kırmızıya çevirdi). Her girdi bir BULGU kimliği
// taşıyor ve bulgu kapanınca çıkarılmalı.
const CONFIRM_EXEMPT: Record<string, string> = {
  // Denetimin kendi kararı (§5, "bilinçli kapsam dışı"): yıldız
  // kaldırma `deleteSavedView` çağırıyor ama GERİ ALINABİLİR — pano
  // duruyor, yalnız yıldız sönüyor ve tek tıkla geri konur. Onay
  // istemek gürültü olurdu ve `confirm`ün tehlike sinyalini sulandırır.
  'pages/Dashboards.tsx': 'yıldız kaldırma geri alınabilir',
  // Deponun EN GÜÇLÜ onayı burada ve `confirm(` değil: arming →
  // kapsam listesi (Deleted/Kept) → type-to-confirm → uçuşta Cancel de
  // kilitli. Atoma indirmek koruma seviyesini DÜŞÜRÜRDÜ.
  'pages/settings/DangerZoneTab.tsx': 'type-to-confirm, confirm()ten güçlü',
};

describe('Kapı A — yıkıcı sunucu çağrısı ONAY taşıyor', () => {
  const hits = FILES.flatMap(f => {
    const bs = narrowestBodies(f.src);
    if (bs.length === 0) return [];
    // api.* sarmalayıcı katmanı (lib/api.ts) ve React Query hook
    // katmanı (lib/queries/*) çağrıyı TANIMLIYOR, TETİKLEMİYOR.
    // Onay bir UI kararı; veri katmanına onay koymak yanlış yer olurdu.
    if (f.rel === 'lib/api.ts' || f.rel.startsWith('lib/queries/')) return [];
    return [{ rel: f.rel, bodies: bs }];
  });

  it('tarama gerçekten yıkıcı çağrı buluyor', () => {
    expect(hits.length).toBeGreaterThanOrEqual(8);
  });

  it('her yıkıcı gövdede bir onay var', () => {
    const bad: string[] = [];
    for (const h of hits) {
      // En dar gövdelerden HERHANGİ BİRİ onay taşıyorsa site kapalı:
      // onay çoğu zaman çağrıdan bir seviye yukarıda (`if (!await
      // confirm(…)) return;` sonra `await mut.mutateAsync()`).
      if (CONFIRM_EXEMPT[h.rel]) continue;
      const ok = h.bodies.some(b => /(?<![\w.])confirm\s*\(/.test(b));
      if (!ok) bad.push(h.rel);
    }
    expect(bad, 'sunucuya yazan + geri alınamayan bir eylem onaysız').toEqual([]);
  });
});

describe('Kapı B — yıkıcı dosyalarda kırmızı dil', () => {
  // Girdi Kapı A'nın çıktısı: yalnız yıkıcı `api.*` çağıran dosyalar.
  // Böylece yerel-taslak silmeleri (PanelEditor, Dashboard panel/satır,
  // Runbook adım silme, ClustersTab) kapsam DIŞI kalıyor — sunucuya
  // yazmıyorlar ve `Save`e kadar geri alınabilirler.
  const destructiveFiles = FILES.filter(f =>
    fileIsDestructive(f.src)
    && f.rel !== 'lib/api.ts'
    && !f.rel.startsWith('lib/queries/'));

  it('tarama gerçekten dosya buluyor', () => {
    expect(destructiveFiles.length).toBeGreaterThanOrEqual(8);
  });

  it('yıkıcı dosyaların hepsinde en az bir kırmızı varyant var', () => {
    const bad = destructiveFiles
      .filter(f => !CONFIRM_EXEMPT[f.rel])
      .filter(f => !/variant=["'{]?\s*(["'])?(ghost-)?danger/.test(f.src))
      .map(f => f.rel);
    expect(bad, 'kalıcı silme yolu `secondary`/`ghost` ile Edit’le eş ağırlıkta duramaz').toEqual([]);
  });

  // Muafiyetlerin GEREKÇESİ hâlâ doğru mu — bayat bir girdi, testi
  // yanlış sebeple yeşil bırakır.
  it('DangerZone muafiyeti hâlâ type-to-confirm’e dayanıyor', () => {
    const dz = FILES.find(f => f.rel === 'pages/settings/DangerZoneTab.tsx')!;
    expect(dz.src).toMatch(/phrase\.trim\(\)\s*!==\s*CONFIRM/);
  });

  it('Dashboards muafiyeti hâlâ YALNIZ deleteSavedView’a dayanıyor', () => {
    const d = FILES.find(f => f.rel === 'pages/Dashboards.tsx')!;
    const calls = [...d.src.matchAll(/\b(?:api\.(?:delete|revoke|purge|bulkDelete)|use(?:Delete|Revoke|Purge|BulkDelete))[A-Z]\w*/g)]
      .map(m => m[0]);
    expect([...new Set(calls)], 'başka bir yıkıcı çağrı eklendiyse muafiyet artık geçerli değil')
      .toEqual(['api.deleteSavedView']);
  });

  it('çıplak <button className="btn-chip-x"> yıkıcı yolda kalmadı', () => {
    // streams.tsx'in atom baypası (C4-3): kendi CSS'iyle boyanmış ham
    // element hem varyant sözleşmesinin hem loading sözleşmesinin
    // dışındaydı.
    const bad = destructiveFiles
      .filter(f => /<button[^>]*className=["'][^"']*btn-chip-x/.test(f.src))
      .map(f => f.rel);
    expect(bad).toEqual([]);
  });
});
