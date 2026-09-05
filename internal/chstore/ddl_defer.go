// v0.9.614 — şema yerindeyken DDL boot'u BEKLETMEZ: arka plana ertelenir.
//
// Operatör (prod, dördüncü gece): "Yeni image çıksan ve direkt deploy
// etsem DDL sorunu kalmasa. Şu an hâlâ prod podları ready olmuyor."
//
// v0.9.607/608 zaten var olan nesne/kolon için DDL'i ELEDİ (prod'da
// 44/49 + 58/76). Ama elenemeyenler — gerçekten yeni tablolar, MODIFY
// COLUMN'lar, probe'ların tetiklediği onarımlar — tıkalı kuyrukta her
// biri bütçesini (20 sn) doldurup boot'u DAKİKALARCA bekletiyordu.
// Pod ölmüyordu ama ready de olmuyordu.
//
// ASIL SORU: schema aylardır yerindeyken boot neden HERHANGİ bir DDL'i
// bekliyor? API'nin servise başlamak için ihtiyacı olan her şey zaten
// duruyor. Bekletilen ifadeler ya no-op ya da "kuyruk düzelince
// uygulanacak iyileştirme" — ikisi de boot'u rehin tutmayı hak etmiyor.
//
// MEKANİZMA: migrate başında şemanın yerinde olduğu tespit edilirse
// (küme modu + spans tablosu mevcut) execDDL ifadeleri ÇALIŞTIRMAK
// yerine biriktirir; New() dönmeden önce birikenler TEK arka plan
// goroutine'ine devredilir. Boot saniyeler içinde biter; ClickHouse
// kuyruğu düzelince ertelenenler sırayla uygulanır.
//
// NE ZAMAN ERTELENMEZ:
//   - TAZE KURULUM (spans yok): API tablosuz servise başlayamaz —
//     tamamen senkron, davranış birebir eski.
//   - TEK DÜĞÜM: kuyruk hastalığı yok, DDL zaten milisaniyelik;
//     ertelemek yalnız chsmoke'u yarıştırırdı.
//
// BİLİNÇLİ SONUÇLAR (gizlenmiyor):
//   - Probe'a bağlı özellikler (hasXCol kapıları) ertelenen kolon
//     uygulanana kadar KAPALI kalır — bu, bugünkü prod gerçeğinin
//     aynısı: kolon kuyrukta beklerken de kapalılar. Kuyruk düzelince
//     bir SONRAKİ boot'ta açılırlar.
//   - registerTraceAttrMaterialized SENKRON kalır (SELECT probe, DDL
//     değil): boot-sonrası-yazım-yok sözleşmesi korunur.
package chstore

import (
	"context"
	"log"
	"time"
)

// deferMigrationDDL — SAF kapı: bu boot DDL'i ertelesin mi?
func deferMigrationDDL(clusterMode, spansExists bool) bool {
	return clusterMode && spansExists
}

// ddlDeferred — execDDL şu an ÇALIŞTIRIYOR mu, BİRİKTİRİYOR mu?
//
// v0.9.633 (denetim bulgusu, daraltılmış) — erteleme kipinde execDDL
// nil döndüğü için çağıranların "başarılı" dalı koşuyor ve YANLIŞ
// şeyler söylüyor:
//
//	"[chstore] dropped operation_group_summary_5m MV (op_group absent
//	 — its insert trigger would block ingest)"
//
// DROP çalışmadı, KUYRUĞA ALINDI. Operatör "ingest'i tıkayan MV
// düştü" okuyor; halbuki MV hâlâ orada ve tetikleyicisi hâlâ her span
// INSERT'ünü reddedebilir. Bu, boot krizinde yanlış yere baktıracak
// cinsten bir yalan.
//
// Denetimin ÖNERDİĞİ düzeltme (execDDL erteleme kipinde hata döndürsün)
// UYGULANMADI: v0.9.614'ün tüm amacı boot'u DDL'e bağlamamaktı, hata
// döndürmek onu geri alırdı. Gereken tek şey çağıranın DOĞRU cümleyi
// kurabilmesi.
//
// SADECE boot/migrate goroutine'inden çağrılır — deferDDL alanı
// kilitsiz ve aynı sözleşme execDDL için de geçerli.
func (s *Store) ddlDeferred() bool { return s.deferDDL }

// ddlAppliedOrQueued — bir DDL'in ne olduğunu anlatan doğru fiil.
// SAF (tablo testli).
func ddlAppliedOrQueued(deferred bool) string {
	if deferred {
		return "kuyruğa alındı (arka planda uygulanacak)"
	}
	return "uygulandı"
}

// finishDeferredDDL — birikenleri devralır ve durumu TEMİZLER.
//
// Bayrağın listeden ÖNCE temizlenmesi kritik ve testli: arka plan
// yürütücüsü aynı execDDL'den geçer; bayrak açık kalsaydı yürütücünün
// her ifadesi yeniden birikir ve hiçbir şey uygulanmazdı — sessiz bir
// sonsuz erteleme.
func (s *Store) finishDeferredDDL() []string {
	s.deferDDL = false
	list := s.deferredDDL
	s.deferredDDL = nil
	return list
}

// runDeferredDDL — ertelenen ifadeleri sırayla uygular.
//
// Sıra korunur: DROP+CREATE çiftleri (MV onarımları) sıraya bağımlı.
// Hata di̇ğerleri̇ni̇ durdurmaz — her ifade bağımsız, hepsi idempotent;
// başarısız olan bir SONRAKİ boot'ta yeniden denenir (aynı eleme/
// erteleme döngüsünden geçerek).
func (s *Store) runDeferredDDL(list []string) {
	if len(list) == 0 {
		return
	}
	log.Printf("[chstore] %d ertelenmiş DDL arka planda uygulanıyor (boot bunları BEKLEMEDİ)", len(list))
	okN, failN := 0, 0
	start := time.Now()
	for _, sql := range list {
		// İfade başına bağımsız bütçe: tıkalı kuyrukta her biri
		// sunucu bütçesini (ddlTaskTimeoutSeconds) doldurabilir;
		// üstüne istemci payı.
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		err := s.execDDL(ctx, sql)
		cancel()
		if err != nil {
			failN++
			log.Printf("[chstore] ertelenmiş DDL uygulanamadı (sonraki boot'ta yeniden denenecek): %v", err)
			continue
		}
		okN++
	}
	log.Printf("[chstore] arka plan DDL bitti: %d uygulandı, %d başarısız, süre %s",
		okN, failN, time.Since(start).Round(time.Second))
	s.reprobePromotedAttrs()
}

// reprobePromotedAttrs — ertelenen DDL indikten sonra terfi kolonlarını
// YENİDEN dener.
//
// v0.9.625 — bu olmadan v0.9.621/623 küme kipinde İKİ RESTART istiyordu:
//
//	boot: repairPromotedAttrCols → execDDL → ERTELENDİ (koşmadı)
//	boot: probePromotedAttrs     → kolon hâlâ bozuk → KAYDEDİLMEDİ
//	arka plan: onarım uygulandı  → ama haritayı kimse güncellemiyor
//	→ hızlanma bir sonraki restart'a kadar KAPALI
//
// Operatör deploy'u çeker, hiçbir şey değişmemiş görür. Kabul edilemez.
//
// Neden bir kez değil de geri çekilmeli birkaç deneme: dağıtık DDL
// "kuyruğa alındı" (kod 159) BAŞARI sayılıyor (v0.9.604) — yani execDDL
// döndüğünde ifade henüz UYGULANMAMIŞ olabilir. Denemeler bunu kapatıyor;
// hiçbiri tutmazsa davranış bugünküyle aynı kalır (dizi yolu, doğru ama
// yavaş) ve bir sonraki boot yeniden dener.
//
// Kayıt kopyala-ve-değiştir + atomic pointer üzerinden (repo.go), yani
// bu goroutine okuyucularla yarışmıyor.
func (s *Store) reprobePromotedAttrs() {
	for _, wait := range []time.Duration{0, time.Minute, 5 * time.Minute, 15 * time.Minute} {
		if wait > 0 {
			time.Sleep(wait)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		found := s.probePromotedAttrs(ctx)
		// v0.10.299 — attribute hash indeksi de ertelenen DDL ile iner; kolonlar
		// görünür görünmez derleyici bloom yoluna geçer (restart gerekmez).
		if !AttrIndexAvailable() && s.probeAttrIndex(ctx) {
			registerAttrIndex(true)
		}
		// v0.10.409 — ai_calls genişletilmiş kolonları da ertelenen DDL ile
		// iner; görünür görünmez INSERT yeni listeye geçer (ikinci boot
		// gerekmez — gerekirse hâlâ güvenli).
		if !AICallsExtended() {
			s.probeAICallsColumns(ctx)
		}
		cancel()
		if len(found) == 0 {
			continue
		}
		registerTraceAttrMaterialized(found)
		log.Printf("[chstore] terfi kolonları ertelenen DDL sonrası devreye girdi (%d yazım) — restart gerekmedi", len(found))
		return
	}
	log.Printf("[chstore] terfi kolonları hâlâ doğrulanamadı — dizi yolunda kalınıyor, sonraki boot yeniden deneyecek")
}
