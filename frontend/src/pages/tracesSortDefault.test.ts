import { describe, it, expect, beforeEach, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// v0.10.669 — operatör (prod): "Traces sayfası start time desc sıralı olsun".
// Kök neden: sıralama önceliği URL > localStorage > initialSort; bir kez
// başlığa tıklanınca tercih localStorage'a yazılıyor ve her yeni ziyaret onu
// açıyordu. persistSort:false ile localStorage basamağı atlanır (URL yine
// kazanır).
//
// Bu test ortamında gerçek localStorage YOK (aggSort.test.ts notu: kısmî
// stub / opak origin, yazma sessizce düşer) — storage modülü bellek içi bir
// haritayla mock'lanır; DataTable.tsx aynı modülü kullandığı için okuma
// yolu birebir sınanır.
const mem = new Map<string, string>();
vi.mock('@/lib/storage', async (importOriginal) => {
  const orig = await importOriginal<typeof import('@/lib/storage')>();
  return {
    ...orig,
    getRaw: (k: string) => mem.get(k) ?? null,
    setRaw: (k: string, v: string) => { mem.set(k, v); },
    removeRaw: (k: string) => { mem.delete(k); },
    getItem: <T,>(k: string, fb: T): T => {
      const r = mem.get(k);
      if (r == null) return fb;
      try { return JSON.parse(r) as T; } catch { return fb; }
    },
    setItem: <T,>(k: string, v: T) => { mem.set(k, JSON.stringify(v)); },
  };
});

import { resolveInitialSort } from '@/components/ui/DataTable';
import { setItem, dtSortKey } from '@/lib/storage';

const KEY = 'traces-list-test';
const TIME = { id: 'time', dir: 'desc' as const };

describe('resolveInitialSort persist=false (v0.10.669)', () => {
  beforeEach(() => mem.clear());

  it('localStorage daki eski tıklama persist=true ile kazanır, persist=false ile yok sayılır', () => {
    setItem(dtSortKey(KEY), { id: 'duration', dir: 'desc' });
    expect(resolveInitialSort(KEY, null, TIME, null, true)).toEqual({ id: 'duration', dir: 'desc' });
    expect(resolveInitialSort(KEY, null, TIME, null, false)).toEqual(TIME);
  });

  it('URL s_ parametresi persist=false ile de kazanır (paylaşılan link)', () => {
    setItem(dtSortKey(KEY), { id: 'duration', dir: 'desc' });
    expect(resolveInitialSort(KEY, 'spans.asc', TIME, null, false)).toEqual({ id: 'spans', dir: 'asc' });
  });

  it('varsayılan persist=true (imza geriye uyumlu: diğer tablolar aynen)', () => {
    setItem(dtSortKey(KEY), { id: 'status', dir: 'asc' });
    expect(resolveInitialSort(KEY, null, TIME)).toEqual({ id: 'status', dir: 'asc' });
  });
});

describe('BAĞLANMA (Traces.tsx)', () => {
  const src = readFileSync(resolve(__dirname, 'Traces.tsx'), 'utf8');
  it('liste tablosu persistSort:false ile kurulur; varsayılan time/desc', () => {
    const i = src.indexOf("storageKey: 'traces-list'");
    expect(i).toBeGreaterThan(-1);
    const block = src.slice(i, i + 900);
    expect(block).toContain('persistSort: false');
    expect(block).toContain('initialSort: { id: sort, dir: order }');
    expect(src).toContain("(searchParams.get('sort') as SortColumn) || 'time'");
    expect(src).toContain("searchParams.get('order') === 'asc' ? 'asc' : 'desc'");
  });
  it('useDataTable persistSort:false localStorage a yazmaz', () => {
    const dt = readFileSync(resolve(__dirname, '../components/ui/DataTable/DataTable.tsx'), 'utf8');
    expect(dt).toContain('if (persistSort) setItem(sortLSKey, sort);');
  });
});
