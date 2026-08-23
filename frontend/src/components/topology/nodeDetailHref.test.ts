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
import { nodeDetailHref, splitDbNodeName, queueDestination } from './nodeDetailHref';
import { encodeDestinationParam, decodeDestinationParam } from '@/pages/messaging/destinationParam';
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

  // ── v0.9.1326 — '@' yok = "instance BİLİNMİYOR", null DEĞİL ────────
  //
  // Bu pin TERSİNE DÖNDÜ. Eski hâli (`'h2'` → null) iki AYRI durumu tek
  // cevaba katlıyordu: "db düğümü değil" ile "db düğümü ama instance'ı
  // bilinmiyor". İkincisinin gerçek bir hedefi var.
  it('@ yoksa instance BOŞ — sistem hâlâ biliniyor', () => {
    expect(splitDbNodeName('h2')).toEqual({ system: 'h2', instance: '' });
    expect(splitDbNodeName('clickhouse')).toEqual({ system: 'clickhouse', instance: '' });
    // Sondaki '@' de aynı hâl: instance parçası boş.
    expect(splitDbNodeName('system@')).toEqual({ system: 'system', instance: '' });
  });

  it('adlandıracak bir SİSTEM yoksa hâlâ null', () => {
    expect(splitDbNodeName('@instance')).toBeNull();
    expect(splitDbNodeName('')).toBeNull();
    expect(splitDbNodeName('   ')).toBeNull();
  });

  // 'unknown' db_summary_5m'in GERÇEK bir instance değeri
  // (dbInstanceExpr'in terminal sentineli, internal/chstore/identity.go).
  // "Bilmiyorum" karşılığı olarak yazmak ÜÇÜNCÜ bir kimlik uydurmak
  // olurdu ve /database sayfası sessizce boş açılırdı.
  it("bilinmeyen instance ASLA 'unknown' sentineline çözülmez", () => {
    expect(splitDbNodeName('clickhouse')?.instance).not.toBe('unknown');
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

  // ── v0.9.1326 — KARIŞIK KİMLİK PENCERESİNİN dürüst yarısı ─────────
  //
  // Belirti: `/service-map` odaklı komşuluğunda bir "db · clickhouse"
  // düğümünün HİÇBİR linki yoktu. Kök neden v0.9.1318 ÖNCESİNDEKİ MV
  // yazıcısı: db instance'ını ÜÇ basamakla adlandırıyordu (peer_service →
  // server.address → net.peer.name), db_summary_5m ise ALTI ile. Lokalde
  // ölçüldü (v0.9.1318): `clickhouse` için üç basamak da BOŞ (53.692
  // span), altı basamaklı zincir `coremetry-monolithic` verdi. Düğüm düz
  // `db:clickhouse` yazıldı, splitDbNodeName '@' göremedi, link kurulmadı.
  //
  // 1318 YAZICIYI düzeltti; bu test OKUMA tarafını düzeltiyor. İkisi
  // ayrı çünkü topology_edges_5m'in TTL'i 14 GÜN (store.go:1954): eski
  // yazım o pencere boyunca okunmaya devam ediyor ve geçmiş yeniden
  // YAZILMIYOR (o satırlar instance'ı gerçekten bilmiyor).
  it('instance BİLİNMEYEN DB düğümü → motora daraltılmış KATALOG', () => {
    const href = nodeDetailHref(
      node({ kind: 'database', name: 'clickhouse', system: 'clickhouse' }), { range: '1h' })!;
    expect(href).toContain('/databases?');
    expect(href).toContain('dbsys=clickhouse');
    expect(href).toContain('range=1h');
    // Detay sayfası KURULMAZ: instance uydurmak sessizce boş sayfa açardı.
    expect(href).not.toContain('/database?');
    expect(href).not.toContain('instance=');
    // Ve özellikle: 'unknown' sentineli YAZILMAZ (ÜÇÜNCÜ kimlik olurdu).
    expect(href).not.toContain('unknown');
  });

  // Değişimin EKLEMELİ olduğunun kanıtı: instance'ı ZATEN bilinen düğüm
  // aynı detay linkini korur. (infra_host'un üç basamağı dbInstanceExpr'in
  // ilk üç basamağıyla BİREBİR aynı ve aynı sırada — biri doluysa iki
  // zincir aynı değeri verir, yani 1318 hiçbir mevcut düğümü yeniden
  // adlandırmadı.)
  it('instance BİLİNEN düğüm detay sayfasında KALIR — davranış aynen', () => {
    const href = nodeDetailHref(
      node({ kind: 'database', name: 'oracle@oracle', system: 'oracle' }), { range: '1h' })!;
    expect(href).toContain('/database?');
    expect(href).not.toContain('/databases?');
  });

  it('adlandıracak sistem yoksa hâlâ link YOK', () => {
    expect(nodeDetailHref(node({ kind: 'database', name: '', system: '' }))).toBeNull();
    expect(nodeDetailHref(node({ kind: 'database', name: '@x', system: '' }))).toBeNull();
  });

  // v0.9.972 — bu pin TERSİNE DÖNDÜ ve dönmesi doğru. Eski gerekçe
  // ("cluster yok → link yok") yanlış sonuca bağlanmıştı: '(default)'
  // varsaymak YANLIŞ topiğe götürmez, GetMessagingDetail cluster'ı tam
  // eşitlikle filtrelediği için sessizce BOŞ çekmece açar. Çekmece hâlâ
  // kurulamaz — ama KATALOG sahip olunan boyutlarla daraltılabilir.
  it('kuyruk düğümü → /messaging kataloğu, system+destination taşınır', () => {
    const href = nodeDetailHref(
      node({ kind: 'queue', name: 'kafka:payment.settled', system: 'kafka' }), { range: '1h' })!;
    expect(href).toContain('/messaging?');
    expect(href).toContain('msys=kafka');
    expect(href).toContain('q=payment.settled');
    expect(href).toContain('range=1h');
    // Çekmece kimliği KURULMAZ: cluster/destination paramı yazılmamalı.
    expect(href).not.toContain('cluster=');
    expect(href).not.toContain('destination=');
  });

  it('kuyruk düğümü — `<sys>@<host>` şekli', () => {
    const href = nodeDetailHref(
      node({ kind: 'queue', name: 'rabbitmq@broker-1', system: 'rabbitmq' }))!;
    expect(href).toContain('msys=rabbitmq');
    expect(href).toContain('q=broker-1');
  });

  it('kuyruk düğümü — yalnız sistem (`queue:<sys>`) → q YAZILMAZ', () => {
    // Boş bir filtre filtresiz sayfadan farksızdır ama URL'de
    // "daralttım" diye durur; yazılmaması dürüstlük.
    const href = nodeDetailHref(node({ kind: 'queue', name: 'sqs', system: 'sqs' }))!;
    expect(href).toBe('/messaging?msys=sqs');
  });

  // ── v0.9.1026 — cluster geldiğinde GERÇEK derin link ──────────────
  //
  // İki dal da çivileniyor, çünkü ikisi de DOĞRU cevaptır ve hangisinin
  // seçildiği veriye bağlı: üçlü tamamsa çekmece, değilse katalog.
  // Yanlış dalı seçmek sessizdir — çekmece açılmayan bir link ile boş
  // açılan bir çekmece arasındaki fark, operatörün gözünde ikisi de
  // "bir şey olmadı"dır.
  it('cluster VARSA çekmece derin linki — üçlü kimlik tam', () => {
    const href = nodeDetailHref(
      node({ kind: 'queue', name: 'kafka:payment.settled', system: 'kafka', cluster: 'broker-1:9092' }),
      { range: '1h' })!;
    const sp = new URLSearchParams(href.split('?')[1]);
    // Çekmecenin okuduğu codec'in TA KENDİSİ ile karşılaştırılıyor —
    // elle kurulmuş bir beklenti, kaçış kuralları ayrıştığında testi
    // değil ürünü haklı çıkarırdı.
    expect(sp.get('destination')).toBe(
      encodeDestinationParam({ system: 'kafka', cluster: 'broker-1:9092', destination: 'payment.settled' }),
    );
    // decode → encode gidiş-dönüşü: link gerçekten AÇILABİLİR mi.
    expect(decodeDestinationParam(sp.get('destination'))).toEqual({
      system: 'kafka', cluster: 'broker-1:9092', destination: 'payment.settled',
    });
    // Katalog filtresi derin linkte de KALIYOR: çekmece satır üzerinde
    // render ediliyor, satır ilk-200 tavanının altındaysa yalnız
    // ?destination= ile hiçbir şey açılmaz.
    expect(sp.get('msys')).toBe('kafka');
    expect(sp.get('q')).toBe('payment.settled');
    expect(sp.get('range')).toBe('1h');
  });

  it('cluster YOKSA katalog köprüsü — v0.9.972 davranışı AYNEN', () => {
    const href = nodeDetailHref(
      node({ kind: 'queue', name: 'kafka:payment.settled', system: 'kafka' }), { range: '1h' })!;
    expect(new URLSearchParams(href.split('?')[1]).get('destination')).toBeNull();
    expect(href).toContain('msys=kafka');
  });

  it('cluster VAR ama destination YOK → çekmece kurulmaz', () => {
    // decodeDestinationParam boş alanlı param'ı reddediyor: eksik üçlü
    // ile kurulan link "açılacakmış gibi" durur ve hiçbir şey yapmaz.
    const href = nodeDetailHref(node({ kind: 'queue', name: 'sqs', system: 'sqs', cluster: '(default)' }))!;
    expect(href).toBe('/messaging?msys=sqs');
  });

  it('boş/whitespace cluster çekmece SAYILMAZ', () => {
    const href = nodeDetailHref(
      node({ kind: 'queue', name: 'kafka:orders', system: 'kafka', cluster: '   ' }))!;
    expect(href).not.toContain('destination=');
  });

  it('destination URL-ENCODE edilir — nokta/eğik çizgi taşıyan topikler', () => {
    const href = nodeDetailHref(
      node({ kind: 'queue', name: 'kafka:orders/v2 eu', system: 'kafka' }))!;
    expect(href).not.toContain(' ');
    expect(decodeURIComponent(new URLSearchParams(href.split('?')[1]).get('q')!))
      .toBe('orders/v2 eu');
  });
});

describe('queueDestination — sistemi TÜRETMEZ, ÖNEK olarak soyar', () => {
  it('iki ayırıcı da kabul', () => {
    expect(queueDestination('kafka:payment.settled', 'kafka')).toBe('payment.settled');
    expect(queueDestination('rabbitmq@broker-1', 'rabbitmq')).toBe('broker-1');
  });

  it('ad == sistem → destination YOK', () => {
    expect(queueDestination('sqs', 'sqs')).toBe('');
  });

  it('ad sistemle BAŞLAMIYORSA boş — uydurma destination yerine dar katalog', () => {
    // Sunucunun sistemi ile ad çelişirse: yalnız sisteme daraltılmış
    // katalog, hiçbir şey bulmayan bir aramadan iyidir.
    expect(queueDestination('kafka:payment.settled', 'rabbitmq')).toBe('');
  });

  it('boş sistem → boş', () => {
    expect(queueDestination('kafka:payment.settled', '')).toBe('');
    expect(queueDestination('kafka:payment.settled', '   ')).toBe('');
  });

  it('destination kendi içinde ayırıcı taşıyabilir — İLK ayırıcıda böl', () => {
    expect(queueDestination('kafka:tenant:orders', 'kafka')).toBe('tenant:orders');
  });
});
