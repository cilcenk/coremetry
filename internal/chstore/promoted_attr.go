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
func promotedAttrDDL(a promotedAttr, have string, exists bool) []string {
	add := fmt.Sprintf(
		"ALTER TABLE spans ADD COLUMN IF NOT EXISTS %s LowCardinality(String) MATERIALIZED %s",
		a.col, promotedAttrExpr(a.keys))
	if !exists {
		return []string{add}
	}
	if !promotedAttrNeedsRepair(have, a.keys) {
		return nil
	}
	return []string{
		fmt.Sprintf("ALTER TABLE spans DROP COLUMN IF EXISTS %s", a.col),
		add,
	}
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
		have, exists := s.spansColumnExpr(ctx, a.col)
		stmts := promotedAttrDDL(a, have, exists)
		if len(stmts) == 0 {
			continue
		}
		if exists {
			log.Printf("[chstore] %s ifadesi eksik yazım taşıyor — DROP+ADD ile onarılıyor (eski part'lar okuma anında hesaplanır)", a.col)
		}
		for _, q := range stmts {
			if err := s.execDDL(ctx, q); err != nil {
				// Soft-fail: terfi kolonu saf bir hız optimizasyonu.
				// Probe zaten dolmamış kolonu kaydetmiyor, yani
				// başarısızlık "yavaş" demek, "yanlış" değil.
				log.Printf("[chstore] terfi kolonu DDL'i başarısız (yumuşak): %v", err)
				break
			}
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
