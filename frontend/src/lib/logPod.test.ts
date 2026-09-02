import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { POD_FIELDS, podOfLog, podEntryOfLog } from './logPod';

// v0.9.1249 — bağlam modalının pod kapsamı.

describe('podOfLog', () => {
  it('resourceAttributes içindeki her varyant anahtarı okur', () => {
    for (const k of POD_FIELDS) {
      expect(podOfLog({ resourceAttributes: { [k]: 'payment-api-7d6f9b54c5-xkv2m' } }))
        .toBe('payment-api-7d6f9b54c5-xkv2m');
    }
  });

  it('attributes içindeki varyantları da okur (ES düzleşmiş alanlar)', () => {
    for (const k of POD_FIELDS) {
      expect(podOfLog({ attributes: { [k]: 'checkout-abc-123' } })).toBe('checkout-abc-123');
    }
  });

  it('resourceAttributes attributes\'tan önce gelir', () => {
    expect(podOfLog({
      resourceAttributes: { 'k8s.pod.name': 'res-pod' },
      attributes: { 'kubernetes.pod_name': 'attr-pod' },
    })).toBe('res-pod');
  });

  it('BOŞ kanonik değer zinciri durdurmaz (v0.5.224 dersi)', () => {
    // Pipeline kanonik attr'ı boş yazıp gerçek değeri snake_case
    // ikizine koyduğunda zincir yürümeli — durursa pod bulunamaz,
    // düğme çizilmez ve özellik o kurulumda sessizce yok olur.
    expect(podOfLog({
      resourceAttributes: { 'kubernetes.pod_name': '', 'k8s.pod.name': 'gercek-pod-1' },
    })).toBe('gercek-pod-1');
  });

  it('pod yoksa boş dize döner — yalancı vaat yok', () => {
    expect(podOfLog({ resourceAttributes: { 'k8s.namespace.name': 'prod' }, attributes: {} })).toBe('');
    expect(podOfLog({})).toBe('');
    expect(podOfLog(null)).toBe('');
    expect(podOfLog(undefined)).toBe('');
    expect(podOfLog({ resourceAttributes: null, attributes: null })).toBe('');
  });

  it('alakasız pod-benzeri anahtarları uydurmaz', () => {
    expect(podOfLog({ resourceAttributes: { 'k8s.pod.uid': 'abc', 'pod.template.hash': 'x' } })).toBe('');
  });
});

// DİLLER ARASI KAPI — logFieldAliases.test.ts emsali (v0.9.661).
// Gösterilen pod adı, backend filtresinin SORDUĞU alanlardan gelmeli;
// listeler ayrışırsa modal bir pod gösterir, "yalnız bu pod" ise sıfır
// satır döndürür (v0.8.265 sınıfı sessiz yalan).
describe('Go ↔ TS pod alan aynası', () => {
  it('POD_FIELDS esPodFields ile birebir aynı (sıra dahil)', () => {
    const go = readFileSync(
      resolve(__dirname, '../../../internal/logstore/elasticsearch.go'), 'utf8');
    const anchor = 'var esPodFields = []string{';
    const i = go.indexOf(anchor);
    expect(i, 'Go kaynağında esPodFields bulunamadı').toBeGreaterThan(-1);
    const body = go.slice(i + anchor.length, go.indexOf('}', i));
    // Yorum soyucu: satır yorumları alan adı İÇEREBİLİR (bu kod
    // tabanında dört kez ısırdı — logFieldAliases.test.ts notu).
    const code = body.split('\n').map(l => {
      const c = l.indexOf('//');
      return c < 0 ? l : l.slice(0, c);
    }).join('\n');
    const goFields = [...code.matchAll(/"([^"]+)"/g)].map(m => m[1]);
    expect(goFields).toEqual([...POD_FIELDS]);
  });
});

describe('podEntryOfLog (v0.10.282 pod pivotu)', () => {
  it('değerle birlikte onu taşıyan anahtarı döner — pill o anahtarla yazılır', () => {
    expect(podEntryOfLog({ resourceAttributes: { 'k8s.pod.name': 'api-7f-x1' } }))
      .toEqual({ key: 'k8s.pod.name', value: 'api-7f-x1' });
    expect(podEntryOfLog({ attributes: { pod_name: 'w-1' } })).toEqual({ key: 'pod_name', value: 'w-1' });
    expect(podEntryOfLog({ resourceAttributes: { 'k8s.pod.name': '' }, attributes: {} })).toBeNull();
    expect(podEntryOfLog(null)).toBeNull();
  });
  it('podOfLog aynı girdiden türer', () => {
    const l = { resourceAttributes: { 'kubernetes.pod_name': 'p' } };
    expect(podOfLog(l)).toBe(podEntryOfLog(l)!.value);
  });
});
