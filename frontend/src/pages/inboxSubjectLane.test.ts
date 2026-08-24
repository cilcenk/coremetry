import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

// inboxSubjectLane.test.ts — v0.9.1342, ŞERİDİN BAĞLI OLDUĞU kapı.
//
// Operatör kararı: DB özneli problemler /inbox'ta AYRI ŞERİT, aynı liste
// DEĞİL — servis problemleriyle öncelik sırasında yarışmasınlar.
//
// Buradaki testler saf mantığı değil BAĞLANTIYI ölçüyor. Bu repoda tam
// o boşluk daha önce açıldı: saf yardımcı doğru, sayfa onu çağırmıyor,
// kapı da çağrının varlığını ölçmediği için ısırmıyor (v0.9.1339 dersi).

const ROOT = join(__dirname, '..');
const read = (rel: string) => readFileSync(join(ROOT, rel), 'utf8');

describe('inbox özne şeridi — bağlantı kapısı', () => {
  const page = read('pages/Inbox.tsx');

  it('sayfa şeridi SUNUCUYA gönderiyor', () => {
    // Şerit bir SUNUCU filtresi: SQL'de, LIMIT'ten önce daralıyor.
    // İstemcide filtrelemek v0.9.330'un "Exceptions 0" yalanı olurdu —
    // sunucunun 300'lük cap'i db satırlarını da yer, sonra sayfa onları
    // atar ve kuyruk boş görünür.
    expect(page).toContain('subject: subjectLane');
    expect(page).toMatch(/searchParams\.get\('subject'\) === 'db'/);
  });

  it('şerit URL yazıcısından geçiyor ve varsayılan param BIRAKMIYOR', () => {
    // Tek yazıcı setParam (replace:true + prev kopyası), yoksa yabancı
    // paramlar (?item= çekmecesi, ?problem= host'u) düşer.
    expect(page).toContain("setParam('subject', s === 'service' ? null : s)");
  });

  it('çip sayısı SUNUCUDAN, dönen sayfadan DEĞİL', () => {
    // Servis şeridindeyken db satırları dönen sayfada zaten YOK, yani
    // sayfadan türetilen her sayı 0 çıkardı ve çip "veritabanı problemi
    // yok" derdi — ölçmediği bir şeyi ölçmüş gibi.
    expect(page).toContain('inboxQ.data?.dbSubjectCount');
    expect(page).not.toMatch(/dbSubjectCount\s*\?\?\s*0/);
    // undefined (henüz cevap yok) ile 0 (ölçüldü, yok) AYRI gösterilmeli.
    expect(page).toContain('dbLaneCount !== undefined');
  });

  it('db şeridinde Tür facet\'i çizilmiyor', () => {
    // O şeritte sunucu kind'i ["problem"]e zorluyor; açık bırakmak
    // tıklandığında hiçbir şey değiştirmeyen bir kontrol göstermek olurdu.
    expect(page).toContain("{subjectLane === 'service' && (");
  });

  it('şerit sözlüğü KAPALI ve iki değerli', () => {
    expect(page).toMatch(/SUBJECT_LANES: readonly SubjectLane\[\] = \['service', 'db'\]/);
  });
});

describe('özne şeridi ile satır KAYNAĞI karışmıyor', () => {
  const types = read('lib/types.ts');

  it('SubjectLane ile InboxKind AYRI tipler', () => {
    // İkisi de string olsaydı derleyici karışıklığı yakalayamazdı ve
    // v0.9.1339 tam olarak o çakışmadan bir bug üretti: nesne alan bir
    // yardımcı yanlış alanı sessizce okuyor.
    expect(types).toContain("export type SubjectLane = 'service' | 'db'");
    expect(types).toMatch(/export type InboxKind =[^\n]*'incident'/);
    // InboxItem.subjectKind DAR tipte olmalı — düz `string` bırakmak
    // ayrı-tip korumasını yok eder.
    expect(types).toContain('subjectKind?: SubjectLane;');
  });

  it('api istemcisi iki paramı ayrı adlarla taşıyor', () => {
    const api = read('lib/api.ts');
    const inbox = api.slice(api.indexOf('  inbox: (params: {'));
    expect(inbox.slice(0, 3000)).toContain('subject?: ');
    expect(inbox.slice(0, 3000)).toContain('kind?: string;');
  });
});
