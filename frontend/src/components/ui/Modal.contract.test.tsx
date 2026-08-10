// @vitest-environment jsdom
//
// Modal.contract.test.tsx — v0.9.924 (tutarlılık denetimi Dalga 6, mK2).
//
// `Modal`ın DEPODA HİÇ TESTİ YOKTU — 17 dosya / 23 örnek onun üzerinde
// duruyor. Ve `aria-modal="true"` bugüne kadar YALAN söylüyordu: rol
// "arkadaki her şey inert" diye duyuruyordu ama Tab diyaloğun son
// öğesinden çıkıp altındaki sayfaya geçiyordu. Odak görünmez bir yere
// kayınca kullanıcı hangi öğede olduğunu kaybediyor ve diyaloğa dönüş
// yolu kalmıyor.
//
// Kaynak taraması burada YETMEZ: hapsin doğru çalıştığı ancak gerçek
// odak hareketiyle görülür. Bu yüzden jsdom render'ı.
import { describe, it, expect, afterEach } from 'vitest';
import { createRoot, type Root } from 'react-dom/client';
import { act } from 'react';
import { Modal } from './Modal';

let root: Root | null = null;
let host: HTMLDivElement | null = null;

function render(ui: React.ReactElement) {
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => { root!.render(ui); });
}

afterEach(() => {
  act(() => { root?.unmount(); });
  host?.remove();
  root = null; host = null;
  document.body.innerHTML = '';
});

function tab(shift = false) {
  document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: shift, bubbles: true }));
}

describe('Modal — Tab hapsi', () => {
  it('son öğeden Tab başa DÖNER (sayfaya kaçmaz)', () => {
    render(
      <Modal open onClose={() => {}} title="T">
        <button id="a">A</button>
        <button id="b">B</button>
      </Modal>,
    );
    const b = document.getElementById('b') as HTMLElement;
    b.focus();
    act(() => { tab(); });
    // İlk odaklanabilir öğe kapatma butonu (başlık satırında).
    expect(document.activeElement?.getAttribute('aria-label')).toBe('Close dialog');
  });

  it('ilk öğeden Shift+Tab sona DÖNER', () => {
    render(
      <Modal open onClose={() => {}} title="T">
        <button id="a">A</button>
        <button id="b">B</button>
      </Modal>,
    );
    const close = document.querySelector<HTMLElement>('[aria-label="Close dialog"]')!;
    close.focus();
    act(() => { tab(true); });
    expect(document.activeElement?.id).toBe('b');
  });

  it('odaklanabilir liste HER Tab\'da yeniden toplanıyor', () => {
    // R3: liste bir kez toplanıp saklanırsa, koşullu render'lı formlarda
    // (bir seçim yeni alan açıyor, bir hata satırı beliriyor) bayat liste
    // üzerinden sarılır ve odak ARTIK VAR OLMAYAN bir öğeye gönderilir.
    // Burada modal açıldıktan SONRA yeni bir buton ekleniyor; hapis onu
    // görmek zorunda.
    render(
      <Modal open onClose={() => {}} title="T">
        <button id="a">A</button>
      </Modal>,
    );
    const body = document.querySelector('.modal-body')!;
    const late = document.createElement('button');
    late.id = 'late';
    body.appendChild(late);
    late.focus();
    act(() => { tab(); });
    expect(document.activeElement?.getAttribute('aria-label')).toBe('Close dialog');
  });

  it('disabled öğeler hapse dahil DEĞİL', () => {
    render(
      <Modal open onClose={() => {}}>
        <button id="a">A</button>
        <button id="z" disabled>Z</button>
      </Modal>,
    );
    const a = document.getElementById('a') as HTMLElement;
    a.focus();
    act(() => { tab(); });
    // 'z' disabled olduğu için 'a' hem ilk hem son; Tab kendine döner.
    expect(document.activeElement?.id).toBe('a');
  });

  it('aria-modal ve role hâlâ beyan ediliyor', () => {
    render(<Modal open onClose={() => {}}>x</Modal>);
    const d = document.querySelector('.modal-dialog')!;
    expect(d.getAttribute('role')).toBe('dialog');
    expect(d.getAttribute('aria-modal')).toBe('true');
  });
});
