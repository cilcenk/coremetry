package influx

// ratio.go — v0.10.532: türetilmiş oran serisi (operatör 2026-09-07: hata
// oranı = TFAIL ÷ toplam). SAF: iki Flux sorgusunun HAM kayıtları (watermark
// ÖNCESİ, aynı tikten) (groupBy değerleri, _time) anahtarında birleşir; çıktı
// yine Record'dur ki SplitBuckets + BuildMetricsRequest aynen çalışsın (kendi
// watermark anahtarıyla). Influx'a ek sorgu YOK.
//
// Üç koruyucu, üçü de sayılır (sessiz düşürme yok):
//   • payda < MinDenominator → kova YAZILMAZ (1/2 = %50 gürültüsü); 0 değil.
//   • pay satırı yok → %0: pay sorgusu createEmpty:false ile kova üretmez,
//     hata yok = sıfır hata (audit R3 ile aynı okuma).
//   • settle: kaynak gecikmeli yazar; pay henüz yazılmamışken payda geldi
//     diye %0 basmak, watermark yüzünden ASLA düzelmezdi. Pay satır verdiyse
//     payın en yeni kovasından SONRAKİ payda kovaları bekler; pay hiç satır
//     vermediyse paydanın en yeni SettleBuckets kovası bekler (kova genişliği
//     paydanın zaman damgalarından çıkarılır, tek kova varsa 1 dk).

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// RatioScale — değer yüzde (0–100): dış eşik varsayılanları (MinAbsDelta
	// 5 = 5 puan, MinMAD 1 = 1 puan) oranla bu ölçekte oturur.
	RatioScale                 = 100.0
	RatioDefaultMinDenominator = 20.0
	RatioDefaultSettle         = 2
	ratioMaxSettle             = 60
	ratioDefaultBucket         = time.Minute
)

// RatioStats — birleşimin saydıkları (durum kartı + log).
type RatioStats struct {
	Joined     int // yazılan kova
	LowDen     int // payda < min → atlandı
	Unsettled  int // henüz oturmamış payda kovası → bekliyor
	NoDen      int // pay var, payda hiç yok (bilgi)
	BadValue   int // _value sayı değil
	MissingTag int // groupBy tag'i boş
}

// Skipped — bilinçli atlananlar (drop değil).
func (r RatioStats) Skipped() int { return r.LowDen + r.Unsettled + r.NoDen }

func (r RatioSpec) minDen() float64 {
	if r.MinDenominator > 0 {
		return r.MinDenominator
	}
	return RatioDefaultMinDenominator
}

func (r RatioSpec) settle() int {
	if r.SettleBuckets > 0 {
		return r.SettleBuckets
	}
	return RatioDefaultSettle
}

// ratioKey — (groupBy değerleri…, _time); boş tag = eşleşmez.
func ratioKey(r Record, groupBy []string) (string, bool) {
	parts := make([]string, 0, len(groupBy)+1)
	for _, g := range groupBy {
		v := r.Values[g]
		if v == "" {
			return "", false
		}
		parts = append(parts, v)
	}
	parts = append(parts, r.Values["_time"])
	return strings.Join(parts, "\x1f"), true
}

func ratioNewest(recs []Record) time.Time {
	var n time.Time
	for _, r := range recs {
		if t := recordTime(r); t.After(n) {
			n = t
		}
	}
	return n
}

// bucketWidth — paydanın zaman damgaları arasındaki en küçük pozitif fark;
// <2 damga → 1 dk.
func bucketWidth(recs []Record) time.Duration {
	seen := map[int64]bool{}
	ts := make([]time.Time, 0, len(recs))
	for _, r := range recs {
		if t := recordTime(r); !t.IsZero() && !seen[t.UnixNano()] {
			seen[t.UnixNano()] = true
			ts = append(ts, t)
		}
	}
	if len(ts) < 2 {
		return ratioDefaultBucket
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].Before(ts[j]) })
	best := time.Duration(0)
	for i := 1; i < len(ts); i++ {
		if d := ts[i].Sub(ts[i-1]); d > 0 && (best == 0 || d < best) {
			best = d
		}
	}
	if best == 0 {
		return ratioDefaultBucket
	}
	return best
}

// BuildRatioRecords — SAF. Çıktı (_time, anahtar) sırasıyla deterministik;
// `_time`'sız kayıtlar (pencere yok, düz sum) "" zamanında birleşir ve
// bekletilmez.
func BuildRatioRecords(num, den []Record, groupBy []string, spec RatioSpec) ([]Record, RatioStats) {
	var st RatioStats
	numBy := map[string]float64{}
	var numNewest time.Time
	for _, r := range num {
		k, ok := ratioKey(r, groupBy)
		if !ok {
			st.MissingTag++
			continue
		}
		v, err := strconv.ParseFloat(r.Values["_value"], 64)
		if err != nil {
			st.BadValue++
			continue
		}
		numBy[k] += v
		if t := recordTime(r); t.After(numNewest) {
			numNewest = t
		}
	}
	var cutoff time.Time // bundan SONRAKİ payda kovaları bekler
	if !numNewest.IsZero() {
		cutoff = numNewest
	} else if dn := ratioNewest(den); !dn.IsZero() {
		cutoff = dn.Add(-time.Duration(spec.settle()) * bucketWidth(den))
	}
	minDen := spec.minDen()
	seenDen := map[string]bool{}
	out := make([]Record, 0, len(den))
	for _, r := range den {
		k, ok := ratioKey(r, groupBy)
		if !ok {
			st.MissingTag++
			continue
		}
		seenDen[k] = true
		d, err := strconv.ParseFloat(r.Values["_value"], 64)
		if err != nil {
			st.BadValue++
			continue
		}
		if t := recordTime(r); !cutoff.IsZero() && t.After(cutoff) {
			st.Unsettled++
			continue
		}
		if d <= 0 || d < minDen {
			st.LowDen++
			continue
		}
		vals := make(map[string]string, len(groupBy)+2)
		for _, g := range groupBy {
			vals[g] = r.Values[g]
		}
		if t := r.Values["_time"]; t != "" {
			vals["_time"] = t
		}
		vals["_value"] = strconv.FormatFloat(RatioScale*numBy[k]/d, 'f', -1, 64)
		out = append(out, Record{Table: r.Table, Values: vals})
		st.Joined++
	}
	for k := range numBy {
		if !seenDen[k] {
			st.NoDen++
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := out[i].Values["_time"], out[j].Values["_time"]; a != b {
			return a < b
		}
		ki, _ := ratioKey(out[i], groupBy)
		kj, _ := ratioKey(out[j], groupBy)
		return ki < kj
	})
	return out, st
}
