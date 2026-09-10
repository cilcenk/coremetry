// MessagingTopic.pin.test.ts — v0.10.575 (/messaging/topic).
//
// NEDEN KAYNAK TARAMASI: bu dilimin sözleşmelerinin hiçbirini saf bir
// fonksiyon tutmuyor. `topicHref.ts` yeşil olabilir ve sayfa yine de rotaya
// hiç bağlanmamış olabilir; `useMessagingClients` doğru anahtarı kurabilir ve
// sayfa yine de AÇILIŞTA istemci sorgusunu atabilir. Pinlenen şey çekirdeğin
// doğruluğu değil, ÇEKİRDEĞE GİDEN BAĞ ("test edilmiş ama ulaşılamaz",
// v0.9.1334 sınıfı):
//   1. rota App.tsx'te KAYITLI ve chunk lazy,
//   2. `?tab=` sözleşmesi (replace:true + prev'den türetme + varsayılan siler),
//   3. açılışta YALNIZ `set=chart`; `set=clients` yalnız sekme seçilince,
//   4. diğer sekmeler EK İSTEK yapmıyor (tek detay yükü),
//   5. CallerSection PAYLAŞILIYOR — çekmece de aynı dosyadan import ediyor,
//   6. çekmecede "Detay sayfası →" bağlantısı var ve pencereyi taşıyor.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// Yorumları at: pinlenen metinlerin çoğu bu dosyaların KENDİ açıklama
// cümlelerinde de geçiyor ("set=clients yalnız sekme seçilince…"), yorum
// eşleşmesi sahte yeşil verirdi — "gate kendi metnini ısırır" sınıfı.
//
// SIRA ÖNEMLİ ve bu yazılırken ISIRDI: önce blok yorumları silen bir tarayıcı,
// bir SATIR yorumunun içindeki `/*` dizisini (ör. bir paket yolu) blok açılışı
// sanıp bir sonraki `*/`e kadar olan GERÇEK KODU siliyor — ve testler "kod yok"
// diye kırmızıya dönüyor. Satır yorumları ÖNCE gidince o tuzak kapanıyor.
const strip = (s: string) => s.replace(/^\s*\/\/.*$/gm, '').replace(/\/\*[\s\S]*?\*\//g, '');

const page = strip(readFileSync(resolve(__dirname, 'MessagingTopic.tsx'), 'utf8'));
const app = strip(readFileSync(resolve(__dirname, '..', 'App.tsx'), 'utf8'));
const drawer = strip(readFileSync(resolve(__dirname, '..', 'features', 'dependencies', 'DetailDrawer.tsx'), 'utf8'));
const queries = strip(readFileSync(resolve(__dirname, '..', 'lib', 'queries', 'messaging.ts'), 'utf8'));
const api = strip(readFileSync(resolve(__dirname, '..', 'lib', 'api.ts'), 'utf8'));

describe('rota kaydı', () => {
  it('/messaging/topic App.tsx\'te ve chunk lazy', () => {
    expect(app).toContain("const MessagingTopic    = lazy(() => import('./pages/MessagingTopic'));");
    expect(app).toContain('<Route path="/messaging/topic" element={<MessagingTopic />} />');
    // Liste rotası AYNEN duruyor — sayfa onun yerine geçmiyor.
    expect(app).toContain('<Route path="/messaging"      element={<Messaging />} />');
  });
  it('sayfa kabuğu: PageShell + Topbar (range seçici)', () => {
    expect(page).toContain('<Topbar title="Topic" range={range} onRangeChange={setRange} />');
    expect(page).toContain('<PageShell>');
    // Env bayrağı VERİLMEZ: messaging okumaları `?env=` uygulamıyor.
    expect(page).not.toContain('envApplies');
  });
});

describe('URL sözleşmesi', () => {
  it('tek useSearchParams örneği', () => {
    expect(page.match(/useSearchParams\(\)/g)?.length ?? 0).toBe(1);
  });
  it('?tab= prev\'den türetilir ve replace:true ile yazılır', () => {
    expect(page).toContain('const setTab = (next: MsgTopicTab) => setParams(prev => {');
    // prev'den TÜREME: yabancı param (env, gelecekteki filtreler) korunur.
    expect(page).toContain('const p = new URLSearchParams(prev);');
    expect(page).toContain("if (next === 'producers') p.delete('tab'); else p.set('tab', next);");
    expect(page).toContain('{ replace: true }');
    // Sıfırdan kurulan bir query string yabancı param'ı sessizce düşürürdü.
    expect(page).not.toMatch(/setParams\(new URLSearchParams\(/);
  });
  it('sekme ve kimlik saf kodekten okunur (elle parse yok)', () => {
    expect(page).toContain("import { parseTopicRef, parseTopicTab, type MsgTopicTab } from './messaging/topicHref';");
    expect(page).toContain("const tab = parseTopicTab(params.get('tab'));");
    expect(page).toContain('const ref = useMemo(() => parseTopicRef(search), [search]);');
  });
  it('pencere memo içinde çözülür (v0.5.184 sonsuz refetch)', () => {
    expect(page).toContain('const { from, to } = useMemo(() => timeRangeToNs(range), [range]);');
  });
});

describe('maliyet disiplini — istemci metrik sorguları', () => {
  it('açılışta YALNIZ set=chart', () => {
    expect(page).toContain("set: 'chart', enabled: !!ref,");
    // Açılışta ikinci bir istemci sorgusu (topic/clients) kurulmuyor.
    expect(page).not.toContain("set: 'clients'");
    expect(page).not.toContain("set: 'topic'");
  });
  it('set=clients YALNIZ sekme seçilince: mount + enabled', () => {
    const i = page.indexOf("{tab === 'clients' && (");
    expect(i).toBeGreaterThan(-1);
    const block = page.slice(i, i + 500);
    expect(block).toContain('<KafkaClientsSection');
    expect(block).toContain('set="clients" enabled');
    // Bölüm koşulsuz mount edilirse maliyet sekmeden bağımsız ödenir.
    expect(page.match(/<KafkaClientsSection/g)?.length ?? 0).toBe(1);
  });
  it('diğer sekmeler EK İSTEK yapmaz — veri tek detay yükünden', () => {
    // Sayfada yalnız iki sorgu hook\'u var: detay + grafik.
    expect(page).toContain('const detailQ = useMessagingTopicDetail({');
    expect(page).toContain('const chartQ = useMessagingClients({');
    expect(page.match(/useMessagingClients\(\{/g)?.length ?? 0).toBe(1);
    expect(page.match(/useQuery\(/g)?.length ?? 0).toBe(0);
    // Sekme gövdeleri açılış yükünden besleniyor.
    for (const src of ['d?.callers ?? []', 'msgOperationRows(d?.operations)', 'd?.topOps ?? []']) {
      expect(page).toContain(src);
    }
  });
  it('set sorgu anahtarına girer ve tele yazılır', () => {
    expect(queries).toContain("p.fromNs, p.toNs, p.set ?? '']");
    expect(queries).toContain('api.messagingClients(p.system, p.cluster, p.destination, p.fromNs, p.toNs, signal, p.set)');
    expect(api).toContain("+ (set ? `&set=${encodeURIComponent(set)}` : '')");
    // Parametre yoksa tel bugünkü hâlinde: `set=` yazılmaz.
    expect(api).toContain("set?: 'chart' | 'topic' | 'clients'");
  });
});

describe('paylaşılan gövde — CallerSection', () => {
  it('sayfa ve çekmece AYNI dosyadan import ediyor', () => {
    expect(page).toContain("import { CallerSection } from '@/features/dependencies/CallerSection';");
    expect(drawer).toContain("import { CallerSection } from './CallerSection';");
  });
  it('sayfa kendi düzen anahtarlarını veriyor (çekmecenin genişlikleri taşmasın)', () => {
    expect(page).toContain('storageKey="msg-topic-producers" showReset');
    expect(page).toContain('storageKey="msg-topic-consumers" showReset');
    // Çekmece iki propu da GEÇMEZ — varsayılanlar bugünkü davranış.
    expect(drawer).not.toContain('storageKey="msg-topic');
    expect(drawer).not.toContain('showReset');
  });
  it('sekme tabloları paylaşılan primitifi kullanıyor', () => {
    for (const key of ['msg-topic-ops', 'msg-topic-spannames']) {
      expect(page).toContain(`storageKey: '${key}'`);
    }
    expect(page.match(/<DataTableColgroup dt=\{dt\} \/>/g)?.length ?? 0).toBe(2);
    expect(page.match(/<ResetLayoutButton dt=\{dt\} \/>/g)?.length ?? 0).toBe(2);
    // >100 satır ihtimali olan her tablo content-visibility taşır.
    expect(page.match(/contentVisibility: 'auto'/g)?.length ?? 0).toBe(2);
  });
  it('grafik panelleri AYRI storageKey (çekmece ikizini ezmesin)', () => {
    expect(page).toContain("storageKey=\"msg-topic-e2e\"");
    expect(page).toContain("storageKey=\"msg-topic-rate\"");
    expect(drawer).toContain('storageKey="msg-drawer-e2e"');
  });
});

describe('çekmeceden sayfaya kapı', () => {
  it('"Detay sayfası →" bağlantısı queue dalında ve pencereyi taşıyor', () => {
    expect(drawer).toContain("import { messagingTopicHref } from '@/pages/messaging/topicHref';");
    expect(drawer).toContain('to={messagingTopicHref({ system, cluster, destination: name, range })}');
    expect(drawer).toContain('Detay sayfası →');
    // Çekmecenin kendisi KALIYOR — link ikinci bir kapı, yerine geçen değil.
    const link = drawer.indexOf('Detay sayfası →');
    const kindQueue = drawer.indexOf("{kind === 'queue' && (");
    expect(kindQueue).toBeGreaterThan(-1);
    expect(link).toBeGreaterThan(kindQueue);
  });
});

// v0.10.589 — Partition'lar sekmesi: set=partitions YALNIZ sekme bileşeninde
// (fetch-on-open); sayfa açılışta onu istemez; sekme mount kapılı.
describe('partitions sekmesi (v0.10.589)', () => {
  const table = readFileSync(new URL('../features/dependencies/PartitionLagTable.tsx', import.meta.url), 'utf8');
  it("set: 'partitions' sayfada DEĞİL, yalnız tablo bileşeninde", () => {
    expect(page).not.toContain("set: 'partitions'");
    expect(table).toContain("set: 'partitions', enabled: true");
  });
  it('tablo yalnız sekme seçiliyken mount edilir', () => {
    expect(page).toContain("{tab === 'partitions' && (");
    expect(page).toContain('<PartitionLagTable system={system} cluster={cluster} destination={destination} range={range} />');
  });
  it('sekme kodekte tanımlı', () => {
    const codec = readFileSync(new URL('./messaging/topicHref.ts', import.meta.url), 'utf8');
    expect(codec).toContain("'partitions'");
  });
});
