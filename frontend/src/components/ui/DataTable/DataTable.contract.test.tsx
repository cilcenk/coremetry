// @vitest-environment jsdom
//
// DataTable.contract.test.tsx — v0.10.249 (DataTable dilim 4, audit §12
// test stratejisi 2). jsdom sözleşme testleri, @testing-library YOK
// (Button.contract.test.tsx emsali). Kapsam: DOM sözleşmesi, görünüm değil.
//   • sıralanabilir başlık odaklanır (tabIndex=0) + aria-sort; Enter sıralar;
//     Shift+→ genişliği 8 px artırır (resizeBy)
//   • columnModel görünür sırayı/gizliyi uygular, allColumns tam kalır,
//     genişlik imzası bildirilen kolonlardan (gizleme genişliği sıfırlamaz)
//   • VirtualTable aria-rowcount basar; boş satır colSpan görünür kolon sayısı
//   • selection: toggle/range/all/clear id ile
import { describe, it, expect, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import type { ReactNode } from 'react';
import { MemoryRouter } from 'react-router-dom';
import { useDataTable, DataTableHead, DataTableColgroup, VirtualTable, type DataTable, type ColumnDef, type ColumnModel } from './index';

let host: HTMLDivElement | null = null;
let root: Root | null = null;
function render(node: ReactNode): HTMLElement {
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => { root!.render(<MemoryRouter>{node}</MemoryRouter>); });
  return host;
}
afterEach(() => {
  act(() => { root?.unmount(); });
  host?.remove();
  root = null; host = null;
});

interface Row { id: string; name: string; n: number }
const ROWS: Row[] = [{ id: 'a', name: 'alpha', n: 3 }, { id: 'b', name: 'beta', n: 1 }, { id: 'c', name: 'gamma', n: 2 }];
const COLS: ColumnDef<Row>[] = [
  { id: 'name', label: 'Name', sortValue: r => r.name, width: 120, hideable: false },
  { id: 'n', label: 'N', sortValue: r => r.n, numeric: true, width: 80 },
  { id: 'plain', label: 'Plain', width: 60 },
];

let last: DataTable<Row> | null = null;
function Probe({ model, selection }: { model?: ColumnModel | null; selection?: boolean }) {
  const dt = useDataTable<Row>({
    storageKey: 'contract-test', columns: COLS, rows: ROWS,
    columnModel: model !== undefined ? { value: model } : undefined,
    selection: selection ? { mode: 'multi', getRowId: r => r.id } : undefined,
  });
  last = dt;
  return (
    <div className="table-wrap">
      <table>
        <DataTableColgroup dt={dt} />
        <DataTableHead dt={dt} />
        <tbody>{dt.sortedRows.map((r, i) => <tr key={r.id} {...dt.rowProps(i)}><td>{r.name}</td></tr>)}</tbody>
      </table>
    </div>
  );
}

describe('DataTableHead klavye sözleşmesi', () => {
  it('sıralanabilir th odaklanır, aria-sort taşır, Enter sıralar, Shift+→ 8 px', () => {
    const el = render(<Probe />);
    const ths = Array.from(el.querySelectorAll('th'));
    const nameTh = ths.find(t => t.textContent?.startsWith('Name'))!;
    const plainTh = ths.find(t => t.textContent?.startsWith('Plain'))!;
    expect(nameTh.tabIndex).toBe(0);
    expect(nameTh.getAttribute('aria-sort')).toBe('none');
    expect(plainTh.getAttribute('tabindex')).toBeNull();
    act(() => { nameTh.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })); });
    expect(['ascending', 'descending']).toContain(el.querySelector('th[aria-sort]:not([aria-sort="none"])')!.getAttribute('aria-sort'));
    const before = last!.colWidths['name'] ?? 120;
    act(() => { nameTh.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', shiftKey: true, bubbles: true })); });
    expect(last!.colWidths['name']).toBe(before + 8);
  });
});

describe('columnModel bağlaması', () => {
  it('sırayı/gizliyi uygular, allColumns tam kalır', () => {
    const model: ColumnModel = { v: 1, order: ['plain', 'name', 'n'], hidden: ['n'], sig: 'x' };
    render(<Probe model={model} />);
    expect(last!.columns.map(c => c.id)).toEqual(['plain', 'name']);
    expect(last!.allColumns.map(c => c.id)).toEqual(['name', 'n', 'plain']);
    expect(last!.visibleColumns.map(c => c.id)).toEqual(['plain', 'name']);
  });
  it('model yokken bugünkü davranış', () => {
    render(<Probe />);
    expect(last!.columns).toBe(COLS);
    expect(last!.selection).toBeNull();
    expect(last!.server).toBeNull();
  });
});

describe('selection bağlaması', () => {
  it('toggle/range/all/clear id anahtarlı', () => {
    render(<Probe selection />);
    const sel = () => last!.selection!;
    act(() => { sel().toggle(ROWS[0]); });
    expect([...sel().ids]).toEqual(['a']);
    act(() => { sel().range(ROWS[2]); });
    expect([...sel().ids].sort()).toEqual(['a', 'b', 'c']);
    act(() => { sel().clear(); });
    expect(sel().ids.size).toBe(0);
    act(() => { sel().all(); });
    expect(sel().ids.size).toBe(3);
    expect(sel().isSelected(ROWS[1])).toBe(true);
  });
});

function VProbe({ rows }: { rows: Row[] }) {
  const dt = useDataTable<Row>({ storageKey: 'contract-vt', columns: COLS, rows });
  return <VirtualTable<Row> dt={dt} height={200} renderRow={r => <td>{r.name}</td>} />;
}

describe('VirtualTable', () => {
  it('aria-rowcount basar; boş satır colSpan görünür kolon sayısı', () => {
    const el = render(<VProbe rows={ROWS} />);
    expect(el.querySelector('table')!.getAttribute('aria-rowcount')).toBe('3');
    const empty = render(<VProbe rows={[]} />);
    const td = empty.querySelector('td.vt-empty')!;
    expect(td.getAttribute('colspan')).toBe(String(COLS.length));
  });
});
