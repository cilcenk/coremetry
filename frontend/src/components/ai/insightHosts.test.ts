// insightHosts — v0.9.1133 (AI Faz 2.3) YUVA BAĞLANMA kapısı.
//
// insightRow.test.tsx davranışı ölçüyor ama kendi harness'ıyla: yuva
// birimleri doğru çalışsa da GERÇEK host onları yanlış bağlayabilir ve
// hiçbir şey kızarmaz. Bu deponun en pahalı dersi tam bu — "saf test ≠
// BAĞLANMA" (v0.9.1012) ve "kapı kapsamı göçte erir": bir atom ikinci bir
// yazılış doğurduğunda eski kapı yeni dosyaları ölçmeyi bırakır, sayı
// yeşil kaldığı için kimse fark etmez.
//
// Somut, tip-doğru ve SESSİZ üç kırılma bu kapının varlık sebebi:
//   1. kartın koşullu çizimi düşer (`<InsightRowSlot …/>` her satırda) →
//      her satır bir ES projeksiyonu + bir LLM çağrısı; ekran neredeyse
//      aynı görünür, fatura görünmez;
//   2. çipin `toggle`ı yanlış kimliği taşır (fingerprint yerine servis
//      adı gibi) → kart açılır ama BAŞKA bir özneyi anlatır;
//   3. problem satırında şerit `suppressed` almaz → aynı satırda iki
//      kanıt yüzeyi üst üste açılır (v0.9.306 sınıfı) ve mockup'ın
//      "tek şerit" kararı sessizce geri alınır;
//   4. (v0.9.1137) host kancaya KENDİ TÜRÜNÜ vermez → tsc yakalar, ama
//      YANLIŞ türü verirse yakalamaz: `useInsightRow('problem')` yazan
//      bir desen host'u adrese "problem:Disk full" yazar, sunucu 404
//      döner ve operatör "kart bozuk" görür. Tür host başına pinli.
//
// KAPSAM (v0.9.1137, Faz 2.4): dört yuva. Üçüncüsü TABLO DEĞİL (kart
// ızgarası → InsightGridSlot), dördüncüsünde satır-içi ✨ Explain
// SÖKÜLDÜ — o sökümün geri gelmemesi de burada pinli.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { resolve, join, relative } from 'node:path';

const SRC = resolve(__dirname, '../..');

// Yorumları BOŞALT (satır numaraları korunur). ŞART: bu dosyaların
// şerhleri kendi sözleşmelerini düz metin olarak ANLATIYOR ("kart YALNIZ
// açık satırda mount olur"), ham tarama onları kod sanardı — ve tersi
// daha kötü: bir gün kod silinip yorum kalsa kapı YEŞİL kalırdı.
const read = (rel: string) => readFileSync(resolve(SRC, rel), 'utf8')
  .replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, ' '))
  .split('\n').map(l => l.replace(/\/\/.*$/, '')).join('\n');

/** Bir JSX öğesinin kendi penceresi — prop'lar KOMŞU öğeden okunmasın. */
function element(src: string, tag: string): string {
  const i = src.indexOf(`<${tag}`);
  expect(i, `${tag} öğesi bulunamadı — kapı BAYAT, hedefi yeniden bul`).toBeGreaterThan(-1);
  const j = src.indexOf('/>', i);
  expect(j, `${tag} kendini kapatmıyor — kapı şekli değişmiş`).toBeGreaterThan(-1);
  return src.slice(i, j + 2);
}

describe('exception satırı — yuva KALDIRILDI (AnomaliesPage)', () => {
  const src = read('features/anomalies/AnomaliesPage.tsx');

  // v0.9.1149 — operatör kararı (2026-08-17): exception satırındaki
  // "▸ Ne oldu?" çipi kötü görünüyordu ve detaya girince Explain zaten
  // var. Bu kapı eskisinin TERSİ: çip/yuva sessizce GERİ GELMESİN.
  // Problems (ProblemsSection) ve desen (streams) yuvaları bilinçli
  // olarak DURUYOR — aşağıdaki describe'lar onları pinlemeye devam eder.
  it('insight çipi/yuvası bu listede YOK', () => {
    expect(src).not.toContain('<InsightRowChip');
    expect(src).not.toContain('<InsightRowSlot');
    expect(src).not.toMatch(/useInsightRow\('exception'\)/);
  });

  it('worker-yazımı pasif özet satırda ÇİZİLİYOR (sıfır fetch)', () => {
    // ListExceptionGroups ai_summary'yi zaten seçiyor; v0.9.1133'e dek
    // liste onu hiç göstermiyordu. Basım yolu (stripMarkdown) ayrıca
    // markdownSurfaces kapısında.
    expect(src).toMatch(/g\.aiSummary && \(/);
    expect(src).toContain('stripMarkdown(g.aiSummary)');
  });
});

describe('problem satırı — şerit BİRLEŞİK (ProblemsSection)', () => {
  const src = read('features/anomalies/ProblemsSection.tsx');

  it('yuva bağlı ve çip problem kimliğini taşıyor', () => {
    expect(src).toContain("from '@/components/ai/insightRow'");
    expect(src).toMatch(/useInsightRow\('problem'\)/);
    expect(element(src, 'InsightRowChip')).toContain('insight.toggle(p.id)');
    const el = element(src, 'InsightRowSlot');
    expect(el).toContain('kind="problem"');
    expect(el).toContain('id={p.id}');
    // 1 (seçim) + 8 (PROBLEM_COLS) + 2 (Assignee/Triage).
    expect(el).toContain('colSpan={11}');
  });

  it('kart KOŞULLU çizilir', () => {
    const i = src.indexOf('<InsightRowSlot');
    expect(src.slice(Math.max(0, i - 200), i)).toMatch(/insight\.openId === p\.id &&/);
  });

  it('şerit kartı GÖRÜYOR: suppressed + onExpandRequest bağlı', () => {
    const el = element(src, 'RootCauseRibbon');
    expect(el, 'ribbon suppressed almıyor — iki kanıt yüzeyi üst üste açılır')
      .toContain('suppressed={insight.openId === p.id}');
    expect(el, 'onExpandRequest yok — şerit çipi ölü tık olur')
      .toContain('onExpandRequest={insight.close}');
  });

  it('çip şeridin KENDİ satırına giriyor (trailing), kardeş öğe DEĞİL', () => {
    // Kardeş yazımda şerit genişlediğinde flex satırı sığmaz ve çip
    // açılan panelin ALTINA kayar — operatörün az önce tıkladığı düğme
    // yer değiştirir. `trailing` gövdeyi her zaman satırın altında
    // tutuyor. Kap `<div>` de aralarına girmemeli.
    const el = element(src, 'RootCauseRibbon');
    expect(el).toContain('trailing={');
    expect(el).toContain('<InsightRowChip');
    expect(el).not.toContain('<div');
  });

  it('satır map\'i key TAŞIYAN Fragment döndürüyor (MT4)', () => {
    // Satır artık iki <tr> döndürebiliyor; keyless bir <> reconcile'ı
    // yanlış eşleştirir ve sıralama değişince açık kart BAŞKA satırın
    // altında kalır.
    expect(src).toMatch(/import \{ Fragment,/);
    expect(src).toContain('<Fragment key={p.id}>');
  });
});

describe('şerit motoru — bastırma gerçekten gövdeyi kesiyor', () => {
  const src = read('components/RootCauseRibbon.tsx');

  it('gövde `showBody` ile çizilir ve showBody suppressed\'ı okur', () => {
    expect(src).toMatch(/const showBody = open && !suppressed;/);
    expect(src).toContain('{showBody && (');
    // Caret dönüşü de görünen hâli izlemeli; `open` kalırsa kapalı bir
    // gövdenin üstünde açık ok durur.
    expect(src).toContain('transform: showBody ?');
  });

  it('gövde istendiğinde host\'a haber verilir', () => {
    const i = src.indexOf('const onToggle');
    const body = src.slice(i, i + 700);
    expect(body).toMatch(/const next = !showBody;/);
    expect(body).toContain('onExpandRequest?.()');
  });
});

describe('log deseni — ızgara yuvası (streams.tsx)', () => {
  const src = read('features/anomalies/streams.tsx');

  it('kanca DESEN türüyle çağrılmış ve ızgara yuvası kullanılıyor', () => {
    expect(src).toContain("from '@/components/ai/insightRow'");
    expect(src).toMatch(/useInsightRow\('log-pattern'\)/);
    expect(src).toContain('<InsightRowChip');
    // `<tr>`li yuva burada YANLIŞ olurdu: bu bölüm kart ızgarası, satır
    // bağlamı yok (geçersiz HTML, yalnız çalışma zamanında uyarı).
    expect(src).toContain('<InsightGridSlot');
    expect(src, 'ızgara host\'unda tablo yuvası kullanılmış')
      .not.toContain('<InsightRowSlot');
  });

  it('çip + yuva desenin ADINI taşıyor (pencereyle değişen servis DEĞİL)', () => {
    expect(element(src, 'InsightRowChip')).toContain('insight.toggle(a.pattern)');
    expect(element(src, 'InsightRowChip')).toContain('insight.openId === a.pattern');
    const el = element(src, 'InsightGridSlot');
    expect(el).toContain('kind="log-pattern"');
    expect(el).toContain('id={a.pattern}');
    expect(el).toContain('onClose={insight.close}');
    // a.service kimlik DEĞİL: pencerede en çok basan servis, pencereyle
    // değişir — kimliğe girse aynı desen iki pencerede iki kart olurdu.
    expect(el, 'yuva kimliğe servisi karıştırmış').not.toContain('a.service');
  });

  it('kart KOŞULLU çizilir — kapalı desen sıfır istek', () => {
    const i = src.indexOf('<InsightGridSlot');
    expect(src.slice(Math.max(0, i - 200), i)).toMatch(/insight\.openId === a\.pattern &&/);
  });

  it('satır map\'i DESEN ADIYLA keyli Fragment döndürüyor (MT4)', () => {
    // Liste her pollde orana göre YENİDEN sıralanıyor; dizin key'i ile
    // açık kart başka bir desenin altında kalırdı.
    expect(src).toMatch(/import \{ Fragment,/);
    expect(src).toContain('<Fragment key={a.pattern}>');
    // Pencere DAR: aynı dosyadaki kardeş bölümler (TraceOps/Metric) hâlâ
    // dizin key'i taşıyor ve onlarda yuva YOK, yani bu iddia yalnız
    // desen bölümünü ölçmeli — dosya-kapsamlı bir `not.toContain`
    // ALAKASIZ bir bölüm yüzünden kırmızı yanardı.
    const i = src.indexOf('function LogPatternsSection');
    expect(i, 'LogPatternsSection bulunamadı — kapı BAYAT').toBeGreaterThan(-1);
    const j = src.indexOf('\nfunction ', i + 10);
    const block = src.slice(i, j > 0 ? j : undefined);
    expect(block, 'desen kartı hâlâ dizin key\'i taşıyor').not.toContain('key={i}');
  });

  it('/logs panel-düzeyi anlatıcısı YERİNDE (yuva onun yerine geçmedi)', () => {
    // Tasarım dokümanı "log paneli" diyordu; /logs'ta desen LİSTESİ yok
    // (LogPatternStrip v0.8.35'te operatör kararıyla kaldırıldı), o yüzden
    // yuva desen satırlarının yaşadığı yere kuruldu. Panel özeti KALMALI:
    // silmek v0.9.1100'ü sessizce geri almak olurdu.
    const logs = read('pages/Logs.tsx');
    expect(logs, '/logs panel anlatıcısı kaldırılmış').toContain('explainLogPatterns');
    expect(logs).toContain('Desenleri anlat');
    // Ve /logs'a desen listesi GERİ GELMEDİ (ES maliyeti + v0.8.35 kararı).
    expect(logs, '/logs\'a desen listesi geri eklenmiş')
      .not.toContain('useLogPatternAnomalies');
  });
});

describe('yavaş sorgu — satır yuvası + satır-içi explain SÖKÜMÜ (SlowQueries)', () => {
  const src = read('pages/SlowQueries.tsx');

  it('kanca SORGU türüyle çağrılmış, yuva tablo satırı', () => {
    expect(src).toContain("from '@/components/ai/insightRow'");
    expect(src).toMatch(/useInsightRow\('slow-query'\)/);
    expect(src).toContain('<InsightRowChip');
    expect(src).toContain('<InsightRowSlot');
  });

  it('kimlik `?stmt=` kodeğinden türüyor — üçüncü bir yazılış yok', () => {
    // Satırın (service, statement) `key`i kart kimliği OLAMAZ: pencereler
    // arası kalıcı değil ve sunucu onu ayrıştıramaz (400).
    expect(src).toMatch(/const insightId = r\.stmtHash/);
    expect(src).toContain('encodeStmtParam({ hash: r.stmtHash, system: dbSystem })');
    const el = element(src, 'InsightRowSlot');
    expect(el).toContain('kind="slow-query"');
    expect(el).toContain('id={insightId}');
    expect(el).toContain('onClose={insight.close}');
    // colSpan 11 = 1 (chevron) + 10 kolon; yanlış sayı kartı tablonun
    // dışına taşırır (tableLayout:fixed + colgroup).
    expect(el).toContain('colSpan={11}');
  });

  it('kart SAYFANIN penceresini taşıyor (1sa varsayılanına düşmüyor)', () => {
    const el = element(src, 'InsightRowSlot');
    expect(el, 'windowSec düşmüş — kart satırdan başka sayı gösterir')
      .toContain('windowSec={windowSec}');
    // Pencere memoize edilmiş türev: timeRangeToNs'in ham çağrısı
    // v0.5.184 sınıfı (her render'da yeni "şimdi" → sonsuz refetch).
    expect(src).toMatch(/const windowSec = useMemo\(/);
  });

  it('kimliksiz satırda çip HİÇ çizilmez (kart 400 almasın)', () => {
    const i = src.indexOf('<InsightRowChip');
    expect(src.slice(Math.max(0, i - 200), i)).toMatch(/insightId && \(/);
    const j = src.indexOf('<InsightRowSlot');
    expect(src.slice(Math.max(0, j - 200), j)).toMatch(/insightId && insight\.openId === insightId &&/);
  });

  it('satır-içi ✨ Explain paneli GERİ GELMEDİ', () => {
    // Kart onun ÜST KÜMESİ (aynı prompt + sinyaller + pivotlar + 👍/👎).
    // İkisi birlikte aynı satırda iki AI yüzeyi olurdu (v0.9.306), ve eski
    // panel cevabı HAM basıyordu (markdownSurfaces kapısının görmediği
    // dördüncü yazılış).
    expect(src, 'satır-içi explain çağrısı geri gelmiş')
      .not.toContain('copilotExplainSlowQuery');
    expect(src, 'satır-içi explain state makinesi geri gelmiş')
      .not.toMatch(/ExplainState|askCopilot|setExplains/);
    expect(src, 'kartın dışında ikinci bir 👍/👎 rayı')
      .not.toContain('<AIFeedbackButtons');
    expect(src, 'eski panelin ham metin kabı geri gelmiş')
      .not.toContain('✨ CoSRE');
  });

  it('istemci metodu da sökülmüş (yörüngesiz kod bırakılmadı)', () => {
    // Uç (POST /api/copilot/explain-slow-query) SUNUCUDA duruyor ve
    // prompt'u kart kullanıyor; frontend tüketicisi ise yok.
    const api = read('lib/api.ts');
    expect(api, 'api.copilotExplainSlowQuery hâlâ tanımlı')
      .not.toMatch(/copilotExplainSlowQuery:/);
  });
});

describe('kart TEK kapıdan mount olur', () => {
  it('yuva dışında hiçbir dosya <InsightCard/> çizmiyor', () => {
    // Yuvayı atlayan bir host URL/Esc/tek-kart sözleşmesinin HİÇBİRİNİ
    // almaz — ve tam da bu yüzden sessizce çalışır görünür.
    const walk = (dir: string, out: string[] = []): string[] => {
      for (const e of readdirSync(dir)) {
        const p = join(dir, e);
        if (statSync(p).isDirectory()) walk(p, out);
        else if (/\.tsx$/.test(p) && !/\.test\.tsx$/.test(p)) out.push(p);
      }
      return out;
    };
    const allowed = ['components/ai/insightRow.tsx', 'components/ai/InsightCard.tsx'];
    const offenders = walk(SRC)
      .map(p => relative(SRC, p).split('\\').join('/'))
      .filter(rel => !allowed.includes(rel))
      // Önek çakışmasına kapalı: `<InsightCardFoo` bu kapıyı ALAKASIZ bir
      // dosyada patlatmasın diye sınır aranıyor.
      .filter(rel => /<InsightCard[\s/>]/.test(read(rel)));
    expect(offenders, 'InsightCard yuva dışında mount ediliyor').toEqual([]);
  });
});
