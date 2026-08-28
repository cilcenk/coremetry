// entityHref — v0.10.135 (DETAY SAYFALARI ortak kural). Her entity adı DOĞRU
// entity'ye götürür (id cluster'ı taşır: aynı pod adı iki cluster'da iki ayrı
// link) ve bağlamı korur (range + at). Pod → /pod (mevcut sayfa, entity
// paneliyle), cluster → /clusters, diğerleri → /entity. Saf; vitest'li.
import type { EntityRecord, TimeRange } from './types';
import { windowRangeParam } from './urlState';
import { podDetailPath } from '@/pages/service/podDetailPath';

export type EntityLinkRecord = Pick<EntityRecord, 'type' | 'id' | 'name' | 'clusterId'> & { namespace?: string };

export interface EntityHrefOpts {
  range?: TimeRange | { fromNs: number; toNs: number } | string | null;
  /** ms epoch — tarihsel bağlam (trace zamanı); hedef sayfa o an geçerli kaydı çözer. */
  at?: number;
  /** Remote Cluster adı — /pod ve /clusters isim de kabul eder (ClusterByRef); yoksa id. */
  clusterName?: string;
  /** Pod linki için servis bağlamı: /pod geri-linki + servis-kapsamlı RED (inceleme, v0.10.136). */
  service?: string;
}

function encRange(r: EntityHrefOpts['range']): string | undefined {
  if (!r) return undefined;
  return typeof r === 'string' ? r : windowRangeParam(r);
}

export function entityHref(rec: EntityLinkRecord, opts: EntityHrefOpts = {}): string {
  const range = encRange(opts.range);
  if (rec.type === 'pod') {
    return podDetailPath({ pod: rec.name, cluster: opts.clusterName || rec.clusterId, namespace: rec.namespace, service: opts.service, range, at: opts.at });
  }
  const q = new URLSearchParams();
  if (rec.type === 'cluster') {
    q.set('cluster', rec.name);
    if (range) q.set('range', range);
    return `/clusters?${q.toString()}`;
  }
  q.set('id', rec.id);
  if (opts.at) q.set('at', String(opts.at));
  if (range) q.set('range', range);
  return `/entity?${q.toString()}`;
}

export type EntityLiveness = 'live' | 'stale' | 'gone';

/** Ömür kapalı → gone (artık mevcut değil); açık ama son senkronda görülmedi → stale. */
export function entityLiveness(rec: Pick<EntityRecord, 'validTo' | 'stale'>): EntityLiveness {
  if (rec.validTo) return 'gone';
  if (rec.stale) return 'stale';
  return 'live';
}
