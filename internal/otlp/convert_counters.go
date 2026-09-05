package otlp

// convert_counters.go — v0.10.388 (dış skill denetimi B2/B5): dönüştürücünün
// SESSİZ degrade dalları sayılır. /otlp-converter §3 kuralı: desteklenmeyen
// bir dal ya taşınır, ya sayaçla degrade edilir, ya sayaçla düşürülür —
// sessiz kalamaz. Eskiden exponential histogram dört koşulda boş kova
// bırakıyor (yüzdelik sessizce avg'a düşüyor) ve bilinmeyen AnyValue tipi
// "" oluyordu; operatör "p99 yanlış" der, sebebi ekranda yoktu.
// /admin/stats: otlp_convert_degrades.

import "sync/atomic"

var convertDegrades struct {
	expHistEmpty    atomic.Uint64 // pozitif kova yok
	expHistNegative atomic.Uint64 // negatif kova taşıyor
	expHistScale    atomic.Uint64 // scale [-10, 20] dışında
	expHistCap      atomic.Uint64 // kova sayısı > maxExpBuckets
	anyValUnknown   atomic.Uint64 // tanınmayan AnyValue tipi → ""
	// deltaTemporality — v0.10.390 (dış skill denetimi B3): delta
	// temporality'li Sum/Histogram noktası. CH `temporality` kolonuna
	// yazar ama VM forward'ı ham gövdeyi aynen iletir ve VM rate()/
	// increase() kümülatif sayaç varsayar → delta kurulumda VM tarafı
	// sistematik yanlış rate. Sayaç > 0 ise collector'da
	// deltatocumulative işlemcisi gerekir (Settings notu ayrı dilim).
	deltaTemporality atomic.Uint64
}

func countDeltaTemporality(t string) {
	if t == "delta" {
		convertDegrades.deltaTemporality.Add(1)
	}
}

// ConvertDegradeCounts — /admin/stats için anlık sayaçlar.
func ConvertDegradeCounts() map[string]uint64 {
	return map[string]uint64{
		"exp_histogram_empty":      convertDegrades.expHistEmpty.Load(),
		"exp_histogram_negative":   convertDegrades.expHistNegative.Load(),
		"exp_histogram_scale":      convertDegrades.expHistScale.Load(),
		"exp_histogram_cap":        convertDegrades.expHistCap.Load(),
		"anyvalue_unknown_type":    convertDegrades.anyValUnknown.Load(),
		"delta_temporality_points": convertDegrades.deltaTemporality.Load(),
	}
}
