// v0.10.420 — dilim 1 inceleme düzeltmeleri kaynak pinleri.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const read = (p: string) => readFileSync(resolve(__dirname, p), 'utf8');

describe('log-search slice 1 review fixes (v0.10.420)', () => {
  const src = read('./Logs.tsx');
  it('doc permalink: degraded ≠ miss', () => {
    expect(src).toContain("setDocMiss('degraded')");
    expect(src).not.toContain('setDocMiss(true)');
  });
  it('boş durum kapısı YÜKLENEN satırlar (narrow süzgeci değil)', () => {
    expect(src).toContain('data && loadedRows.length === 0 && !live && !!staticQ.data?.degraded');
    expect(src).toContain('data && loadedRows.length === 0 && (live || !staticQ.data?.degraded)');
  });
  it('canlı kuyruk bekleme boş durumu', () => {
    expect(src).toContain('title="Canlı kuyruk açık — yeni satır bekleniyor"');
  });
  it('statik sorgu canlıyken kapalı', () => {
    expect(src).toContain('{ enabled: !live }');
    expect(read('../lib/queries/logs.ts')).toContain('enabled: opts?.enabled ?? true');
  });
  it('wrap düğmesi adı sabit, durum aria-pressed', () => {
    expect(src).not.toContain("'⤶ sarılı'");
  });
});
