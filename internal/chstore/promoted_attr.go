// v0.9.621 — terfi etmiş attribute kolonları PROD'DA HİÇ ÇALIŞMADI.
//
// Operator-reported: /traces?range=6h&filters=[{"k":"channel_code",…}]
// ClickHouse'ta 26 sn sürüp max_execution_time=25s'e takılıyor.
//
// Kök neden ÜÇ ayrı katman ve her biri tek başına düzeltilirse ya işe
// yaramaz ya da YANLIŞ sonuç verir:
//
//	1. Kolon ifadesi BÜYÜK harf okuyordu:
//	   MATERIALIZED attr_values[indexOf(attr_keys, 'CHANNEL_CODE')]
//	   Prod ise KÜÇÜK harf yazıyor ('channel_code' — operatör ölçümü,
//	   10 dakikada 2.67M span). Yani kolon v0.9.198'den beri HEP BOŞTU.
//	2. Yönlendirme haritası da BÜYÜK harf anahtarlıydı ve arama tam
//	   eşleşme (repo.go traceExtrasProjection / business_dims.go) —
//	   küçük harf anahtar hiç eşleşmiyordu.
//	3. Filtre yolu (filterexpr.go) haritaya HİÇ bakmıyordu.
//
// Bu dosya (1) ve (2)'yi kapatıyor; (3) ayrı bir dilim, çünkü kolon
// doğru dolmadan filtreyi oraya yönlendirmek BOŞ sonuç verir.
//
// ÖLÇÜLDÜ (CH 24.8, lokal, 10M satır prod şeklinde tablo, 3 koşu medyanı):
//
//	dizi açma (bugünkü)      10.000.000 satır   3.90 GiB   362 ms
//	terfi etmiş kolon        10.000.000 satır   1.98 GiB   204 ms
//	kolon + set(0) indeks     1.310.720 satır    261 MiB    81 ms
//
// Kolonun tek başına yalnız 2× olmasının sebebi: YENİ eklenen bir
// MATERIALIZED kolon eski part'larda SAKLANMAZ, okuma anında diziden
// hesaplanır. Asıl kazanç skip index'te (ayrı dilim).
//
// AYNI ÖLÇÜM, DÜZELTMENİN ŞEKLİNİ BELİRLEDİ:
//
//	ALTER … MODIFY COLUMN … MATERIALIZED <yeni ifade>
//	  → eski part'lar ONARILMAZ, boş kalır (ölçüldü)
//	ALTER … DROP COLUMN + ADD COLUMN <aynı ad>
//	  → eski part'lar için okuma anında HESAPLANIR (ölçüldü: 200.000/
//	    200.000 boş → 0 boş)
//
// Bu yüzden onarım DROP+ADD. MODIFY olsaydı tarihsel veri sessizce boş
// dönerdi ve filtreyi kolona yönlendirmek YANLIŞ sonuç üretirdi.
package chstore

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// promotedAttr — bir operatör attribute'unun taşıyıcı kolonu ve kabul
// ettiği yazımlar.
//
// SIRA ÖNEMLİ: coalesce ilk boş-olmayanı alır. Bir span'de iki yazım
// birden bulunursa kolon BÜYÜK harfli olanı taşır — ve probe (aşağıda)
// o durumda küçük harfli yazımı KAYDETMEZ, yani o filtre dizi yolunda
// kalır. Yanlış sonuç yerine yavaş sonuç.
type promotedAttr struct {
	col  string
	keys []string
}

var promotedAttrs = []promotedAttr{
	{col: "attr_channel_code", keys: []string{"CHANNEL_CODE", "channel_code"}},
	{col: "attr_function_code", keys: []string{"FUNCTION_CODE", "function_code"}},
}

// promotedAttrExpr — kolonun MATERIALIZED ifadesi. SAF (tablo testli).
//
// Anahtarlar SQL'e gömülü sabitler; bind arg olamazlar çünkü DDL
// metninin parçası. Kaynakları bu dosyadaki sabit liste — kullanıcı
// girdisi değil.
func promotedAttrExpr(keys []string) string {
	parts := make([]string, 0, len(keys)+1)
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("nullIf(attr_values[indexOf(attr_keys, '%s')], '')", k))
	}
	parts = append(parts, "''")
	return "coalesce(" + strings.Join(parts, ", ") + ")"
}

// promotedAttrNeedsRepair — mevcut default_expression istenen TÜM
// yazımları içeriyor mu? SAF.
//
// Ham metin karşılaştırması YERİNE "her anahtar geçiyor mu" testi:
// ClickHouse ifadeyi normalize ederek saklıyor (boşluk, parantez,
// backtick), dolayısıyla birebir eşitlik kırılgan olurdu — ve yanlış
// bir "farklı" kararı her boot'ta gereksiz bir DROP+ADD demek.
func promotedAttrNeedsRepair(have string, keys []string) bool {
	for _, k := range keys {
		if !strings.Contains(have, "'"+k+"'") {
			return true
		}
	}
	return false
}

// promotedAttrDDL — kolonun mevcut hâline göre gönderilecek ifadeler.
// SAF (tablo testli).
//
// ⚠ BU İFADELER `alters` LİSTESİNE KONULAMAZ. planAlterDDL (v0.9.608)
// boot'ta bir kolon anlık görüntüsü alıp `ADD COLUMN IF NOT EXISTS`'i
// eler; DROP COLUMN ise regex'e uymadığı için elenmez. O listede:
//
//	DROP  → gönderilir     (anlık görüntüde kolon "var")
//	ADD   → ELENİR          (aynı anlık görüntü "var" diyor)
//	sonuç → kolon düşer ve HİÇ GERİ GELMEZ; onu okuyan her sorgu
//	        UNKNOWN_IDENTIFIER verir.
//
// Bu yüzden çağıran doğrudan s.execDDL kullanır (store.go), tıpkı
// v0.9.198'in attrColAlters döngüsü gibi.
//
// ADD'de IF NOT EXISTS bilinçli KALIYOR: DROP başarısız olursa ADD
// no-op olur ve eski (bozuk ama var olan) kolon yerinde kalır. Probe
// onu zaten kaydetmez → dizi yolu → doğru-ama-yavaş. Kolonsuz kalmaktan
// iyidir.
// v0.9.623 — skip index de bu listeye girdi ve SIRA ZORUNLU.
//
// ÖLÇÜLDÜ (CH 24.8): indeksi duran bir kolonu düşürmek
//
//	Code: 47 … Cannot apply mutation because it breaks skip index
//	           idx_attr_channel_code. (UNKNOWN_IDENTIFIER)
//
// ile REDDEDİLİYOR. Yani onarım önce indeksi düşürmek zorunda. İki
// ayrı yerde iki yarım sıra tutmak yerine tek sıralı liste — bu
// oturumun tekrar eden dersi: bir kural iki yere bölününce ayrışıyor.
//
// idxExists ayrı bir parametre çünkü `ADD INDEX IF NOT EXISTS`'i her
// boot göndermek tıkalı bir dağıtık DDL kuyruğunda bedava değil
// (v0.9.607/608'in tüm konusu buydu): sonucu belli bir tur.
func promotedAttrDDL(a promotedAttr, have string, colExists, idxExists bool) []string {
	idx := "idx_" + a.col
	needsRepair := colExists && promotedAttrNeedsRepair(have, a.keys)

	var out []string
	if needsRepair && idxExists {
		out = append(out, fmt.Sprintf("ALTER TABLE spans DROP INDEX IF EXISTS %s", idx))
	}
	if needsRepair {
		out = append(out, fmt.Sprintf("ALTER TABLE spans DROP COLUMN IF EXISTS %s", a.col))
	}
	if !colExists || needsRepair {
		out = append(out, fmt.Sprintf(
			"ALTER TABLE spans ADD COLUMN IF NOT EXISTS %s LowCardinality(String) MATERIALIZED %s",
			a.col, promotedAttrExpr(a.keys)))
	}
	// Onarım indeksi düşürdüyse idxExists'e bakmadan geri konur.
	if !idxExists || needsRepair {
		out = append(out, fmt.Sprintf(
			"ALTER TABLE spans ADD INDEX IF NOT EXISTS %s %s TYPE set(0) GRANULARITY 4",
			idx, a.col))
	}
	return out
}

// promotedAttrResolve — KANONİK bir iş-boyutu anahtarını okunabilir bir
// SQL ifadesine çevirir. ok=false ise anahtar terfi listesinde yok.
//
// v0.9.624 — bu, KOD İÇİ sabit anahtar listeleri içindir, kullanıcı
// filtresi için DEĞİL. Ayrım bilinçli:
//
//   - Kullanıcı filtresi (filterexpr.go) HARF DUYARLI kalır: OTel'de
//     attribute anahtarları harf duyarlıdır, 'CHANNEL_CODE' yazan
//     operatör gerçekten o anahtarı sormuştur ve olmayan bir anahtarın
//     sessizce başkasına eşlenmesi sürpriz olur.
//   - Kod içi liste (businessDimKeys, copilot kırılım listeleri) bir
//     KAVRAMI ifade eder — "kanal boyutu" — yazımı değil. Prod verisi
//     küçük harf yazdığı için o listelerin BÜYÜK harfli girdileri
//     hiçbir şeye eşleşmiyordu ve iş-boyutu kırılımı SIFIR satır
//     dönüyordu (v0.9.511/580 ile gelen kanıt bloğu prod'da ölüydü).
//
// Kolon kayıtlıysa onu, değilse TÜM yazımları deneyen dizi ifadesini
// döndürür — yani kolon henüz onarılmamış kurulumlarda da doğru çalışır.
func promotedAttrResolve(key string) (string, []any, bool) {
	for _, a := range promotedAttrs {
		match := false
		for _, k := range a.keys {
			if strings.EqualFold(k, key) {
				match = true
				break
			}
		}
		if !match {
			continue
		}
		// Probe hangi yazımı doğruladıysa kolon o yazımla kayıtlı;
		// hepsi aynı kolona işaret ediyor.
		for _, k := range a.keys {
			if col, ok := promotedCols()[k]; ok {
				return col, nil, true
			}
		}
		parts := make([]string, 0, len(a.keys)+1)
		args := make([]any, 0, len(a.keys))
		for _, k := range a.keys {
			parts = append(parts, "nullIf(attr_values[indexOf(attr_keys, ?)], '')")
			args = append(args, k)
		}
		parts = append(parts, "''")
		return "coalesce(" + strings.Join(parts, ", ") + ")", args, true
	}
	return "", nil, false
}

// spansColumnExpr — spans üzerindeki bir kolonun MATERIALIZED ifadesi.
// exists=false ise kolon yok (ya da okunamadı — aynı tarafa düşüyoruz:
// ADD gönderilir, IF NOT EXISTS onu zararsız kılar).
//
// Küme kipinde materialized kolon `spans_local`'da; ikisine de bakıyoruz
// çünkü hangi adın yerel tablo olduğu kuruluma göre değişiyor.
func (s *Store) spansColumnExpr(ctx context.Context, col string) (string, bool) {
	var expr string
	err := s.conn.QueryRow(ctx,
		`SELECT default_expression FROM system.columns
		 WHERE database = currentDatabase()
		   AND table IN ('spans', 'spans_local')
		   AND name = ?
		   AND default_kind = 'MATERIALIZED'
		 LIMIT 1`, col).Scan(&expr)
	if err != nil {
		return "", false
	}
	return expr, true
}

// spansIndexExists — spans üzerinde bu adda bir skip index var mı?
//
// Hata hâlinde false döner: emin olamadığımızda `ADD INDEX IF NOT
// EXISTS` göndermek doğru taraf — fazladan bir no-op zararsız, eksik
// bir indeks yavaş.
func (s *Store) spansIndexExists(ctx context.Context, name string) bool {
	var n uint64 // count() UInt64 döner (v0.9.595 dersi)
	if err := s.conn.QueryRow(ctx,
		`SELECT count() FROM system.data_skipping_indices
		 WHERE database = currentDatabase()
		   AND table IN ('spans', 'spans_local')
		   AND name = ?`, name).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// repairPromotedAttrCols — terfi kolonlarını istenen ifadeye getirir.
//
// Dış Distributed + cluster_name boşken ATLANIR (v0.8.185/186 emsali,
// distributed-column-safety): ALTER yerel tabloya yönlenemez, kolon
// oluşmaz, okuma tarafı zaten probe ile kapalı kalır.
func (s *Store) repairPromotedAttrCols(ctx context.Context) {
	if s.spansIsExternalDistributed(ctx) {
		log.Printf("[chstore] dış Distributed `spans` (cluster_name boş) — terfi attribute kolonları ATLANDI; /traces dizi yolunda kalıyor")
		return
	}
	for _, a := range promotedAttrs {
		have, colExists := s.spansColumnExpr(ctx, a.col)
		idxExists := s.spansIndexExists(ctx, "idx_"+a.col)
		stmts := promotedAttrDDL(a, have, colExists, idxExists)
		if len(stmts) == 0 {
			continue
		}
		if colExists && promotedAttrNeedsRepair(have, a.keys) {
			log.Printf("[chstore] %s ifadesi eksik yazım taşıyor — DROP+ADD ile onarılıyor (eski part'lar okuma anında hesaplanır)", a.col)
		}
		for _, q := range stmts {
			if err := s.execDDL(ctx, q); err != nil {
				// Soft-fail: terfi kolonu + indeksi saf birer hız
				// optimizasyonu. Probe zaten dolmamış kolonu
				// kaydetmiyor, yani başarısızlık "yavaş" demek,
				// "yanlış" değil.
				//
				// Distributed engine'de ADD_INDEX kod 48 döner
				// (cluster_name boşsa adaptDDL _local'e çeviremiyor) —
				// store.go'daki skip-index döngüsüyle aynı gerekçe.
				log.Printf("[chstore] terfi kolonu DDL'i başarısız (yumuşak): %v", err)
				break
			}
		}
		// v0.9.623 — asıl kazanç burada. Kolon tek başına yalnız 2×
		// çünkü YENİ eklenen bir MATERIALIZED kolon eski part'larda
		// SAKLANMAZ, okuma anında diziden hesaplanır. Skip index ise
		// granülü tamamen eliyor:
		//
		//	dizi açma            10.000.000 satır   3.90 GiB   362 ms
		//	kolon                10.000.000 satır   1.98 GiB   204 ms
		//	kolon + set(0)        1.310.720 satır    261 MiB    81 ms
		//
		// idx_kind / idx_db_system / idx_status ile aynı biçim
		// (store.go ~2192): düşük kardinaliteli LowCardinality kolonda
		// `set(0)` TAM eleme yapar, bloom_filter'ın yanlış-pozitifi yok.
		//
		// İndeks YALNIZ YENİ PART'LARA uygulanır. `MATERIALIZE INDEX`
		// BİLİNÇLİ OLARAK ÇALIŞTIRILMIYOR: 30 günlük spans üzerinde
		// milyarlarca satırlık bir mutasyon olurdu. Gerek de yok —
		// tipik pencere 6 saat, yani indeks eklendikten 6 saat sonra
		// o pencere tamamen indeksli. Eski veri doğal olarak dolar.
		if err := s.execDDL(ctx, fmt.Sprintf(
			"ALTER TABLE spans ADD INDEX IF NOT EXISTS idx_%s %s TYPE set(0) GRANULARITY 4",
			a.col, a.col)); err != nil {
			// Distributed engine'de ADD_INDEX kod 48 döner (cluster_name
			// boşsa adaptDDL _local'e çeviremiyor) — store.go'daki
			// skip-index döngüsüyle aynı gerekçe: saf sorgu-zamanı
			// optimizasyonu, eksikliği yavaşlatır ama bozmaz.
			log.Printf("[chstore] %s skip index'i eklenemedi (yumuşak): %v", a.col, err)
		}
	}
}

// probePromotedAttrs — hangi (yazım → kolon) eşleşmelerinin GERÇEKTEN
// doğru olduğunu veriyle kanıtlar.
//
// v0.9.198'in probe'u yalnız `SELECT attr_channel_code … LIMIT 1`
// çalıştırıyordu — yani kolonun VAR OLDUĞUNU kanıtlıyordu, DOLU
// olduğunu değil. Kolon aylardır boş olduğu hâlde probe geçti ve harita
// kaydedildi. Aynı hatayı ben de bir kez yaptım: var olma kanıtını
// dolu olma kanıtı sandım.
//
// Buradaki probe her yazım için AYRI karar veriyor ve kanıt şu:
// "bu anahtarı taşıyan span'lerde kolon, dizi aramasının verdiği
// değerin AYNISINI mı veriyor?" Sayı sıfırdan büyük tek bir uyuşmazlık
// bile o yazımı kaydettirmez.
//
// seen == 0 (o anahtarı taşıyan taze span yok) → KAYDEDİLMEZ. Kanıt
// yokluğunda dizi yolunda kalmak doğru taraf: yavaş ama doğru. Taze
// kurulumda kolonlar ilk veriden sonraki boot'ta devreye girer.
func (s *Store) probePromotedAttrs(ctx context.Context) map[string]string {
	out := map[string]string{}
	for _, a := range promotedAttrs {
		for _, k := range a.keys {
			// count()/countIf() UInt64 döner — int'e taramak derlenir,
			// testleri geçer, yalnız canlıda patlar (v0.9.595 dersi).
			var bad, seen uint64
			q := fmt.Sprintf(`SELECT
				countIf(has(attr_keys, ?) AND %[1]s != attr_values[indexOf(attr_keys, ?)]),
				countIf(has(attr_keys, ?))
			FROM (
				SELECT attr_keys, attr_values, %[1]s FROM spans
				WHERE time >= now() - INTERVAL 30 MINUTE
				LIMIT 50000
			) SETTINGS max_execution_time = 10`, a.col)
			if err := s.conn.QueryRow(ctx, q, k, k, k).Scan(&bad, &seen); err != nil {
				log.Printf("[chstore] %s çözülemedi (%v) — bu attribute dizi yolunda kalıyor", a.col, err)
				break // kolon yok/okunamıyor: bu attribute'un HİÇBİR yazımı kaydedilmez
			}
			if seen == 0 {
				continue
			}
			if bad > 0 {
				log.Printf("[chstore] %s, '%s' yazımını doğru taşımıyor (%d/%d uyuşmazlık) — o filtre dizi yolunda kalıyor", a.col, k, bad, seen)
				continue
			}
			out[k] = a.col
			log.Printf("[chstore] terfi kolonu doğrulandı: '%s' → %s (%d taze span)", k, a.col, seen)
		}
	}
	return out
}
