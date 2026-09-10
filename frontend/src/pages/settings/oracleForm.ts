// oracleForm (v0.10.580) — OracleTab'ın kutuları ile tel arasındaki çeviri
// ve doğrulama. influxForm.ts deseni: SAF ve TABLO-TESTLİ; sekme yalnız
// çizer, karar buradadır (asıl test yüzeyi bu dosya).
//
// Neden burada İKİNCİ bir doğrulama var (sunucu zaten Normalize ediyor):
// üç ayrı gerekçe, üçü de kozmetik değil.
//   1. Şema/tablo/kolon adları Oracle sorgusuna IDENTIFIER olarak girer —
//      bind EDİLEMEZLER (internal/oracle/client.go). Aynı kapıyı formda da
//      tutmak, operatörün "neden reddedildi" sorusunu round-trip'siz
//      cevaplar; kapı tek yerde kalsaydı hata mesajı alan başına DEĞİL,
//      kaynak başına gelirdi.
//   2. `extraWhere` serbest ifadedir: tek bir `--` sorgunun kalanını (zaman
//      yüklemi + FETCH FIRST tavanı) yorum yapar ve sınırsız taramaya
//      çevirir. Bu yüzden `;` `--` `/*` üçlüsü İKİ tarafta da yasak.
//   3. Sayısal kelepçeler sessizce KIRPILMIYOR (sunucu da kırpmıyor):
//      operatör 100 yazıp 16 uygulanmasını göremezdi.
//
// Doğrulamanın sunucudan TEK sapması: burada hiçbir kural sunucununkinden
// KATI değil. Katı olsaydı, sunucunun kabul ettiği meşru bir yapılandırma
// formda kaydedilemezdi — bu, kapı değil duvar olurdu.
//
// Şifre sözleşmesi (v0.10.224 Influx kararının ikizi): GET şifreyi geri
// vermez, dolayısıyla form onu ASLA bilmez. Boş kutu "sil" değil "dokunma"
// demektir ve `sourceForSave` bunu `password` anahtarını gövdeye HİÇ
// KOYMAYARAK ifade eder — boş string göndermek sunucuda aynı sonucu verirdi
// ama niyeti belirsiz bırakırdı.
import type { OracleSource, OracleSourceSnapshot } from '@/lib/types';
// Sayı/liste kutusu çevirileri InfluxTab ile BAYT BAYT aynı işi yapıyor;
// ikinci bir nüsha yazmak ölçülü kopya ailelerinin (Stat ×6, Field ×7)
// doğum mekanizmasının ta kendisi olurdu. `numToForm` 0'ı boş kutuya
// çevirir — Oracle'da da 0 "sunucu varsayılanı" demek, aynı anlam.
export { numFromForm, numToForm, parseList, listToText } from './influxForm';
import { parseList, listToText } from './influxForm';

// ── Varsayılanlar — internal/oracle/settings.go sabitlerinin aynası ──────
export const ORACLE_DEFAULT_PORT = 1521;
export const ORACLE_DEFAULT_MAX_OPEN_CONNS = 4;
export const ORACLE_MIN_MAX_OPEN_CONNS = 1;
export const ORACLE_MAX_MAX_OPEN_CONNS = 16;
export const ORACLE_DEFAULT_QUERY_TIMEOUT_SEC = 20;
export const ORACLE_MIN_QUERY_TIMEOUT_SEC = 5;
export const ORACLE_MAX_QUERY_TIMEOUT_SEC = 120;
export const ORACLE_DEFAULT_INTERVAL_SEC = 60;
export const ORACLE_MIN_INTERVAL_SEC = 10;
export const ORACLE_MAX_INTERVAL_SEC = 3600;
export const ORACLE_DEFAULT_TIMESTAMP_COLUMN = 'MCA_ERR_TIMESTAMP';
export const ORACLE_DEFAULT_TYPE_COLUMN = 'MCA_ERR_TYPE';
export const ORACLE_MAX_EXTRA_WHERE = 500;
export const ORACLE_MAX_TYPE_FILTER = 16;
export const ORACLE_MAX_TYPE_VALUE_LEN = 32;
export const ORACLE_MAX_NAME_LEN = 64;
// v0.10.603 — Aşama 2 eşleme ayarları (internal/oracle/mapping.go aynası).
export const ORACLE_DEFAULT_TIMEZONE = 'Europe/Istanbul';
/** Form kutusunda `-` = "bu alan tabloda yok" → tel'de "" (sunucu alanı kapatır).
 *  Boş kutu = varsayılan kolon (anahtar gövdeye HİÇ konmaz). */
export const ORACLE_COLUMN_DISABLED = '-';
/** Alan → varsayılan Oracle kolonu; timestamp/type kendi kutularından gelir
 *  (timestampColumn/typeColumn), bu listede DEĞİLLER. Sıra Go fieldOrder. */
export const ORACLE_MAPPING_FIELDS: ReadonlyArray<{ field: string; target: string; column: string }> = [
  { field: 'severity',     target: 'severity',            column: 'MCA_ERR_SEVERITY' },
  { field: 'message',      target: 'body',                column: 'MCA_ERR_MESSAGE' },
  { field: 'traceId',      target: 'trace_id',            column: 'MCA_ERR_TRACEID' },
  { field: 'host',         target: 'host.name',           column: 'MCA_ERR_HOSTNAME' },
  { field: 'instance',     target: 'oracle.instance_id',  column: 'MCA_ERR_INSTANCE_ID' },
  { field: 'service',      target: 'operation.code',      column: 'MCA_ERR_SERVICE' },
  { field: 'code',         target: 'error.code',          column: 'MCA_ERR_CODE' },
  { field: 'externalCode', target: 'error.external_code', column: 'MCA_ERR_EXTERNAL_CODE' },
  { field: 'channel',      target: 'channel.code',        column: 'MCA_ERR_CHANNELCODE' },
  { field: 'task',         target: 'task.code',           column: 'MCA_ERR_TASKCODE' },
  { field: 'requestId',    target: 'request.id',          column: 'MCA_ERR_REQUESTID' },
  { field: 'customerId',   target: 'customer.id',         column: 'MCA_ERR_CUSTOMERID' },
  { field: 'tellerId',     target: 'teller.id',           column: 'MCA_ERR_TELLERID' },
  { field: 'location',     target: 'location',            column: 'MCA_ERR_LOCATION' },
];
// IANA dilim adı biçimi (Europe/Istanbul, UTC, Etc/GMT+3). Gerçek varlık
// kontrolü sunucuda (time.LoadLocation): burada yalnız biçim.
const TZ_RE = /^[A-Za-z_]+(?:\/[A-Za-z0-9_+-]+)*$/;

/** Varsayılan tip süzgeci. Fonksiyon, sabit dizi DEĞİL: çağıran dönen diziyi
 *  değiştirse bile varsayılan bozulmaz (paylaşılan-dilim tuzağı, Go tarafında
 *  `DefaultTypeFilter()` aynı gerekçeyle fonksiyon). */
export function defaultTypeFilter(): string[] { return ['T']; }

// Oracle identifier'ı: harfle başlar, ≤30 karakter (11g sınırı). SQL'e
// TIRNAKSIZ girdiği için bu regex'in geçirdiği her şey enjeksiyon-güvenli
// olmak ZORUNDA — boşluk, tırnak, noktalı virgül, tire hiçbiri geçmiyor.
const IDENT_RE = /^[A-Za-z][A-Za-z0-9_$#]{0,29}$/;
const NAME_RE = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$/;
const DSN_RE = /^oracle:\/\//i;
// secretref.Valid aynası: `env:POSIX_ADI` ya da `file:/mutlak/yol`.
const SECRET_REF_RE = /^(env:[A-Za-z_][A-Za-z0-9_]*|file:\/\S+)$/;

/** ExtraWhere'de YASAK diziler + neden. `;` ikinci bir ifade açar; `--` ve
 *  `/*` sorgunun kalanını yorum yapıp sınırsız taramaya çevirir. */
const EXTRA_WHERE_BANNED: ReadonlyArray<readonly [string, string]> = [
  [';', 'noktalı virgül ikinci bir ifade açar'],
  ['--', 'satır yorumu sorgunun kalanını (zaman yüklemi, satır tavanı) susturur'],
  ['/*', 'blok yorumu sorgunun kalanını (zaman yüklemi, satır tavanı) susturur'],
];

export type OracleField =
  | 'name' | 'dsn' | 'host' | 'port' | 'serviceName' | 'user' | 'password'
  | 'passwordRef' | 'schema' | 'table' | 'timestampColumn' | 'typeColumn'
  | 'extraWhere' | 'typeFilter' | 'maxOpenConns' | 'queryTimeoutSec' | 'intervalSec'
  | 'timezone' | 'columns';

export type OracleFieldErrors = Partial<Record<OracleField, string>>;

export interface ValidateOracleOptions {
  /** Ad tekilliği için DİĞER kaynaklar (kendisi hariç). */
  others?: readonly OracleSource[];
  /** Snapshot'ın `hasPassword`'ü — saklı şifre varsa kutunun boş olması
   *  eksiklik değil, "dokunma" demektir. */
  hasStoredPassword?: boolean;
  /** Zorunluluk kuralları (host/servis/kullanıcı/şifre/şema/tablo) uygulansın
   *  mı. Varsayılan `src.enabled`: kapalı bir taslak yarım kaydedilebilir
   *  (sunucu Normalize'ı da aynı kapıyı `Enabled` altında tutuyor), ama
   *  "Bağlantıyı dene" yolunda çağıran bunu `true` verir — test ucu kaynağı
   *  zorla etkin sayarak doğruluyor. BİÇİM kuralları her hâlde koşar. */
  requireComplete?: boolean;
}

/** Yeni kaynak taslağı. `enabled: false` bilinçli: yarım doldurulmuş bir satır
 *  ilk anda kırmızıya boyanmasın, operatör bitirince kendisi açsın. */
export function emptyOracleSource(): OracleSource {
  return {
    name: '',
    dsn: '',
    host: '',
    port: ORACLE_DEFAULT_PORT,
    serviceName: '',
    user: '',
    password: '',
    passwordRef: '',
    schema: '',
    table: '',
    timestampColumn: '',
    typeColumn: '',
    extraWhere: '',
    typeFilter: defaultTypeFilter(),
    maxOpenConns: ORACLE_DEFAULT_MAX_OPEN_CONNS,
    queryTimeoutSec: ORACLE_DEFAULT_QUERY_TIMEOUT_SEC,
    intervalSec: ORACLE_DEFAULT_INTERVAL_SEC,
    timezone: '',
    timestampHasZone: false,
    columns: {},
    enabled: false,
  };
}

const trim = (v: string | undefined | null): string => (v ?? '').trim();

/** Tip süzgeci metni → tekilleştirilmiş liste (virgül/yeni satır ayırır). */
export function parseTypeFilter(text: string): string[] {
  const out: string[] = [];
  for (const v of parseList(text)) if (!out.includes(v)) out.push(v);
  return out;
}

export function typeFilterToText(list: readonly string[] | undefined | null): string {
  return listToText(list ? [...list] : undefined);
}

/** Sayısal kelepçe: boş/0 = sunucu varsayılanı (hata DEĞİL); aralık dışı ya da
 *  tam sayı olmayan değer HATA — sessiz kırpma yok. */
function clampError(v: number | undefined, min: number, max: number, label: string): string | undefined {
  if (v === undefined || v === 0) return undefined;
  if (!Number.isFinite(v) || !Number.isInteger(v)) return `${label} tam sayı olmalı.`;
  if (v < min || v > max) return `${label} ${min}-${max} arasında olmalı.`;
  return undefined;
}

/** Alan başına Türkçe hata haritası. Boş nesne = form kaydedilebilir.
 *  Kural sırası sunucunun `Normalize`'ıyla aynı, mesajlar operatörün gördüğü
 *  kutunun ALTINA basılacak şekilde alan başına ayrılmış. */
export function validateOracleSource(
  src: OracleSource,
  opts: ValidateOracleOptions = {},
): OracleFieldErrors {
  const e: OracleFieldErrors = {};
  const mustBeComplete = opts.requireComplete ?? src.enabled;

  // ── ad: zorunlu + biçim + tekil ────────────────────────────────────────
  const name = trim(src.name);
  if (!name) {
    e.name = 'Ad zorunlu.';
  } else if (!NAME_RE.test(name)) {
    e.name = `Ad harf ya da rakamla başlamalı; yalnız harf, rakam ve _ . - içerebilir (≤${ORACLE_MAX_NAME_LEN}).`;
  } else if ((opts.others ?? []).some(o => trim(o.name).toLowerCase() === name.toLowerCase())) {
    e.name = 'Bu ad başka bir kaynakta kullanılıyor — kaynak adı tekil olmalı.';
  }

  // ── bağlantı: ya tek parça dsn, ya host/port/serviceName ───────────────
  const dsn = trim(src.dsn);
  const host = trim(src.host);
  const serviceName = trim(src.serviceName);
  if (dsn && (host || serviceName)) {
    e.dsn = 'Ya dsn ya host/serviceName verin — ikisi birden verilirse hangisinin geçerli olduğu belirsiz kalır.';
  }
  if (dsn && !DSN_RE.test(dsn)) {
    e.dsn = 'dsn `oracle://kullanıcı:şifre@host:port/servis` biçiminde olmalı.';
  }
  if (dsn) {
    // Port kutusu dsn kipinde YOK SAYILIR (sourceForSave gövdeye koymaz),
    // bu yüzden burada hata da üretilmez: sunucu yalnız GERÇEKTEN gönderilen
    // bir port'a itiraz eder.
  } else {
    if (mustBeComplete && !host) e.host = 'Host zorunlu (ya da tek parça dsn verin).';
    if (mustBeComplete && !serviceName) e.serviceName = 'Servis adı zorunlu (ya da tek parça dsn verin).';
    const port = src.port;
    if (port !== undefined && port !== 0 && (!Number.isInteger(port) || port < 1 || port > 65535)) {
      e.port = 'Port 1-65535 arasında bir tam sayı olmalı.';
    }
  }

  // ── kimlik ─────────────────────────────────────────────────────────────
  // Kullanıcı adı yalnız dsn YOKKEN zorunlu: tek parça dsn'de credential
  // dizenin içindedir (sunucu da orada aramaz).
  if (mustBeComplete && !dsn && !trim(src.user)) e.user = 'Kullanıcı adı zorunlu.';
  const ref = trim(src.passwordRef);
  if (ref && !SECRET_REF_RE.test(ref)) {
    e.passwordRef = 'passwordRef `env:AD` ya da `file:/mutlak/yol` biçiminde olmalı — düz şifre buraya yazılmaz.';
  }
  if (mustBeComplete && !dsn && !trim(src.password) && !ref && !opts.hasStoredPassword) {
    e.password = 'Şifre zorunlu — şifre girin ya da passwordRef (env:/file:) verin.';
  }

  // ── identifier'lar: biçim HER ZAMAN, zorunluluk yalnız etkin kaynakta ──
  const idents: ReadonlyArray<readonly [OracleField, string, string, boolean]> = [
    ['schema', trim(src.schema), 'Şema', true],
    ['table', trim(src.table), 'Tablo', true],
    ['timestampColumn', trim(src.timestampColumn), 'Zaman kolonu', false],
    ['typeColumn', trim(src.typeColumn), 'Tip kolonu', false],
  ];
  for (const [field, value, label, mandatory] of idents) {
    if (!value) {
      if (mandatory && mustBeComplete) e[field] = `${label} zorunlu.`;
      continue;
    }
    if (!IDENT_RE.test(value)) {
      e[field] = `${label} adı Oracle identifier'ı olmalı: harfle başlar, yalnız harf/rakam/_ $ # içerir, ≤30 karakter.`;
    }
  }

  // ── extraWhere: uzunluk + enjeksiyon kalıpları ─────────────────────────
  const where = trim(src.extraWhere);
  if (where.length > ORACLE_MAX_EXTRA_WHERE) {
    e.extraWhere = `Ek koşul en çok ${ORACLE_MAX_EXTRA_WHERE} karakter olabilir (şu an ${where.length}).`;
  } else {
    for (const [bad, why] of EXTRA_WHERE_BANNED) {
      if (where.includes(bad)) {
        e.extraWhere = `Ek koşul \`${bad}\` içeremez — ${why}.`;
        break;
      }
    }
  }

  // ── tip süzgeci ────────────────────────────────────────────────────────
  const types = (src.typeFilter ?? []).map(t => t.trim()).filter(Boolean);
  if (types.length > ORACLE_MAX_TYPE_FILTER) {
    e.typeFilter = `En çok ${ORACLE_MAX_TYPE_FILTER} tip değeri verilebilir.`;
  } else if (types.some(t => t.length > ORACLE_MAX_TYPE_VALUE_LEN)) {
    e.typeFilter = `Tip değeri en çok ${ORACLE_MAX_TYPE_VALUE_LEN} karakter olabilir.`;
  }

  // ── sayısal kelepçeler ─────────────────────────────────────────────────
  const mo = clampError(src.maxOpenConns, ORACLE_MIN_MAX_OPEN_CONNS, ORACLE_MAX_MAX_OPEN_CONNS, 'Bağlantı havuzu');
  if (mo) e.maxOpenConns = mo;
  const qt = clampError(src.queryTimeoutSec, ORACLE_MIN_QUERY_TIMEOUT_SEC, ORACLE_MAX_QUERY_TIMEOUT_SEC, 'Sorgu zaman aşımı (sn)');
  if (qt) e.queryTimeoutSec = qt;
  const iv = clampError(src.intervalSec, ORACLE_MIN_INTERVAL_SEC, ORACLE_MAX_INTERVAL_SEC, 'Poll aralığı (sn)');
  if (iv) e.intervalSec = iv;

  // ── zaman dilimi + kolon eşlemesi (v0.10.603) ──────────────────────────
  const tz = trim(src.timezone);
  if (tz && !TZ_RE.test(tz)) {
    e.timezone = 'Zaman dilimi IANA adı olmalı (Europe/Istanbul, UTC gibi).';
  }
  for (const [field, raw] of Object.entries(src.columns ?? {})) {
    const col = trim(raw);
    if (!col || col === ORACLE_COLUMN_DISABLED) continue;
    if (!ORACLE_MAPPING_FIELDS.some(f => f.field === field)) {
      e.columns = `Bilinmeyen eşleme alanı: ${field}.`;
      break;
    }
    if (!IDENT_RE.test(col)) {
      e.columns = `${field} kolonu Oracle identifier'ı olmalı: harfle başlar, yalnız harf/rakam/_ $ # içerir, ≤30 karakter.`;
      break;
    }
  }

  return e;
}

/** Herhangi bir kaynakta hata var mı — Kaydet kapısı. */
export function hasOracleErrors(errs: OracleFieldErrors): boolean {
  return Object.keys(errs).length > 0;
}

/** Form satırı → PUT/test gövdesi.
 *
 *  ŞİFRE SÖZLEŞMESİ: kullanıcı kutuya bir şey YAZMADIYSA `password` anahtarı
 *  gövdeye HİÇ konmaz. Sunucu boş girdiyi de "saklıyı koru" diye yorumluyor,
 *  ama boş string göndermek niyeti belirsiz bırakırdı — anahtarın yokluğu
 *  "bu alana dokunmadım"ın tek dürüst yazımı.
 *
 *  Boş metin alanları da gövdeden düşer (`omitempty` karşılığı): sunucu
 *  varsayılanı basacaksa formun boş stringi onu ezmemeli. */
export function sourceForSave(
  src: OracleSource,
  snapshot?: OracleSourceSnapshot | null,
): OracleSource {
  const dsn = trim(src.dsn);
  const out: OracleSource = {
    name: trim(src.name),
    user: trim(src.user),
    schema: trim(src.schema),
    table: trim(src.table),
    enabled: !!src.enabled,
  };

  // id sunucu sahipli: formdaki boşsa snapshot'tan taşınır (yeniden
  // adlandırma kaydı KOPARMASIN).
  const id = trim(src.id) || trim(snapshot?.id);
  if (id) out.id = id;

  if (dsn) {
    out.dsn = dsn;
    // host/port/serviceName kasten DÜŞÜRÜLÜR: sunucu ikisini birden
    // reddediyor ve dsn kipinde bu kutular ekranda da yok.
  } else {
    const host = trim(src.host);
    const serviceName = trim(src.serviceName);
    if (host) out.host = host;
    if (serviceName) out.serviceName = serviceName;
    if (Number.isFinite(src.port) && (src.port ?? 0) > 0) out.port = src.port;
  }

  const ref = trim(src.passwordRef);
  if (ref) out.passwordRef = ref;
  const pw = trim(src.password);
  if (pw) out.password = pw; // boşsa anahtar HİÇ YOK — saklı değer korunur

  const ts = trim(src.timestampColumn);
  if (ts) out.timestampColumn = ts;
  const tc = trim(src.typeColumn);
  if (tc) out.typeColumn = tc;
  const where = trim(src.extraWhere);
  if (where) out.extraWhere = where;

  const types: string[] = [];
  for (const t of src.typeFilter ?? []) {
    const v = t.trim();
    if (v && !types.includes(v)) types.push(v);
  }
  if (types.length) out.typeFilter = types;

  if (Number.isFinite(src.maxOpenConns) && (src.maxOpenConns ?? 0) > 0) out.maxOpenConns = src.maxOpenConns;
  if (Number.isFinite(src.queryTimeoutSec) && (src.queryTimeoutSec ?? 0) > 0) out.queryTimeoutSec = src.queryTimeoutSec;
  if (Number.isFinite(src.intervalSec) && (src.intervalSec ?? 0) > 0) out.intervalSec = src.intervalSec;

  // v0.10.603 — dilim boşsa gövdeye girmez (sunucu varsayılanı); dilimli
  // bayrağı yalnız true iken; kolonlar: boş kutu = varsayılan (anahtar yok),
  // `-` = alan kapalı ("" gider), diğeri kırpılmış kolon adı.
  const tz = trim(src.timezone);
  if (tz) out.timezone = tz;
  if (src.timestampHasZone) out.timestampHasZone = true;
  const cols: Record<string, string> = {};
  for (const [field, raw] of Object.entries(src.columns ?? {})) {
    const v = trim(raw);
    if (!v) continue;
    cols[field] = v === ORACLE_COLUMN_DISABLED ? '' : v;
  }
  if (Object.keys(cols).length) out.columns = cols;

  return out;
}

/** Sunucu snapshot'ı → düzenlenebilir form satırı. Şifre kutusu DAİMA boş
 *  açılır: GET onu geri vermiyor, dolayısıyla formda gösterilebilecek bir
 *  değer yok — boşluk "bilmiyorum"un dürüst gösterimi. */
export function sourceFromSnapshot(s: OracleSourceSnapshot): OracleSource {
  return {
    id: s.id ?? '',
    name: s.name ?? '',
    dsn: s.dsn ?? '',
    host: s.host ?? '',
    port: s.port,
    serviceName: s.serviceName ?? '',
    user: s.user ?? '',
    password: '',
    passwordRef: s.passwordRef ?? '',
    schema: s.schema ?? '',
    table: s.table ?? '',
    timestampColumn: s.timestampColumn ?? '',
    typeColumn: s.typeColumn ?? '',
    extraWhere: s.extraWhere ?? '',
    typeFilter: [...(s.typeFilter ?? [])],
    maxOpenConns: s.maxOpenConns,
    queryTimeoutSec: s.queryTimeoutSec,
    intervalSec: s.intervalSec,
    timezone: s.timezone ?? '',
    timestampHasZone: !!s.timestampHasZone,
    // "" (kapalı alan) formda `-` olarak görünür — boş kutuyla (varsayılan)
    // karışmasın.
    columns: Object.fromEntries(Object.entries(s.columns ?? {}).map(([k, v]) => [k, v === '' ? ORACLE_COLUMN_DISABLED : v])),
    enabled: !!s.enabled,
  };
}
