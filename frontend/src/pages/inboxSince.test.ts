// Inbox ?since= basamakları — UX denetimi F5 / Ö13 (v0.9.954).
//
// ORİJİNAL BELİRTİ: Inbox "ne oldu?" sorusunun doğal girişi ama zaman
// penceresi sorulamıyordu — en dar basamak 2 saatti. Bir olayın hemen
// ardından bakan operatör "şu 20 dakikada ortaya çıkanlar"ı kuramıyor,
// en dar seçenekte bile 2 saatlik gürültüyü birlikte alıyordu.
//
// ASIL RİSK İKİ TARAFIN AYRIŞMASI: istemci bir basamağı sunuyorsa
// sunucunun normalizeInboxSince'i de onu TANIMALI. Tanımazsa filtre
// seçili görünür, sunucu "" alır ve pencere sessizce "hepsi" olur —
// boş liste değil, YANLIŞ liste.

import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

const REPO = join(__dirname, '..', '..', '..');
const inbox = readFileSync(join(REPO, 'frontend/src/pages/Inbox.tsx'), 'utf8');
const server = readFileSync(join(REPO, 'internal/api/inbox.go'), 'utf8');

/** İstemcinin sunduğu basamaklar (boş = "hepsi", filtre değil). */
const clientRungs = Array.from(
  inbox.slice(inbox.indexOf('const SINCE_OPTS'), inbox.indexOf('] as const;', inbox.indexOf('const SINCE_OPTS')))
    .matchAll(/\{ v: '([^']*)',/g),
  m => m[1],
).filter(Boolean);

/** Sunucunun kabul ettiği basamaklar (normalizeInboxSince switch'i). */
const serverRungs = (() => {
  const i = server.indexOf('func normalizeInboxSince');
  const body = server.slice(i, server.indexOf('}', server.indexOf('return ""', i)));
  const m = body.match(/case ([^\n:]+):/);
  return m ? m[1].split(',').map(s => s.trim().replace(/"/g, '')) : [];
})();

describe('Inbox since basamakları (v0.9.954)', () => {
  it('30m basamağı VAR — Ö13’ün asıl isteği', () => {
    expect(clientRungs, '"şu 20 dakikada ortaya çıkanlar" hâlâ kurulamıyor').toContain('30m');
  });

  it('1h basamağı da eklendi', () => {
    expect(clientRungs).toContain('1h');
  });

  it('İSTEMCİ ve SUNUCU aynı kümeyi tanır', () => {
    // İstemcide olup sunucuda olmayan bir basamak, seçili görünüp
    // hiçbir şeyi elemeyen bir filtre demek.
    expect([...clientRungs].sort()).toEqual([...serverRungs].sort());
  });

  it('CUSTOM pencere YOK — sabit basamak sözleşmesi duruyor (v0.8.270)', () => {
    // Serbest bir pencere sunucu cache anahtarının kardinalitesini
    // patlatırdı; basamak sayısı küçük kalmalı.
    expect(clientRungs.length).toBeLessThanOrEqual(6);
    expect(inbox).not.toMatch(/<TimeRangePicker[\s\S]{0,80}since/);
  });

  it('basamaklar DAR → GENİŞ sırada', () => {
    const order = ['30m', '1h', '2h', '24h', '7d'];
    expect(clientRungs).toEqual(order);
  });
});
