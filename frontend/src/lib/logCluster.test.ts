import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { CLUSTER_FIELDS, clusterEntryOfLog, clusterOfLog } from './logCluster';

// v0.10.501 (B4) — cluster hücresinin ⊕/⊖ pivotu satırın TAŞIDIĞI
// anahtarla yazılır; gösterilen değer süzgecin bulacağı değerdir.
describe('clusterEntryOfLog', () => {
  it('taşıyan anahtarı ve değeri döner, resourceAttributes önce', () => {
    expect(clusterEntryOfLog({
      resourceAttributes: { 'k8s.cluster.name': 'prod-eu' },
      attributes: { 'openshift.labels.cluster': 'other' },
    })).toEqual({ key: 'k8s.cluster.name', value: 'prod-eu' });
    expect(clusterEntryOfLog({ attributes: { 'openshift.labels.cluster': 'ocpma' } }))
      .toEqual({ key: 'openshift.labels.cluster', value: 'ocpma' });
  });
  it('boş dize değer sayılmaz — zincir yürür', () => {
    expect(clusterOfLog({ resourceAttributes: { 'openshift.labels.cluster': '', 'kubernetes.cluster_name': 'dr' } })).toBe('dr');
  });
  it('yoksa null / boş', () => {
    expect(clusterEntryOfLog({ resourceAttributes: { 'k8s.namespace.name': 'x' } })).toBeNull();
    expect(clusterOfLog(null)).toBe('');
  });
});

// DİLLER ARASI KAPI — logPod.test.ts emsali: CH DSL derleyicisinin cluster
// takma adları CLUSTER_FIELDS'in alt kümesi olmalı; aksi hâlde bir pill
// yazılır ama CH'de cluster zincirine değil genel attr aramasına düşer.
describe('Go ↔ TS cluster anahtar aynası', () => {
  it('log_query_compile.go cluster case anahtarları CLUSTER_FIELDS içinde', () => {
    const go = readFileSync(
      resolve(__dirname, '../../../internal/chstore/log_query_compile.go'), 'utf8');
    const line = go.split('\n').find(l => l.includes('case "cluster"'));
    expect(line, 'Go kaynağında case "cluster" bulunamadı').toBeTruthy();
    const goKeys = [...line!.matchAll(/"([^"]+)"/g)].map(m => m[1]);
    for (const k of goKeys) expect([...CLUSTER_FIELDS], `eksik: ${k}`).toContain(k);
  });
  it('ES doküman yolu openshift.labels.cluster listede (prod şekli)', () => {
    expect([...CLUSTER_FIELDS]).toContain('openshift.labels.cluster');
  });
});

// Kaynak pini: LogTable cluster değerini bu yardımcıdan alır ve servis /
// cluster / attribute hücreleri pod hücresiyle aynı .lt-pivot kalıbını taşır.
describe('LogTable hücre pivotları (v0.10.501)', () => {
  const src = readFileSync(resolve(__dirname, '../components/LogTable.tsx'), 'utf8');
  it('cluster hücresi clusterEntryOfLog kullanır', () => {
    expect(src).toContain('clusterEntryOfLog(l)');
  });
  it('servis / cluster / attribute hücreleri pivotlu', () => {
    for (const want of [
      "pivotCell('service.name', l.serviceName",
      'pivotCell(ce.key, ce.value',
      'pivotCell(id, v',
      'pivotCell(pe.key, pe.value',
    ]) expect(src, want).toContain(want);
  });
});
