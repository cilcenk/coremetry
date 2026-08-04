// v0.9.629 — demo iş boyutları: kanal ve fonksiyon kodu.
//
// NEDEN: v0.9.621-628'de kapatılan hata sınıfı LOKALDE YAPISAL OLARAK
// GÖRÜNEMİYORDU. Terfi etmiş attribute kolonları (attr_channel_code /
// attr_function_code), onların skip index'i, /traces filtresi, iş-boyutu
// kırılımı ve rollup GENİŞ ailesinin iki boyutu — hepsi `channel_code` /
// `function_code` attribute'una dayanıyor ve HİÇBİR demo üreteci bu
// attribute'ları YAYMIYORDU.
//
// Sonuç: prod'da aylardır boş dolan bir kolon lokalde hiç test
// edilemedi. Bir hatanın kendini gösterememesi, o hatanın en pahalı
// hâli — hangi denetim koşarsa koşsun veri yoksa bulunamaz.
//
// CLAUDE.md demo gerçekçiliği kuralı: her yeni demo boyutu YÜK
// MODELİNDEN okur (L / loadModel), kendi sabit olasılığından değil.
// Burada iki yerde okunuyor:
//
//   - kanal dağılımı ÇARPIK (gerçek trafik gibi: birkaç kanal hakim,
//     kuyruk uzun), düz uniform değil;
//   - OLAY sırasında dağılım TEK BİR KANALA kayıyor — böylece
//     "hangi kanal patlıyor" kırılımı (BusinessBreakdown, v0.9.511/580)
//     lokalde gerçekten bir şey gösteriyor ve boş dönerse fark edilir.
package main

import (
	"fmt"
	mrand "math/rand/v2"
)

// demoChannels — kanal kodu havuzu ve kümülatif ağırlıkları.
//
// Gerçek trafikteki gibi çarpık: ilk iki kanal hacmin ~%70'i. Düz
// uniform bir dağılım kırılım panelini anlamsız kılardı (sekiz eşit
// çubuk hiçbir şey söylemez) ve LowCardinality kolonun gerçek
// kardinalite davranışını da yansıtmazdı.
var demoChannels = []struct {
	code   string
	weight int
}{
	{"030101", 40}, // internet şubesi
	{"010101", 30}, // mobil
	{"020202", 12}, // ATM
	{"040404", 8},  // çağrı merkezi
	{"050505", 5},  // şube
	{"060606", 3},  // açık bankacılık
	{"070707", 2},  // toplu iş
}

// demoChannelTotal — ağırlıkların toplamı; init'te bir kez.
var demoChannelTotal = func() int {
	n := 0
	for _, c := range demoChannels {
		n += c.weight
	}
	return n
}()

// incidentChannel — olay sırasında hacmin kaydığı kanal.
//
// Gerçek olaylar nadiren her kanalı eşit vurur: bir kanalın altyapısı
// (mobil bff, ATM ağı) bozulur ve o kanal hem hacmi hem hatayı domine
// eder. Kırılım panelinin göstermesi gereken şey tam olarak bu.
const incidentChannel = "010101"

// pickChannelCode — yük modelinden beslenen kanal seçimi.
//
// Olay yokken ağırlıklı dağılım; olay varken çağrıların yarısı
// incidentChannel'a kayıyor. İkinci dal L'den okuyor — kuralın gereği.
func pickChannelCode() string {
	if L.incidentLabel() != "" && mrand.IntN(2) == 0 {
		return incidentChannel
	}
	r := mrand.IntN(demoChannelTotal)
	for _, c := range demoChannels {
		if r < c.weight {
			return c.code
		}
		r -= c.weight
	}
	return demoChannels[0].code
}

// pickFunctionCode — işlem fonksiyon kodu.
//
// channel_code'dan AYRI bir kardinalite sınıfı: rollup tasarımında
// function_code düz String (LowCardinality DEĞİL) çünkü distinct sayısı
// yüksek olabiliyor. Demo bunu yansıtmalı, yoksa lokalde "her iki boyut
// da düşük kardinaliteli" gibi görünür ve rollup'ın kardinalite kararı
// hiç sınanmaz.
func pickFunctionCode() string {
	return fmt.Sprintf("F%04d", 1000+mrand.IntN(400))
}

// withBusinessDims — bir span attribute haritasına iş boyutlarını ekler.
//
// KÜÇÜK HARF, bilinçli: prod'un yazdığı yazım bu (operatör ölçümü —
// 10 dakikalık pencerede 'channel_code' taşıyan 2.67M span,
// 'CHANNEL_CODE' taşıyan sıfır). Demo prod'un yazımını taklit etmezse
// v0.9.621'in düzelttiği uyuşmazlık lokalde yine görünmez olur.
func withBusinessDims(attrs map[string]any, channel, function string) map[string]any {
	if attrs == nil {
		attrs = map[string]any{}
	}
	attrs["channel_code"] = channel
	attrs["function_code"] = function
	return attrs
}
