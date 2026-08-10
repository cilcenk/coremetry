// nodeDetailHref — v0.9.958 (UX denetimi G3 / Ö10). Topoloji grafiğindeki
// bir düğümün DETAY sayfası.
//
// ─── Hangi çıkmazı kapatıyor ─────────────────────────────────────────
// Odaklı komşulukta bir "db · oracle" pill'inin üstüne gelen operatörün
// gördüğü TEK eylem "Recenter"dı ve o buton düğümün adını SERVİS adı
// sanıp `/service?name=oracle@oracle` açıyordu — var olmayan bir sayfa.
// Yolculuk çıkmaza giriyordu: bağımlılığı görüyorsun, içine giremiyorsun.
//
// ─── Kimlik: ÜÇLÜ, etiket DEĞİL (v0.9.821'in dersi) ─────────────────
// `/database` kimliği (system, instance, dbName) üçlüsüdür. Grafiğin
// düğüm ADI bu üçlünün ilk ikisini taşıyor: sunucu tarafı adı
// `db:<system>@<instance>` kuruyor ve istemciye ön-eki soyulmuş hâlde
// (`oracle@oracle`) geliyor (internal/api/servicegraph.go decodeNodeName
// AYNI '@' ayrımını yapıyor — sistem İLK '@'ten önce).
//
// CANLI VERİYLE DOĞRULANDI (2026-08-11, lokal küme — bu dilim kanıtsız
// gönderilmeyecekti):
//
//	/api/servicegraph düğümü : id "db:oracle@oracle", system "oracle",
//	                           dbName "COREBANK"
//	/api/databases satırı    : system "oracle", instance "oracle",
//	                           dbName "COREBANK"
//
// yani '@'ten türeyen instance katalogdaki instance'la BİREBİR eşleşiyor.
//
// ─── dbName BİLEREK TAŞINMIYOR ──────────────────────────────────────
// Grafik düğümü (system, instance) düzeyinde toplanır; taşıdığı `dbName`
// o instance'ın YALNIZCA BİR örneğidir (aynı oracle@oracle üstünde hem
// COREBANK hem CARDS var, düğüm tek). Onu linke koymak, düğümün temsil
// ettiğinden DAHA DAR bir sayfa açmak olurdu — "sessizce daralan soru"
// sınıfı. `/database` boş dbName'i zaten meşru ve SÖYLENEN bir hâl
// olarak taşıyor ("bu instance'taki her veritabanı"), düğümün anlamı da
// tam olarak bu.
//
// ─── Kuyruk düğümleri KAPSAM DIŞI ────────────────────────────────────
// `/messaging` çekmecesinin kimliği (system, cluster, destination)
// ÜÇLÜSÜ ve üçü de zorunlu (destinationParam.ts). Topoloji kuyruk düğümü
// (`queue:kafka:payment.settled`) CLUSTER TAŞIMIYOR. "(default)" diye
// varsaymak tek-cluster kurulumda çalışır, çok-cluster kurulumda sessizce
// YANLIŞ topiğe götürürdü — kanıtlanamayan eşleme gönderilmez.

import { databaseDetailHref, type DatabasePageScope } from '@/pages/databases/databaseParam';
import type { GraphNode } from '@/lib/types';

/**
 * splitDbNodeName — `<system>@<instance>` → parçalar.
 *
 * İLK '@' ayırıcıdır, sunucu tarafındaki decodeNodeName ile aynı kural
 * (internal/api/servicegraph.go): instance adı '@' içerebilir, sistem
 * adı içeremez. '@' yoksa instance BİLİNMİYOR demektir ve null döner —
 * uydurma bir instance ile sorgulamak hiçbir şeyle eşleşmeyen sessizce
 * boş bir sayfa açardı.
 */
export function splitDbNodeName(name: string): { system: string; instance: string } | null {
  const at = name.indexOf('@');
  if (at <= 0) return null;
  const system = name.slice(0, at);
  const instance = name.slice(at + 1);
  if (!system || !instance) return null;
  return { system, instance };
}

/**
 * nodeDetailHref — düğümün detay sayfası, ya da null ("bu düğümün
 * gidilecek bir sayfası yok").
 *
 * null dönmek BİR CEVAP: çağıran, olmayan bir hedefe link çizmek yerine
 * hiç çizmez. Eskiden burada her düğüm için bir servis linki vardı ve
 * yarısı yanlıştı.
 */
export function nodeDetailHref(node: GraphNode, scope: DatabasePageScope = {}): string | null {
  if (node.kind === 'service') {
    const p = new URLSearchParams();
    p.set('name', node.name);
    if (scope.range) p.set('range', scope.range);
    if (scope.env) p.set('env', scope.env);
    return `/service?${p.toString()}`;
  }
  if (node.kind === 'database') {
    // system alanı sunucudan da geliyor; ad çözümlemesiyle çelişirse
    // ADI kazandırıyoruz — `/database` sayfası kimliği ADIN türettiği
    // instance ile eşleştiriyor ve ikisi ayrışırsa sayfa boş kalır.
    const parts = splitDbNodeName(node.name);
    if (!parts) return null;
    return databaseDetailHref(
      { system: parts.system, instance: parts.instance, dbName: '', source: 'spans' },
      scope,
    );
  }
  return null;
}
