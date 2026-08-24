import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

// problemSubjectSites.test.ts — v0.9.1339, İKİZ-YAZIM KAPISI.
//
// Yukarıdaki saf testler sınıflandırmanın doğru olduğunu kanıtlıyor ama
// KULLANILDIĞINI kanıtlamıyor. Bu repoda tam olarak o boşluk daha önce
// açıldı: "bir saf yardımcı yazıldı, sayfa onu ÇAĞIRMADI" ve kapı
// çağrının VARLIĞINI ölçtüğü için ısırmadı.
//
// Burada ölçülen şey varlık değil YOKLUK: problem/inbox öznesini basan
// dosyalarda `<Link to={serviceHref(<özne>.service …)}>` kalıbı BİR DAHA
// belirmemeli. Belirirse yedinci çıkmaz link doğmuş demektir.

const ROOT = join(__dirname, '..');

// Özne basan yüzeyler. Yeni bir tanesi doğduğunda bu listeye girmezse
// kapı onu görmez — o yüzden liste UZUNLUĞU da ayrı bir testte pinli
// değil, bilinçli: kapının kapsamı gözle genişletilir, ama içindeki
// dosyalar mekanik olarak taranır.
const SUBJECT_SURFACES = [
  'features/anomalies/ProblemsSection.tsx',
  'features/anomalies/ProblemDetail.tsx',
  'features/anomalies/streams.tsx',
  'pages/Inbox.tsx',
  'pages/Shift.tsx',
  'components/InboxTriageDrawer.tsx',
];

// MUAFİYET — her biri GEREKÇELİ ve staleness-testli. Ortak ölçüt:
// satırın `service` alanı bir PROBLEM ÖZNESİ değil, span türevli GERÇEK
// bir servis adı. Gerekçe düşerse alt taraftaki bayatlık testi kırılır.
const ALLOWED: { file: string; needle: string; why: string }[] = [
  {
    file: 'features/anomalies/ProblemDetail.tsx',
    needle: "tab: 'pods', params: { jpod: pod }",
    why: 'pod pill yalnız problem.pod doluyken çizilir; o alanı SADECE ' +
      'runtime denetimleri yazar ve onların Service\'i v0.9.401\'den beri ' +
      'gerçek bir servis adıdır. db özneli satırda pod boş → dal koşmaz.',
  },
  {
    file: 'pages/Shift.tsx',
    needle: 'serviceHref(c.service, { range: pageWindow }',
    why: 'ChangedService satırı — /api/shift\'in RED kıyasından geliyor, ' +
      'kaynağı spans.service_name. Problem öznesi DEĞİL.',
  },
  {
    file: 'pages/Shift.tsx',
    needle: 'serviceHref(g.service, { range: exceptionGroupWindow(g) }',
    why: 'ExceptionGroup satırı — exception_groups.service, span türevli ' +
      'gerçek servis adı. Problem öznesi DEĞİL.',
  },
  {
    file: 'features/anomalies/ProblemDetail.tsx',
    needle: 'serviceHref(group.service',
    why: 'ExceptionGroup başlığı — exception_groups.service, span türevli ' +
      'gerçek servis adı. Problem satırı DEĞİL.',
  },
  {
    file: 'features/anomalies/streams.tsx',
    needle: 'serviceHref(a.service',
    why: 'TraceOpAnomaly satırı — span türevli operasyon anomalisi, ' +
      'service_name doğrudan spans\'ten. Problem öznesi DEĞİL.',
  },
  {
    file: 'features/anomalies/streams.tsx',
    needle: 'serviceHref(e.service',
    why: 'AnomalyEvent satırı — anomaly.go/clustering.go GERÇEK servis adı ' +
      'yazar (üretici denetimi 2026-08-24). Problem öznesi DEĞİL.',
  },
];

function read(rel: string): string {
  return readFileSync(join(ROOT, rel), 'utf8');
}

describe('problem öznesi çıkmaz link üretmiyor', () => {
  it.each(SUBJECT_SURFACES)('%s ham serviceHref linki kurmuyor', (rel) => {
    const src = read(rel);
    // `to={serviceHref(` + bir özne değişkeninin `.service`i.
    const exempt = ALLOWED.filter(a => a.file === rel).map(a => a.needle);
    const raw = [...src.matchAll(/to=\{serviceHref\(\w+\.service[\s\S]{0,120}/g)]
      .map(m => m[0])
      .filter(hit => !exempt.some(n => hit.includes(n)));
    expect(raw, `${rel} içinde gate'lenmemiş servis linki:\n${raw.join('\n')}\n` +
      `Özne bir servis OLMAYABİLİR (db-capacity alarmları). <SubjectLink ` +
      `service=… subjectKind=… href={serviceHref(…)} /> kullan.`).toEqual([]);
  });

  // POZİTİF KONTROL — yukarıdaki kapı "hiç serviceHref yok" dünyasında da
  // yeşil kalırdı, yani tek başına hiçbir şey kanıtlamaz. Bu test
  // dönüşümün GERÇEKTEN yapıldığını ölçüyor.
  it.each(SUBJECT_SURFACES)('%s SubjectLink kullanıyor', (rel) => {
    expect(read(rel)).toContain('<SubjectLink');
  });

  // Ve muafiyetin hâlâ orada olduğunu — silinirse liste bayatlar ve
  // kapı yanlış bir şeyi affetmeye devam ederdi.
  // Bayatlık kapısı: bir muafiyet kaynaktan silinince liste onu affetmeye
  // DEVAM ederdi ve o dosyada yeni bir çıkmaz link doğsa görülmezdi.
  it.each(ALLOWED)('muafiyet hâlâ gerçek bir kalıba karşılık geliyor: $needle', (a) => {
    expect(read(a.file), `muafiyet bayat (${a.why})`).toContain(a.needle);
  });

  // Servis-adı gerektiren pivot bloğu da tür kapılı olmalı.
  it('ProblemDetail pivotları özne türüne kapılı', () => {
    const src = read('features/anomalies/ProblemDetail.tsx');
    expect(src).toContain("subjectKind(problem.service, problem.kind) !== 'service'");
  });
});

describe('backend ile frontend aynı biçimi konuşuyor', () => {
  // DB_PREFIX ve ayırıcı iki dilde AYRI yazılmak zorunda (Go sabitini
  // TypeScript'e ithal edemiyoruz). Ayrıştıkları an frontend her db
  // öznesini servis sanar ve çıkmaz link geri gelir — sessizce.
  it('db: öneki ve @ ayırıcısı chstore/identity.go ile aynı', () => {
    const go = readFileSync(join(ROOT, '../../internal/chstore/identity.go'), 'utf8');
    expect(go).toContain('{"db:", NodeKindDB}');
    expect(go).toContain('return dbNodePrefix() + system + "@" + instance');
    const ts = read('lib/problemSubject.ts');
    expect(ts).toContain("const DB_PREFIX = 'db:';");
    expect(ts).toContain("const at = rest.indexOf('@');");
  });
});
