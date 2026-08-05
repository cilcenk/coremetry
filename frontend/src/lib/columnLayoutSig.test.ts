import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { columnLayoutSig, readPersistedWidths, type DataTableColumn } from './dataTable';

// v0.9.695 — KALICI GENİŞLİK, KOLON TANIMINA MÜHÜRLÜ.
//
// Operatör-bildirimi: "Manage users sayfasında user tablosunun kolonları,
// ilk header kullanıcının üzerinde çıkıyor."
//
// v0.9.660 Users kolonlarını yeniden tanımlamıştı (team: flex → sabit
// 145). Ama `dt.users.widths` SÜRÜMSÜZDÜ ve DataTableColgroup kalıcı
// genişliği tanımın ÖNÜNE koyuyor:
//
//     const w = dt.colWidths[c.id] ?? (c.flex ? 'auto' : c.width ?? …)
//
// Kolonları bir kez sürüklemiş bir tarayıcıda düzeltme HİÇ UYGULANMADI.
// Bu sınıfın sinsiliği: `git log` düzeltmeyi gemide gösteriyor, kod
// doğru, yalnız ekran yanlış — kimse localStorage'dan şüphelenmiyor.

type Row = Record<string, never>;

// Users'ın v0.9.660 ÖNCESİ ve SONRASI kolon niyeti. Aralarındaki tek
// fark team'in emici→sabit dönüşü; id kümesi AYNI. Testin can alıcı
// noktası bu: id'lere bakan bir imza bu değişikliği KAÇIRIRDI.
const BEFORE: DataTableColumn<Row>[] = [
  { id: 'email', label: 'Email', flex: true, minWidth: 220 },
  { id: 'role', label: 'Role', width: 115 },
  { id: 'team', label: 'Team', flex: true },
];
const AFTER: DataTableColumn<Row>[] = [
  { id: 'email', label: 'Email', flex: true, minWidth: 220 },
  { id: 'role', label: 'Role', width: 115 },
  { id: 'team', label: 'Team', width: 145 },
];

describe('columnLayoutSig', () => {
  it('emici→sabit dönüşünü YAKALAR (operatörün tam vakası)', () => {
    expect(columnLayoutSig(BEFORE)).not.toBe(columnLayoutSig(AFTER));
  });

  it('aynı tanım için kararlı', () => {
    expect(columnLayoutSig(AFTER)).toBe(columnLayoutSig([...AFTER]));
  });

  it('beyan edilen genişliğin DEĞİŞMESİ imzayı değiştirir', () => {
    const wider = AFTER.map(c => (c.id === 'role' ? { ...c, width: 150 } : c));
    expect(columnLayoutSig(wider)).not.toBe(columnLayoutSig(AFTER));
  });

  it('kolon eklenmesi/çıkması imzayı değiştirir', () => {
    expect(columnLayoutSig(AFTER.slice(0, 2))).not.toBe(columnLayoutSig(AFTER));
  });

  // label SIRALAMA/GÖRÜNÜM meselesi, DÜZEN değil — genişliği etkilemeyen
  // bir düzenleme operatörün sürüklemesini çöpe atmamalı.
  it('yalnız label değişince imza KORUNUR', () => {
    const relabelled = AFTER.map(c => ({ ...c, label: c.label + ' ' }));
    expect(columnLayoutSig(relabelled)).toBe(columnLayoutSig(AFTER));
  });
});

describe('readPersistedWidths', () => {
  const sig = columnLayoutSig(AFTER);

  it('imza uyuşunca genişlikleri GERİ VERİR', () => {
    expect(readPersistedWidths({ sig, widths: { email: 400 } }, sig)).toEqual({ email: 400 });
  });

  it('imza uyuşmayınca ATAR — bayat harita yeni tanımı ezemez', () => {
    const stale = { sig: columnLayoutSig(BEFORE), widths: { email: 900, team: 300 } };
    expect(readPersistedWidths(stale, sig)).toEqual({});
  });

  // ESKİ ŞEKİL (çıplak Record) bilinçli atılıyor: onu geçersiz kılmak
  // bu değişikliğin AMACI. Taşımak operatörün bozuk düzenini korurdu.
  it('sürümsüz eski şekli ATAR', () => {
    expect(readPersistedWidths({ email: 900, team: 300 }, sig)).toEqual({});
  });

  it('bozuk/eksik girdide çökmez', () => {
    expect(readPersistedWidths(null, sig)).toEqual({});
    expect(readPersistedWidths('nope', sig)).toEqual({});
    expect(readPersistedWidths({ sig }, sig)).toEqual({});
  });
});

// ÇAĞRI YERİ KAPISI — yukarıdaki saf testlerin HEPSİ, useDataTable
// yardımcıyı hiç çağırmasa bile geçer.
//
// Bu boş bir korku değil, bu dosyanın düzelttiği hatanın ta kendisi:
// v0.9.660'ta `resetLayout` useDataTable'dan döndürülüyordu ve HİÇBİR
// sayfa onu bağlamamıştı — yazılmış, bağlanmamış, ölü. Aynı gün beş
// test daha yapıcıyı test edip çağrı yerini kaçırdığı için mutasyondan
// geçti. Kapı, okumanın ve yazmanın gerçekten imzadan geçtiğini çiviler.
describe('useDataTable çağrı yeri', () => {
  const src = readFileSync(resolve(__dirname, '../components/DataTable.tsx'), 'utf8');
  const code = src.split('\n').map(l => {
    const i = l.indexOf('//');
    return i >= 0 ? l.slice(0, i) : l;
  }).join('\n');

  it('OKUMA readPersistedWidths üzerinden', () => {
    expect(code).toContain('readPersistedWidths(');
  });

  it('YAZMA imzayı da kaydediyor', () => {
    expect(code).toMatch(/setItem\(widthLSKey,\s*\{\s*sig:/);
  });

  it('ham getItem(widthLSKey, {}) yolu KALMADI', () => {
    expect(code).not.toMatch(/getItem\(widthLSKey,\s*\{\}\)/);
  });
});
