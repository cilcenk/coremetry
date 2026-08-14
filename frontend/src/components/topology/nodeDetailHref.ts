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
// ─── Kuyruk düğümleri: KATALOG linki (v0.9.972) ─────────────────────
// Eskiden burada null vardı ve gerekçesi ŞUYDU: `/messaging` ÇEKMECESİNİN
// kimliği (system, cluster, destination) üçlüsü, düğüm ise cluster
// taşımıyor. O gerekçe hâlâ doğru — ama YANLIŞ SONUCA bağlanmıştı.
//
// Düzeltilen kısım: "(default)" varsaymak yanlış topiğe GÖTÜRMEZ.
// GetMessagingDetail cluster'ı TAM EŞİTLİKLE filtreliyor
// (dependencies.go), yani çok-cluster kurulumda sessizce BOŞ bir çekmece
// açardı — yanlış cevap değil, cevapsızlık. Yine de gönderilmez.
//
// Doğru hamle çekmeceyi zorlamak değil, KATALOĞU sahip olunan boyutlar
// kadar daraltmak: `/messaging?msys=<system>&q=<destination>`. İkisi de
// bugün çalışan URL paramları (DependenciesTable onları URL'den okuyor),
// yani backend/şema değişikliği SIFIR. `databasesFilterHref` (v0.9.964)
// ile aynı emsal: "satırın kendi boyutları kadar daraltılmış katalog,
// kendinden emin yanlış bir detay sayfasına yeğdir".
//
// Etiket bu yüzden "topiği aç" DEMEZ: katalogda ilk-200 tavanı var
// (dependencies.go), yani aranan satır listede olmayabilir.

import { databaseDetailHref, type DatabasePageScope } from '@/pages/databases/databaseParam';
// v0.9.1026 — çekmece param'ının kodlayıcısı TEK NÜSHADAN geliyor.
// Elle kurulmuş bir `sys|cluster|dest` dizesi decodeDestinationParam'ın
// kabul ettiği kaçış kurallarıyla ayrışabilirdi ve ayrışsa çekmece
// HİÇ AÇILMAZDI — tip hatası vermeyen, sessiz bir kırılma (v0.9.821'in
// depRowKey dersi, aynı sınıf).
import { encodeDestinationParam } from '@/pages/messaging/destinationParam';
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
 * queueDestination — kuyruk düğümünün DESTINATION parçası.
 *
 * Sistemi BURADA TÜRETMİYOR: `system` sunucudan geliyor (servicegraph.go
 * decodeNodeName, v0.9.972) ve bu fonksiyon yalnızca onu ÖNEK olarak
 * soyuyor. Ayrım önemli — sistemi istemcide yeniden türetmek, üç yerde
 * yaşayan bir kuralın dördüncü aynası olurdu.
 *
 * Ad sistemle başlamıyorsa '' döner: sunucunun sistemi ile adın
 * çelişmesi beklenmedik bir hâl ve o hâlde yalnız sisteme daraltılmış
 * katalog, uydurma bir destination ile hiçbir şey bulmayan bir aramaya
 * yeğdir (databasesFilterHref'in "olabildiğinden dar > kendinden emin
 * yanlış" kuralı).
 */
export function queueDestination(name: string, system: string): string {
  const sys = system.trim();
  if (!sys || name === sys) return '';
  for (const sep of [':', '@']) {
    const pfx = sys + sep;
    if (name.startsWith(pfx)) return name.slice(pfx.length);
  }
  return '';
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
  if (node.kind === 'queue') {
    // v0.9.972 — KATALOG linki (çekmece değil): cluster'ı olmayan bir
    // düğümden çekmece kimliği kurulamaz, ama sahip olunan boyutlarla
    // katalog daraltılabilir. Boş system/destination linke YAZILMAZ —
    // boş bir filtre, filtresiz sayfadan farksızdır ama URL'de "burayı
    // daralttım" diye durur.
    const p = new URLSearchParams();
    const sys = (node.system ?? '').trim();
    if (sys) p.set('msys', sys);
    const dest = queueDestination(node.name, sys);
    if (dest) p.set('q', dest);
    // v0.9.1026 — ÜÇLÜ TAMAMLANDIYSA GERÇEK DERİN LİNK. v0.9.972'nin
    // blokörü "cluster yok"tu; artık topology_edges_5m onu taşıyor
    // (v0.9.1025) ve çekmecenin kimliği (system, cluster, destination)
    // eksiksiz kurulabiliyor.
    //
    // Üçünün de dolu olması ŞART: decodeDestinationParam boş alanlı bir
    // param'ı reddediyor, yani eksik bir üçlü çekmeceyi açmaz — link
    // "açılacakmış gibi" durur ve hiçbir şey olmaz. O yüzden kapı üçlü.
    //
    // msys+q GERÇEK LİNKTE DE KALIYOR, süs değil: katalog yanıtı çağrı
    // hacmine göre ilk-200'de kesiliyor ve çekmece SATIR ÜZERİNDE
    // render ediliyor (DependenciesTable), yani hedef topic tavanın
    // altında kaldıysa yalnız ?destination= ile hiçbir şey açılmazdı —
    // sessiz kırılma. Filtre ile: satır varsa liste tam ona daralır ve
    // çekmece açılır; yoksa operatör boş listeyi + sayfanın kendi
    // "liste kesildi" uyarısını (v0.9.813) görür. İkisi de DÜRÜST hâl.
    const cluster = (node.cluster ?? '').trim();
    if (sys && cluster && dest) {
      p.set('destination', encodeDestinationParam({ system: sys, cluster, destination: dest }));
    }
    if (scope.range) p.set('range', scope.range);
    if (scope.env) p.set('env', scope.env);
    const qs = p.toString();
    return qs ? `/messaging?${qs}` : '/messaging';
  }
  return null;
}
