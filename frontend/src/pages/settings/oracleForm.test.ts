import { describe, expect, it } from 'vitest';
import {
  emptyOracleSource, validateOracleSource, sourceForSave, sourceFromSnapshot,
  parseTypeFilter, typeFilterToText, defaultTypeFilter,
  ORACLE_DEFAULT_PORT, ORACLE_MAX_EXTRA_WHERE,
  ORACLE_COLUMN_DISABLED, ORACLE_MAPPING_FIELDS,
} from './oracleForm';
import type { OracleSource, OracleSourceSnapshot } from '@/lib/types';

// v0.10.580 — OracleTab'ın SAF çekirdeği. Üç sessiz sınıf burada çivileniyor:
//   1. Boş şifre kutusunun saklı şifreyi EZMEMESİ (gövdede anahtar YOK).
//   2. `extraWhere` enjeksiyon kalıplarının (`;` `--` `/*`) reddi — geçen bir
//      `--` sorgunun zaman yüklemini ve satır tavanını yorum yapar, yani
//      "biraz yanlış sonuç" değil, banka veritabanında SINIRSIZ TARAMA.
//   3. Identifier kapısı: şema/tablo/kolon adları SQL'e tırnaksız girer,
//      dolayısıyla bu regex'in geçirdiği her şey enjeksiyon-güvenli OLMAK
//      ZORUNDA.

/** Kaydedilebilir, etkin bir kaynak — testler bunun ÜSTÜNE tek alan bozar. */
function goodSource(over: Partial<OracleSource> = {}): OracleSource {
  return {
    ...emptyOracleSource(),
    name: 'core-bank',
    host: 'oradb.internal',
    serviceName: 'ORCLPDB1',
    user: 'coremetry_ro',
    password: 'gizli',
    schema: 'BANKAPP',
    table: 'MCA_TERROR_LOG',
    enabled: true,
    ...over,
  };
}

describe('emptyOracleSource — varsayılanlar', () => {
  const s = emptyOracleSource();
  it('port 1521, tip süzgeci ["T"], havuz 4, timeout 20 sn, aralık 60 sn', () => {
    expect(s.port).toBe(ORACLE_DEFAULT_PORT);
    expect(s.port).toBe(1521);
    expect(s.typeFilter).toEqual(['T']);
    expect(s.maxOpenConns).toBe(4);
    expect(s.queryTimeoutSec).toBe(20);
    expect(s.intervalSec).toBe(60);
  });
  it('enabled false — yarım taslak ilk anda kırmızıya boyanmaz', () => {
    expect(s.enabled).toBe(false);
    // ...ve kapalı taslak ZORUNLULUK hatası da üretmez.
    expect(validateOracleSource(s)).toEqual({ name: 'Ad zorunlu.' });
  });
  it('varsayılan tip süzgeci PAYLAŞILAN dizi değil (çağıran bozamaz)', () => {
    const a = defaultTypeFilter();
    a.push('X');
    expect(defaultTypeFilter()).toEqual(['T']);
    expect(emptyOracleSource().typeFilter).toEqual(['T']);
  });
});

describe('validateOracleSource — ad', () => {
  it('boş ad reddedilir', () => {
    expect(validateOracleSource(goodSource({ name: '' })).name).toBe('Ad zorunlu.');
  });
  it.each([' boşluk lu', 'çğü', '_altçizgiyle-başlar', 'a'.repeat(65)])(
    'biçimsiz ad reddedilir: %j', bad => {
      expect(validateOracleSource(goodSource({ name: bad })).name).toMatch(/harf ya da rakamla başlamalı/);
    });
  it.each(['core-bank', 'db1', 'oracle.prod_2'])('geçerli ad kabul: %j', ok => {
    expect(validateOracleSource(goodSource({ name: ok })).name).toBeUndefined();
  });
  it('ad tekil olmalı — büyük/küçük harf farkı korumaz', () => {
    const others = [goodSource({ name: 'CORE-BANK' })];
    expect(validateOracleSource(goodSource({ name: 'core-bank' }), { others }).name)
      .toMatch(/tekil olmalı/);
    expect(validateOracleSource(goodSource({ name: 'core-bank2' }), { others }).name).toBeUndefined();
  });
});

describe('validateOracleSource — bağlantı: dsn XOR host/serviceName', () => {
  it('ikisi birden verilemez', () => {
    expect(validateOracleSource(goodSource({ dsn: 'oracle://u:p@h:1521/s' })).dsn)
      .toMatch(/ikisi birden/);
  });
  it('tek parça dsn tek başına yeter — host/servis/kullanıcı aranmaz', () => {
    const e = validateOracleSource(goodSource({
      dsn: 'oracle://u:p@h:1521/s', host: '', serviceName: '', user: '', password: '',
    }));
    expect(e).toEqual({});
  });
  it('dsn şeması `oracle://` olmalı', () => {
    const e = validateOracleSource(goodSource({ dsn: 'jdbc:oracle:thin:@h:1521/s', host: '', serviceName: '' }));
    expect(e.dsn).toMatch(/oracle:\/\//);
  });
  it('dsn YOKSA etkin kaynakta host + serviceName + kullanıcı zorunlu', () => {
    const e = validateOracleSource(goodSource({ host: '', serviceName: '', user: '' }));
    expect(e.host).toMatch(/zorunlu/);
    expect(e.serviceName).toMatch(/zorunlu/);
    expect(e.user).toMatch(/zorunlu/);
  });
  it('KAPALI taslakta zorunluluk aranmaz, ama BİÇİM aranır', () => {
    const draft = goodSource({ enabled: false, host: '', serviceName: '', user: '', password: '', schema: '' });
    expect(validateOracleSource(draft)).toEqual({});
    // Biçim kuralı kapalı taslakta da koşar: yarın etkinleşecek.
    expect(validateOracleSource(goodSource({ enabled: false, schema: 'BAD NAME' })).schema)
      .toMatch(/identifier/);
  });
  it('requireComplete:true kapalı taslağı da "Bağlantıyı dene" gibi ölçer', () => {
    const draft = goodSource({ enabled: false, host: '', serviceName: '' });
    expect(validateOracleSource(draft)).toEqual({});
    expect(validateOracleSource(draft, { requireComplete: true }).host).toMatch(/zorunlu/);
  });
  it.each([0, 70000, -1, 1.5])('port aralık dışı reddedilir: %j', bad => {
    const e = validateOracleSource(goodSource({ port: bad }));
    if (bad === 0) expect(e.port).toBeUndefined(); // 0 = sunucu varsayılanı
    else expect(e.port).toMatch(/1-65535/);
  });
});

describe('validateOracleSource — şifre ve referans', () => {
  it('etkin kaynakta şifre de referans da yoksa hata', () => {
    expect(validateOracleSource(goodSource({ password: '' })).password).toMatch(/Şifre zorunlu/);
  });
  it('SAKLI şifre varsa boş kutu hata DEĞİL (dokunmadım demek)', () => {
    expect(validateOracleSource(goodSource({ password: '' }), { hasStoredPassword: true }).password)
      .toBeUndefined();
  });
  it('passwordRef şifre yerine geçer', () => {
    expect(validateOracleSource(goodSource({ password: '', passwordRef: 'env:ORA_PW' })).password)
      .toBeUndefined();
  });
  it.each(['env:ORA_PW', 'file:/var/run/secrets/oracle/pw'])('geçerli referans: %j', ref => {
    expect(validateOracleSource(goodSource({ passwordRef: ref })).passwordRef).toBeUndefined();
  });
  it.each(['düz-şifre', 'env:1BAD', 'file:göreli/yol', 'vault:x'])(
    'geçersiz referans reddedilir: %j', ref => {
      expect(validateOracleSource(goodSource({ passwordRef: ref })).passwordRef)
        .toMatch(/env:AD.*file:/);
    });
});

describe('validateOracleSource — identifier kapısı (SQL enjeksiyonu)', () => {
  it.each(['schema', 'table', 'timestampColumn', 'typeColumn'] as const)(
    '%s alanı Oracle identifier regex\'inden geçer', field => {
      for (const bad of ['1BASLAR', 'AD SOYAD', 'AD;DROP', 'AD-TIRE', "AD'TIRNAK", 'A'.repeat(31), 'AD--X']) {
        const e = validateOracleSource(goodSource({ [field]: bad } as Partial<OracleSource>));
        expect(e[field], `${field}=${bad} geçmemeliydi`).toMatch(/identifier/);
      }
      for (const ok of ['BANKAPP', 'MCA_TERROR_LOG', 'A$B#C', 'a'.repeat(30)]) {
        const e = validateOracleSource(goodSource({ [field]: ok } as Partial<OracleSource>));
        expect(e[field], `${field}=${ok} geçmeliydi`).toBeUndefined();
      }
    });
  it('etkin kaynakta şema ve tablo zorunlu; kolonlar boş bırakılabilir (varsayılan)', () => {
    const e = validateOracleSource(goodSource({ schema: '', table: '', timestampColumn: '', typeColumn: '' }));
    expect(e.schema).toMatch(/zorunlu/);
    expect(e.table).toMatch(/zorunlu/);
    expect(e.timestampColumn).toBeUndefined();
    expect(e.typeColumn).toBeUndefined();
  });
});

describe('validateOracleSource — extraWhere', () => {
  it.each([
    ['MCA_ERR_CODE = 1; DROP TABLE X', ';'],
    ["MCA_ERR_CODE = 'A' -- gerisini sustur", '--'],
    ['MCA_ERR_CODE = 1 /* yorum', '/*'],
    ["1=1 --", '--'],
  ])('enjeksiyon kalıbı reddedilir: %j', (where, token) => {
    const e = validateOracleSource(goodSource({ extraWhere: where }));
    expect(e.extraWhere, `${where} geçmemeliydi`).toContain(token);
  });
  it('meşru ek koşul geçer', () => {
    expect(validateOracleSource(goodSource({ extraWhere: "MCA_ERR_CODE NOT IN ('ERR_020')" })).extraWhere)
      .toBeUndefined();
  });
  it(`en çok ${ORACLE_MAX_EXTRA_WHERE} karakter`, () => {
    expect(validateOracleSource(goodSource({ extraWhere: 'A'.repeat(ORACLE_MAX_EXTRA_WHERE) })).extraWhere)
      .toBeUndefined();
    expect(validateOracleSource(goodSource({ extraWhere: 'A'.repeat(ORACLE_MAX_EXTRA_WHERE + 1) })).extraWhere)
      .toMatch(/en çok 500 karakter/);
  });
});

describe('validateOracleSource — sayısal kelepçeler', () => {
  it.each([
    ['maxOpenConns', 0, undefined], ['maxOpenConns', 1, undefined], ['maxOpenConns', 16, undefined],
    ['maxOpenConns', 17, /1-16/], ['maxOpenConns', -2, /1-16/],
    ['queryTimeoutSec', 0, undefined], ['queryTimeoutSec', 5, undefined], ['queryTimeoutSec', 120, undefined],
    ['queryTimeoutSec', 4, /5-120/], ['queryTimeoutSec', 121, /5-120/],
    ['intervalSec', 0, undefined], ['intervalSec', 10, undefined], ['intervalSec', 3600, undefined],
    ['intervalSec', 9, /10-3600/], ['intervalSec', 3601, /10-3600/],
  ] as const)('%s=%j', (field, value, want) => {
    const e = validateOracleSource(goodSource({ [field]: value } as Partial<OracleSource>));
    if (want === undefined) expect(e[field]).toBeUndefined();
    else expect(e[field]).toMatch(want);
  });
  it('kesirli değer sessizce yuvarlanmaz, HATA olur', () => {
    expect(validateOracleSource(goodSource({ maxOpenConns: 4.5 })).maxOpenConns).toMatch(/tam sayı/);
  });
  it('tip süzgeci sınırları', () => {
    expect(validateOracleSource(goodSource({ typeFilter: Array.from({ length: 17 }, (_, i) => `T${i}`) })).typeFilter)
      .toMatch(/En çok 16/);
    expect(validateOracleSource(goodSource({ typeFilter: ['A'.repeat(33)] })).typeFilter)
      .toMatch(/en çok 32 karakter/);
    expect(validateOracleSource(goodSource({ typeFilter: ['T', 'E'] })).typeFilter).toBeUndefined();
  });
});

describe('sourceForSave — boş şifre SAKLIYI KORUR', () => {
  it('kullanıcı şifreye dokunmadıysa `password` anahtarı gövdede HİÇ YOK', () => {
    const snap: OracleSourceSnapshot = {
      ...goodSource({ password: '' }), id: 'o-1234abcd',
      hasPassword: true, passwordResolved: true,
    };
    const body = sourceForSave(sourceFromSnapshot(snap), snap);
    expect('password' in body).toBe(false);
    expect(JSON.stringify(body)).not.toContain('password');
    // Kimlik snapshot'tan taşınır: yeniden adlandırma kaydı koparmasın.
    expect(body.id).toBe('o-1234abcd');
  });
  it('yalnız BOŞLUK yazmak da "dokunmadım" sayılır', () => {
    const body = sourceForSave(goodSource({ password: '   ' }));
    expect('password' in body).toBe(false);
  });
  it('yeni şifre yazıldıysa kırpılarak gider', () => {
    const body = sourceForSave(goodSource({ password: '  yeni-sifre  ' }));
    expect(body.password).toBe('yeni-sifre');
  });
  it('sourceFromSnapshot şifre kutusunu DAİMA boş açar', () => {
    const snap: OracleSourceSnapshot = {
      ...goodSource(), id: 'o-1', hasPassword: true, passwordResolved: true,
    };
    expect(sourceFromSnapshot(snap).password).toBe('');
  });
});

describe('sourceForSave — gövde hijyeni', () => {
  it('boş metin alanları gövdeye GİRMEZ (sunucu varsayılanı ezilmesin)', () => {
    const body = sourceForSave(emptyOracleSource());
    for (const k of ['dsn', 'host', 'serviceName', 'passwordRef', 'timestampColumn', 'typeColumn', 'extraWhere']) {
      expect(k in body, `${k} boşken gövdeye girmemeli`).toBe(false);
    }
    // name/user/schema/table/enabled sözleşme gereği DAİMA var (zorunlu alanlar).
    expect(body).toMatchObject({ name: '', user: '', schema: '', table: '', enabled: false });
  });
  it('dsn kipinde host/port/serviceName DÜŞER (sunucu ikisini birden reddeder)', () => {
    const body = sourceForSave(goodSource({ dsn: 'oracle://u:p@h:1521/s' }));
    expect(body.dsn).toBe('oracle://u:p@h:1521/s');
    expect('host' in body).toBe(false);
    expect('port' in body).toBe(false);
    expect('serviceName' in body).toBe(false);
  });
  it('0 / tanımsız sayı gövdeye girmez (0 = sunucu varsayılanı)', () => {
    const body = sourceForSave(goodSource({ port: 0, maxOpenConns: 0, queryTimeoutSec: undefined, intervalSec: 0 }));
    for (const k of ['port', 'maxOpenConns', 'queryTimeoutSec', 'intervalSec']) {
      expect(k in body, `${k} 0/undefined iken gövdeye girmemeli`).toBe(false);
    }
  });
  it('tip süzgeci kırpılır, boşlar atılır, tekilleşir; hepsi boşsa alan düşer', () => {
    expect(sourceForSave(goodSource({ typeFilter: [' T ', 'E', 'T', ''] })).typeFilter).toEqual(['T', 'E']);
    expect('typeFilter' in sourceForSave(goodSource({ typeFilter: ['  ', ''] }))).toBe(false);
  });
  it('metin alanları kırpılır', () => {
    const body = sourceForSave(goodSource({ name: ' core ', schema: ' BANKAPP ', extraWhere: '  1=1  ' }));
    expect(body.name).toBe('core');
    expect(body.schema).toBe('BANKAPP');
    expect(body.extraWhere).toBe('1=1');
  });
});

describe('tip süzgeci metin çevirisi', () => {
  it('virgül/yeni satır ayırır, kırpar, tekilleştirir', () => {
    expect(parseTypeFilter('T, E\n T ,,')).toEqual(['T', 'E']);
    expect(parseTypeFilter('')).toEqual([]);
  });
  it('gidiş-dönüş', () => {
    expect(parseTypeFilter(typeFilterToText(['T', 'E']))).toEqual(['T', 'E']);
    expect(typeFilterToText(undefined)).toBe('');
  });
});

// v0.10.603 — Aşama 2 eşleme ayarları: dilim biçimi, kolon eşlemesi kapısı,
// `-` = "alan tabloda yok" (tel'de ""), boş kutu = varsayılan (anahtar YOK).
describe('v0.10.603 — zaman dilimi + kolon eşlemesi', () => {
  it('varsayılanlar: dilim boş (sunucu Europe/Istanbul), dilimsiz, kolon haritası boş', () => {
    const e = emptyOracleSource();
    expect(e.timezone).toBe('');
    expect(e.timestampHasZone).toBe(false);
    expect(e.columns).toEqual({});
  });
  it('ORACLE_MAPPING_FIELDS 14 alan; timestamp/type LİSTEDE DEĞİL (kendi kutuları var)', () => {
    expect(ORACLE_MAPPING_FIELDS.length).toBe(14);
    const fields = ORACLE_MAPPING_FIELDS.map(f => f.field);
    expect(fields).not.toContain('timestamp');
    expect(fields).not.toContain('type');
    expect(new Set(fields).size).toBe(14);
    expect(ORACLE_MAPPING_FIELDS.find(f => f.field === 'service')?.target).toBe('operation.code');
    for (const f of ORACLE_MAPPING_FIELDS) expect(f.column).toMatch(/^MCA_ERR_[A-Z_]+$/);
  });
  it('dilim biçimi: IANA adları geçer, serbest metin reddedilir', () => {
    for (const tz of ['Europe/Istanbul', 'UTC', 'Etc/GMT+3', 'America/Argentina/Buenos_Aires']) {
      expect(validateOracleSource(goodSource({ timezone: tz })).timezone, tz).toBeUndefined();
    }
    for (const tz of ['İstanbul saati', 'Europe Istanbul', 'Europe/İstanbul', '+03:00']) {
      expect(validateOracleSource(goodSource({ timezone: tz })).timezone, tz).toBeDefined();
    }
  });
  it('kolon eşlemesi identifier kapısından geçer; `-` ve boş serbest; bilinmeyen alan reddedilir', () => {
    expect(validateOracleSource(goodSource({ columns: { code: 'ERR_CODE', traceId: ORACLE_COLUMN_DISABLED, host: '' } })).columns).toBeUndefined();
    expect(validateOracleSource(goodSource({ columns: { code: '1BAD' } })).columns).toBeDefined();
    expect(validateOracleSource(goodSource({ columns: { code: 'A;B' } })).columns).toBeDefined();
    expect(validateOracleSource(goodSource({ columns: { nope: 'X' } })).columns).toBeDefined();
  });
  it('sourceForSave: boş dilim/dilimsiz/boş harita gövdeye girmez; `-` → "" (alan kapalı); boş kutu anahtar YOK', () => {
    const plain = sourceForSave(goodSource());
    expect('timezone' in plain).toBe(false);
    expect('timestampHasZone' in plain).toBe(false);
    expect('columns' in plain).toBe(false);
    const body = sourceForSave(goodSource({
      timezone: ' UTC ', timestampHasZone: true,
      columns: { code: ' ERR_CODE ', tellerId: ORACLE_COLUMN_DISABLED, host: '   ' },
    }));
    expect(body.timezone).toBe('UTC');
    expect(body.timestampHasZone).toBe(true);
    expect(body.columns).toEqual({ code: 'ERR_CODE', tellerId: '' });
    expect('host' in (body.columns ?? {})).toBe(false);
  });
  it('sourceFromSnapshot: "" (kapalı alan) formda `-`; dilim ve dilimli bayrağı taşınır', () => {
    const snap: OracleSourceSnapshot = {
      ...goodSource(), id: 'o-1', hasPassword: true, passwordResolved: true,
      timezone: 'UTC', timestampHasZone: true, columns: { tellerId: '', code: 'ERR_CODE' },
    };
    const form = sourceFromSnapshot(snap);
    expect(form.timezone).toBe('UTC');
    expect(form.timestampHasZone).toBe(true);
    expect(form.columns).toEqual({ tellerId: ORACLE_COLUMN_DISABLED, code: 'ERR_CODE' });
    // gidiş-dönüş: form → gövde aynı tel değerini üretir
    expect(sourceForSave(form).columns).toEqual({ tellerId: '', code: 'ERR_CODE' });
  });
});
