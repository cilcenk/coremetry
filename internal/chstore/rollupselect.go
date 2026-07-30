package chstore

import (
	"fmt"
	"sort"
	"time"
)

// rollupselect.go — rollup kademe seçicisi (Aşama 2, docs/rollup-design.md §5).
// Saf mantık: pencere + maxDataPoints + istenen boyutlardan hangi rollup
// tablosunun okunacağına ve gerçek step'e karar verir. I/O yok —
// rollupselect_test.go'daki karar tablosuyla pinli. Tabloların var olup
// olmadığı (migrations/0001-0003 uygulanmış mı) OKUMA katmanının işi
// (rollup_read.go probe'u) — seçici yalnız adres üretir.

// RollupQuery — bir rollup okumasının seçici girdisi.
type RollupQuery struct {
	From, To time.Time
	// MaxDataPoints: panel genişliğinden gelen nokta bütçesi; 0 → 1500.
	MaxDataPoints int
	// Dims: istenen breakdown boyutları (filtre VEYA groupBy'da geçen her
	// boyut). Boş = yalnız zaman serisi (service filtresi dahil değildir —
	// service iki ailede de var).
	Dims []string
	// NeedExactQuant: SLO-hassas percentile (tdigest) şartı. Geniş boyut
	// istenmişse karşılanamaz — Pick hata döner, sessizce bucket'a düşmez.
	NeedExactQuant bool
}

// RollupPlan — seçicinin kararı. StepSeconds YANITTA DA döner (grafik-audit
// Faz B {series, stepSeconds} kontratı, types.ts:1892 emsali).
type RollupPlan struct {
	Table        string `json:"table"`
	StepSeconds  int64  `json:"stepSeconds"`
	QuantileMode string `json:"quantileMode"` // "tdigest" | "buckets"
	Reason       string `json:"reason"`
	// PartialWindow: From, seçilen kademenin TTL penceresinden eski —
	// seri baş tarafı boş dönebilir (backfill sınırı, tasarım §6).
	PartialWindow bool `json:"partialWindow,omitempty"`
}

// rollupTier — bir kademenin tabanı ve retention penceresi.
type rollupTier struct {
	table     string
	baseSec   int64
	retention time.Duration
}

const rollupDay = 24 * time.Hour

// Kademe tabloları TTL'leriyle (migrations/0001-0002). 13 ay ≈ 396 gün.
var narrowTiers = []rollupTier{
	{"rollup_spans_narrow_10s", 10, 7 * rollupDay},
	{"rollup_spans_narrow_1m", 60, 14 * rollupDay},
	{"rollup_spans_narrow_5m", 300, 90 * rollupDay},
	{"rollup_spans_narrow_1h", 3600, 396 * rollupDay},
}

var wideTiers = []rollupTier{
	{"rollup_spans_wide_1m", 60, 14 * rollupDay},
	{"rollup_spans_wide_5m", 300, 90 * rollupDay},
	{"rollup_spans_wide_1h", 3600, 396 * rollupDay},
}

// narrowDims — DAR ailenin cevaplayabildiği boyutlar; wideOnlyDims GENİŞ
// aileyi zorunlu kılar. Bilinmeyen boyut = hata (sessiz yok-sayma yok).
var narrowDims = map[string]bool{"service_name": true, "span_kind": true, "status_code": true}
var wideOnlyDims = map[string]bool{"endpoint": true, "channel_code": true, "function_code": true}

// PickRollup — karar sırası (tasarım §5): aile → kademe → step yuvarlama.
func PickRollup(q RollupQuery, now time.Time) (RollupPlan, error) {
	if !q.To.After(q.From) {
		return RollupPlan{}, fmt.Errorf("rollup: pencere ters ya da boş (from=%s to=%s)", q.From, q.To)
	}
	mdp := q.MaxDataPoints
	if mdp <= 0 {
		mdp = 1500
	}
	if mdp > 4000 { // queryMetric ile aynı üst clamp
		mdp = 4000
	}

	wide := false
	for _, d := range q.Dims {
		switch {
		case wideOnlyDims[d]:
			wide = true
		case narrowDims[d]:
			// iki ailede de var
		default:
			return RollupPlan{}, fmt.Errorf("rollup: bilinmeyen boyut %q (dar: service_name/span_kind/status_code, geniş: +endpoint/channel_code/function_code)", d)
		}
	}
	if wide && q.NeedExactQuant {
		return RollupPlan{}, fmt.Errorf("rollup: geniş boyutlarla tam-quantile (tdigest) yok — bucket kestirimi kabul edilmeli ya da boyutlar daraltılmalı")
	}

	tiers := narrowTiers
	mode := "tdigest"
	if wide {
		tiers = wideTiers
		mode = "buckets"
	}

	windowSec := int64(q.To.Sub(q.From) / time.Second)
	idealStep := (windowSec + int64(mdp) - 1) / int64(mdp)

	// Kademe: tabanı idealStep'i aşmayan EN BÜYÜK kademe; retention From'u
	// kapsamıyorsa daha kaba kademeye zorlanır. Hiçbiri kapsamıyorsa en
	// kaba kademe + PartialWindow (13 ay öncesi zaten hiçbir yerde yok).
	oldest := now.Sub(q.From)
	pick := pickTier(tiers, idealStep, oldest)

	step := idealStep
	if step < pick.baseSec {
		step = pick.baseSec
	}
	// tabanın tam katına yukarı yuvarla (toStartOfInterval hizası)
	if rem := step % pick.baseSec; rem != 0 {
		step += pick.baseSec - rem
	}
	// Yükseltme geçişi: yuvarlanmış step daha KABA bir kademenin tam
	// katıysa ve o kademe retention'ı kapsıyorsa oraya geç — çıktı birebir
	// aynı (aynı bucket sınırları), tarama maliyeti kat kat düşük (24h
	// penceresini 10s yerine 1m tablosundan okumak 6× az satır). Karar
	// tablosundaki (tasarım §5) kademe sınırlarını da bu geçiş kurar.
	for _, t := range tiers {
		if t.baseSec > pick.baseSec && step%t.baseSec == 0 && oldest <= t.retention {
			pick = t
		}
	}

	partial := oldest > pick.retention
	reason := fmt.Sprintf("pencere=%s idealStep=%ds → %s (taban %ds, %s)",
		q.To.Sub(q.From).Truncate(time.Second), idealStep, pick.table, pick.baseSec, mode)
	if partial {
		reason += " — DİKKAT: pencere başı retention dışında, seri kısmi (backfill sınırı)"
	}
	return RollupPlan{
		Table: pick.table, StepSeconds: step,
		QuantileMode: mode, Reason: reason, PartialWindow: partial,
	}, nil
}

// pickTier — kademe seçiminin saf çekirdeği. Kurallar:
//  1. Adaylar: retention'ı pencere başını KAPSAYAN kademeler.
//  2. Aday varsa: tabanı idealStep'i aşmayanların en büyüğü; hepsi
//     idealStep'ten büyükse en incesi (step aşağıda tabana yuvarlanır).
//  3. Hiçbir kademe kapsamıyorsa: en uzun retention'lı (en kaba) kademe —
//     PartialWindow çağıranda işaretlenir.
func pickTier(tiers []rollupTier, idealStep int64, oldest time.Duration) rollupTier {
	covering := make([]rollupTier, 0, len(tiers))
	for _, t := range tiers {
		if oldest <= t.retention {
			covering = append(covering, t)
		}
	}
	if len(covering) == 0 {
		best := tiers[0]
		for _, t := range tiers {
			if t.retention > best.retention {
				best = t
			}
		}
		return best
	}
	sort.Slice(covering, func(i, j int) bool { return covering[i].baseSec < covering[j].baseSec })
	pick := covering[0]
	for _, t := range covering {
		if t.baseSec <= idealStep {
			pick = t
		}
	}
	return pick
}
