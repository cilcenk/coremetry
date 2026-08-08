package anomaly

import "github.com/cilcenk/coremetry/internal/chstore"

// v0.9.826 — eşikler paket sabiti olmaktan çıkıp operatör ayarı olunca
// testlerin de bir "üretim varsayılanı" kaynağına ihtiyacı oldu.
//
// Sabitleri test tarafında YENİDEN YAZMAK yerine gerçek varsayılan
// üreticiyi çağırıyorlar. Fark önemli: varsayılan değiştiğinde testler
// onunla birlikte hareket eder, ama bir vidanın DAVRANIŞINI pinleyen
// testler (aşağıdaki sensitivity testleri) kendi açık değerlerini
// kurduğu için sessizce yeşile dönmez.

// defSens — üretim varsayılanları.
func defSens() chstore.AnomalySensitivityConfig { return chstore.DefaultAnomalySensitivity() }

// defPolicy — bir metriğin varsayılan çözülmüş politikası.
func defPolicy(metric string) metricPolicy { return policyFor(metric, defSens()) }

// defCriticalZ / defDwell — global varsayılanlar.
func defCriticalZ() float64 { return defSens().CriticalZ }
func defDwell() int         { return defSens().DwellBuckets }

// tunedPolicy — tek bir vidası değiştirilmiş politika kurmanın kısa yolu.
// Testler "bu vida ne yapıyor" sorusunu tek satırda sorabilsin diye.
func tunedPolicy(metric string, edit func(*chstore.AnomalyMetricSensitivity)) metricPolicy {
	cfg := defSens()
	s := cfg.For(metric)
	edit(&s)
	cfg.Metrics[metric] = s
	return policyFor(metric, cfg)
}
