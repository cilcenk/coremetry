// problemSubject.ts — v0.9.1339 (entity-model Faz 4b, frontend yarısı).
//
// ORİJİNAL SEMPTOM: `Problem.service` sorgusuz bir servis adı sayılıyordu.
// Backend'de v0.9.1338 bunu düzeltti (Problem.kind); burası o türü GÖRÜNÜR
// kılıyor. Öncesinde db_capacity.go'nun yazdığı `corebank-scan.prod` on
// küsur yüzeyde `<Link to={serviceHref(p.service)}>` olarak basılıyordu:
// link geçerli görünüyor, tıklanıyor, servis sayfası BOŞ açılıyor.
// Hata değil CEVAPSIZLIK — ve çalışmayan bir bağlantı, bağlantı
// olmamasından KÖTÜDÜR (aynı gerekçe: chstore selfHealthRunbooks yorumu).
//
// Bu dosya SAF: React yok, router yok, fetch yok. Tek işi bir özne
// dizgisini sınıflandırmak ve nasıl basılacağını söylemek.

/** Backend'in `problems.kind` evreni (chstore ProblemKind* sabitleri). */
export type SubjectKind = 'service' | 'db' | 'external';
export type ExternalSubject = { source: string; values: string[] };

/** `db:<system>@<instance>` çözümü. chstore.ParseDBSubjectID'nin ikizi. */
export type DbSubject = { system: string; instance: string };

// DB_PREFIX — chstore.TopologyNodeIDPrefixes'in `db:` girdisiyle AYNI
// dizgi. Frontend'de sözlüğü ithal edemiyoruz (ayrı dil), o yüzden tek
// sabit + problemSubject.test.ts'te biçim pini: iki taraf ayrışırsa test
// kırılır, ürün sessizce yanlış sınıflandırmaz.
const DB_PREFIX = 'db:';
// v0.10.228 (Influx D3) — dış metrik kaynağı öznesi: `ext:<kaynak>/<v1>/<v2>`.
const EXT_PREFIX = 'ext:';

export function parseExternalSubject(subject: string): ExternalSubject | null {
  if (!subject.startsWith(EXT_PREFIX)) return null;
  const parts = subject.slice(EXT_PREFIX.length).split('/');
  if (parts[0] === '') return null;
  return { source: parts[0], values: parts.slice(1) };
}

/**
 * parseDbSubject — `db:<system>@<instance>` çözer, değilse null.
 *
 * Ayırıcı İLK '@': system asla '@' taşımaz, instance (bir host adı)
 * teorik olarak taşıyabilir. "'@' varsa böl" yazmak `checkout@v2` gibi
 * bir SERVİS adını db öznesi sanardı — önek ZORUNLU.
 */
export function parseDbSubject(subject: string): DbSubject | null {
  if (!subject.startsWith(DB_PREFIX)) return null;
  const rest = subject.slice(DB_PREFIX.length);
  const at = rest.indexOf('@');
  if (at <= 0 || at === rest.length - 1) return null;
  return { system: rest.slice(0, at), instance: rest.slice(at + 1) };
}

/**
 * subjectKind — satırın özne türü.
 *
 * `kind` alanı BOŞ olabilir ve boş = service: (a) kolonu ekleyen boot'un
 * probe'u false okur (küme kipinde DDL ertelenir), (b) v0.9.1338 öncesi
 * yazılmış satırlar. Backend zaten normalize ediyor; burada TEKRAR
 * normalize ediyoruz çünkü bu fonksiyon eski bir tarayıcı sekmesinin
 * önbelleğindeki JSON'u da görebilir.
 *
 * Tanınmayan bir kind DEĞERİ 'service' sayılmaz — biçimden karar verilir.
 * Böylece backend yarın 'queue' eklerse bu dosya onu servis diye basmaz.
 *
 * ⚠️ AD ÇAKIŞMASI — parametreler NEDEN nesne değil, ayrı ayrı:
 * `InboxItem.kind` ZATEN VAR ve TAMAMEN BAŞKA bir şey söylüyor
 * (`problem | exception | anomaly` — satırın KAYNAĞI, öznenin türü
 * değil). Bu fonksiyon bir nesne alsaydı `<SubjectLink item={inboxItem}/>`
 * sessizce yanlış alanı okurdu ve TypeScript UYARMAZDI: iki alan da
 * `string`. identity.go'daki clusterExpr çakışmasının (v0.9.1318) birebir
 * ikizi. Ayrı parametreler çağıranı hangi alanı verdiğini YAZMAYA
 * zorluyor.
 */
export function subjectKind(service: string, kind?: string): SubjectKind {
  if (kind === 'external' || parseExternalSubject(service)) return 'external';
  if (kind === 'db' || parseDbSubject(service)) return 'db';
  return 'service';
}

/**
 * subjectLabel — operatöre gösterilecek metin.
 *
 * db öznesinde ham `db:oracle@corebank-scan.prod` yerine okunabilir
 * `oracle · corebank-scan.prod`. Ham biçim bir MAKİNE kimliği; onu
 * ekrana basmak v0.9.1029'un topoloji tarafında yaptığı hataydı
 * (düğüm ham `queue:kafka:api.usage` adıyla görünüyordu).
 */
export function subjectLabel(service: string): string {
  const ext = parseExternalSubject(service);
  if (ext) return [ext.source, ...ext.values].join(' · ');
  const db = parseDbSubject(service);
  if (db) return `${db.system} · ${db.instance}`;
  return service;
}

/**
 * subjectHref — öznenin detay linki, ya da null ("link YOK").
 *
 * null İKİ durumda döner ve ikisi de bilinçli:
 *   • kind='db' — ÖLÇÜLDÜ (2026-08-24): kapasite problemlerinin taşıdığı
 *     receiver instance'ı (`corebank-scan.prod`) ile span türevli
 *     `db_summary_5m.instance` (`oracle`) AYRI kimlik uzayları, kesişim
 *     0 satır. Yani /databases linki de boş bir satıra giderdi. Köprü
 *     kurulana dek (ayrı dilim) DOĞRU cevap "link yok".
 *   • özne boş — bağlanacak bir şey yok (global log-query kuralları).
 *
 * Servis öznesinde çağıran serviceHref'i kendi opsiyonlarıyla kurar;
 * bu fonksiyon onu ÇAĞIRMAZ, yalnız "kurulabilir mi" sorusunu
 * cevaplar. Böylece her çağrı yeri kendi range/tab/env'ini taşımaya
 * devam eder ve v0.9.860'ın pencere-taşıma sözleşmesi bozulmaz.
 */
export function subjectIsLinkable(service: string, kind?: string): boolean {
  return service !== '' && subjectKind(service, kind) === 'service';
}

/**
 * subjectTitle — linksiz basılan öznenin `title` metni. Operatör neden
 * tıklayamadığını görebilmeli; sessiz bir düz-metin "bu satır bozuk"
 * gibi okunur.
 */
export function subjectTitle(service: string): string | undefined {
  const ext = parseExternalSubject(service);
  if (ext) {
    return `Dış metrik kaynağı (${ext.source}) serisi — bir servis değil, ` +
      `bu yüzden servis sayfası linki yok`;
  }
  const db = parseDbSubject(service);
  if (db) {
    return `${db.system} veritabanı örneği (${db.instance}) — bir servis değil, ` +
      `bu yüzden servis sayfası linki yok`;
  }
  return undefined;
}

/**
 * derivedTeamTitle — TÜRETİLMİŞ sahipliğin çekincesi (v0.9.1345).
 *
 * Bir db konusunun katalogda satırı yok, o yüzden takımı "bu veritabanını
 * EN ÇOK ÇAĞIRAN servisin takımı" kuralıyla türetiliyor (operatör kararı
 * 2026-08-24, backend chstore/db_ownership.go).
 *
 * Metin İKİ şeyi birden söylemek zorunda ve ikisi de gerekli:
 *
 *  1. KANIT — hangi servis üzerinden türetildi. Operatör cevabı tartabilsin.
 *  2. SINIR — çözüm veritabanı SİSTEMİ düzeyinde yapılıyor, tekil örnek
 *     düzeyinde DEĞİL (iki kimlik uzayı kesişmiyor; backend'deki ölçülmüş
 *     uyarı). Yani aynı sistemden iki küme farklı takımlara aitse ikisi de
 *     aynı takıma yazılır.
 *
 * (2) olmadan bu metin kesin bir atıf gibi okunur. Bu repoda kendinden
 * emin görünen yanlış cevap, çekinceli cevaptan kötüdür.
 */
export function derivedTeamTitle(via: string, service?: string): string {
  const db = service ? parseDbSubject(service) : null;
  const what = db ? `${db.system} veritabanını` : 'bu veritabanını';
  return `Türetilmiş sahiplik (kesin atıf değil) — ${what} en çok çağıran ` +
    `servis "${via}" olduğu için onun katalog takımı gösteriliyor. ` +
    `Çözüm veritabanı SİSTEMİ düzeyinde yapılır, tekil örnek düzeyinde değil: ` +
    `aynı sistemden birden çok küme varsa hepsi bu takıma yazılır.`;
}
