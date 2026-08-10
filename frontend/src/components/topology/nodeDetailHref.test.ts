// nodeDetailHref — v0.9.958 (UX denetimi G3 / Ö10).
//
// Hangi kusuru zorunlu kılıyor: odaklı komşuluk penceresinde bir DB/queue
// düğümünün TEK eylemi "Recenter"dı ve düğüm adını servis adı sanıp
// `/service?name=oracle@oracle` açıyordu — var olmayan bir sayfa.
//
// Bu testlerin koruduğu iki karar, ikisi de "sessizce yanlış cevap
// vermektense hiç cevap verme" tarafında:
//
//	1. '@' yoksa instance BİLİNMİYOR → link YOK (uydurma instance ile
//	   sorgu hiçbir şeyle eşleşmez, sayfa sessizce boş açılır).
//	2. dbName linke KONMAZ — düğüm (system, instance) düzeyinde toplanmış,
//	   taşıdığı dbName yalnız bir örnek. Koymak soruyu sessizce daraltırdı.

import { describe, it, expect } from 'vitest';
import { nodeDetailHref, splitDbNodeName } from './nodeDetailHref';
import type { GraphNode } from '@/lib/types';

function node(p: Partial<GraphNode>): GraphNode {
  return {
    id: 'x', name: 'x', kind: 'service',
    calls: 1, errors: 0, errorRate: 0, rate: 1,
    ...p,
  } as GraphNode;
}

describe('splitDbNodeName', () => {
  it('İLK @ ayırıcı — sunucudaki decodeNodeName ile aynı kural', () => {
    expect(splitDbNodeName('oracle@oracle')).toEqual({ system: 'oracle', instance: 'oracle' });
    expect(splitDbNodeName('clickhouse@coremetry-monolithic'))
      .toEqual({ system: 'clickhouse', instance: 'coremetry-monolithic' });
    // instance adı '@' içerebilir; sistem adı içeremez.
    expect(splitDbNodeName('postgresql@pg@replica-1'))
      .toEqual({ system: 'postgresql', instance: 'pg@replica-1' });
  });

  it('@ yoksa ya da bir taraf boşsa null', () => {
    expect(splitDbNodeName('h2')).toBeNull();
    expect(splitDbNodeName('@instance')).toBeNull();
    expect(splitDbNodeName('system@')).toBeNull();
    expect(splitDbNodeName('')).toBeNull();
  });
});

describe('nodeDetailHref', () => {
  it('servis düğümü → /service, pencere taşınır', () => {
    const href = nodeDetailHref(node({ kind: 'service', name: 'payment-service' }), { range: '1h' });
    expect(href).toBe('/service?name=payment-service&range=1h');
  });

  it('DB düğümü → /database, system+instance ADDAN türer', () => {
    // CANLI VERİ (2026-08-11): /api/servicegraph düğümü "oracle@oracle",
    // /api/databases satırı system=oracle instance=oracle — birebir.
    const href = nodeDetailHref(node({ kind: 'database', name: 'oracle@oracle', system: 'oracle' }), { range: '1h' });
    expect(href).toContain('/database?');
    expect(href).toContain('system=oracle');
    expect(href).toContain('instance=oracle');
    expect(href).toContain('range=1h');
  });

  it('DB düğümünün dbName\'i linke KONMAZ — düğüm instance düzeyinde', () => {
    // Aynı oracle@oracle üstünde COREBANK ve CARDS var; düğüm tek.
    // `name=COREBANK` yazmak düğümün temsil ettiğinden DAR bir sayfa açardı.
    const href = nodeDetailHref(node({
      kind: 'database', name: 'oracle@oracle', system: 'oracle', dbName: 'COREBANK',
    }));
    expect(href).not.toContain('COREBANK');
    expect(href).not.toContain('name=');
  });

  it('instance türetilemeyen DB düğümü → null (link çizilmez)', () => {
    expect(nodeDetailHref(node({ kind: 'database', name: 'h2', system: 'h2' }))).toBeNull();
  });

  it('kuyruk düğümü → null: /messaging kimliği CLUSTER istiyor, düğüm taşımıyor', () => {
    expect(nodeDetailHref(node({ kind: 'queue', name: 'kafka:payment.settled', system: 'kafka' })))
      .toBeNull();
  });
});
