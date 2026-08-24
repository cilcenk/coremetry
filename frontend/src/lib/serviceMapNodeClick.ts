import { isNsNode } from './topoFold';

// serviceMapNodeClick — /service-map grafiğinde bir düğüme tıklandığında ne
// olacağının SAF kararı. Sayfadan ayrı duruyor ki testlenebilsin; karar üç
// girdiye bakıyor ve DOM'a, router'a, React'e dokunmuyor.
//
// v0.9.1330 — çekmeceyi AÇAN yol yoktu. ServiceMap.tsx'teki v0.9.1112 şerhi
// "Odaklı düğüme İKİNCİ tık açar" diye vaat ediyordu ve `ServiceMapNodeDrawer`
// tam olarak yazılmıştı, `?node=` parametresi de okunuyordu — ama `commitNode`
// yalnızca drawer'ın kendi `onClose`'undan, yani `''` ile çağrılıyordu.
// Grafiğin `onSelectNode`'u her tıkta `commitFocus`'a gidiyordu. Sonuç:
// çekmeceye ancak URL'e elle `?node=X` yazarak ulaşılıyordu. Vaat eden bir
// şerh + çalışan bir bileşen + hiç bağlanmamış tek bir kablo.
//
// ⚠ `kind` MUHAFAZASI ZORUNLU, süs değil. `/api/service-map` sentezlenmiş
// düğümler de döndürüyor (`internal/chstore/service_map.go` — `db:<system>`
// ve `peer_service`'ten türetilen external düğümler), ve çekmece bir SERVİS
// adı bekliyor: `db:postgresql` ile açılsa var olmayan bir servisi sorgulayıp
// boş bir çekmece gösterirdi. Ayırt edici alan `kind`: sentezlenmiş düğümler
// taşır, gerçek OTel servisleri TAŞIMAZ. Bu, aynı dosyanın kendi emsali —
// `ServiceMap.tsx`'in oto-odak dalı da `.filter(n => !n.kind)` ile "gerçek
// servis" tanımını böyle yapıyor.
//
// Namespace süper-düğümleri buraya normalde HİÇ gelmez (TopologyFlowGraph
// onlar için `onSelectNode` çağırmıyor, kendi katlama yolu var) — ama
// muhafaza burada da duruyor: o sözleşme grafiğin içinde ve buradan
// görünmüyor, yani bir gün değişirse bu fonksiyon sessizce `db:`/`ns:`
// adıyla çekmece açmaya başlardı.
export type NodeClickAction =
  | 'focus'   // odağı bu düğüme taşı (?focus=)
  | 'drawer'  // çekmeceyi aç (?node=) — yalnız ODAKLI gerçek servise ikinci tık
  | 'ignore'; // sentezlenmiş/bilinmeyen düğüm: ikinci tıkta yapılacak bir şey yok

export function serviceMapNodeClick(
  clicked: string,
  focus: string | null,
  nodes: readonly { service: string; kind?: string }[],
): NodeClickAction {
  if (!clicked) return 'ignore';
  // Başka bir düğüme tık = odak değiştir. Sentezlenmiş düğümler de
  // odaklanabilir (grafik onları komşulukta gösterir), bu yüzden `kind`
  // kontrolü BU dalda YOK — yalnız çekmece dalında var.
  if (clicked !== focus) return 'focus';
  if (isNsNode(clicked)) return 'ignore';
  const node = nodes.find(n => n.service === clicked);
  if (!node) return 'ignore';
  if (node.kind) return 'ignore';
  return 'drawer';
}
