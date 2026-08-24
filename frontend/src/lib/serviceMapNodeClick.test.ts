import { describe, it, expect } from 'vitest';
import { serviceMapNodeClick } from './serviceMapNodeClick';
import { nsNodeId } from './topoFold';

// v0.9.1330 — regresyon: /service-map çekmecesini AÇAN yol yoktu.
// `commitNode`'un tek çağrımı drawer'ın `onClose`'uydu (yani `''` ile), ve
// grafiğin `onSelectNode`'u her tıkta odağa gidiyordu. Bu testler kararı
// çiviliyor; en önemlisi `kind` muhafazası, çünkü onu düşürmek çekmeceyi
// `db:postgresql` gibi sentezlenmiş bir adla açar ve boş çekmece gösterir.

const NODES = [
  { service: 'payments-api' },                    // gerçek servis: kind YOK
  { service: 'orders-api' },
  { service: 'db:postgresql', kind: 'db' },       // sentezlenmiş
  { service: 'ext:api.stripe.com', kind: 'external' },
];

describe('serviceMapNodeClick', () => {
  it('başka düğüme tık → odak değiştir', () => {
    expect(serviceMapNodeClick('orders-api', 'payments-api', NODES)).toBe('focus');
  });

  it('odak yokken tık → odak değiştir', () => {
    expect(serviceMapNodeClick('payments-api', null, NODES)).toBe('focus');
  });

  it('ODAKLI gerçek servise İKİNCİ tık → çekmece', () => {
    expect(serviceMapNodeClick('payments-api', 'payments-api', NODES)).toBe('drawer');
  });

  // Bu dördü asıl muhafaza. Çekmece SERVİS adı bekliyor; sentezlenmiş bir
  // düğümle açılsa var olmayan bir servisi sorgular.
  it('odaklı db: düğümüne ikinci tık → çekmece AÇILMAZ', () => {
    expect(serviceMapNodeClick('db:postgresql', 'db:postgresql', NODES)).toBe('ignore');
  });

  it('odaklı external düğüme ikinci tık → çekmece AÇILMAZ', () => {
    expect(serviceMapNodeClick('ext:api.stripe.com', 'ext:api.stripe.com', NODES)).toBe('ignore');
  });

  // ⚠ Bu vaka MUTASYONLA bulundu. İlk yazımı `NODES` kullanıyordu ve ns
  // düğümü o listede olmadığı için `!node` muhafazası zaten 'ignore'
  // döndürüyordu — `isNsNode` dalını silmek HİÇBİR testi kırmıyordu
  // (gölgelenen muhafaza sınıfı). Ayırt edici vaka: ns düğümü listede VAR
  // ve `kind` TAŞIMIYOR, yani öbür iki muhafazanın ikisi de onu geçirir.
  // Bu gerçekçi de: ns süper-düğümleri topoFold'da istemci tarafında
  // türetiliyor ve grafiğin `onSelectNode` çağırmama sözleşmesi BU dosyadan
  // görünmüyor — değişirse tek tutan şey bu dal olur.
  it('namespace süper-düğümü → çekmece AÇILMAZ (listede olsa bile)', () => {
    const ns = nsNodeId('payments');
    const withNs = [...NODES, { service: ns }];
    expect(serviceMapNodeClick(ns, ns, withNs)).toBe('ignore');
  });

  it('grafikte olmayan (pencereden düşmüş) odak → çekmece AÇILMAZ', () => {
    // ?focus= deep-link'i şu an sessiz bir servisi adlandırabilir
    // (ServiceMap.tsx:152 şerhi bunu meşru sayıyor). Düğüm listede yoksa
    // `kind`'ını bilemeyiz — uydurmak yerine hiçbir şey yapmıyoruz.
    expect(serviceMapNodeClick('retired-svc', 'retired-svc', NODES)).toBe('ignore');
  });

  it('boş ad → ignore (drawer onClose kendi yolundan gider)', () => {
    expect(serviceMapNodeClick('', '', NODES)).toBe('ignore');
  });

  // Negatif kontrol: sentezlenmiş düğüm ODAKLANABİLİR olmalı. `kind`
  // muhafazasını yanlışlıkla 'focus' dalına da koyan biri, grafikte db
  // düğümüne tıklamayı tamamen ölü hale getirirdi.
  it('sentezlenmiş düğüme İLK tık yine odak değiştirir', () => {
    expect(serviceMapNodeClick('db:postgresql', 'payments-api', NODES)).toBe('focus');
  });
});
