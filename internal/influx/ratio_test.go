package influx

import (
	"strconv"
	"testing"
)

// v0.10.532 — türetilmiş oran (operatör 2026-09-07: TFAIL ÷ toplam). Saf
// birleşimin dört koruyucusu tablo-testli: payda tabanı, pay yok = %0,
// settle (payın gerisindeki payda kovaları bekler), yön/sıra.
func rr(t, val string, tags ...string) Record {
	v := map[string]string{"_value": val}
	if t != "" {
		v["_time"] = t
	}
	for i := 0; i+1 < len(tags); i += 2 {
		v[tags[i]] = tags[i+1]
	}
	return Record{Values: v}
}

func ratioVals(recs []Record, groupBy []string) map[string]string {
	out := map[string]string{}
	for _, r := range recs {
		k, _ := ratioKey(r, groupBy)
		out[k] = r.Values["_value"]
	}
	return out
}

func TestBuildRatioRecords_JoinAndGuards(t *testing.T) {
	gb := []string{"KANALKOD", "OPERATIONCODE"}
	t1, t2 := "2026-09-07T09:41:00Z", "2026-09-07T09:42:00Z"
	num := []Record{
		rr(t1, "5", "KANALKOD", "01", "OPERATIONCODE", "OP1"),
		rr(t2, "3", "KANALKOD", "01", "OPERATIONCODE", "OP1"),
		rr(t1, "x", "KANALKOD", "01", "OPERATIONCODE", "OP9"), // kötü değer
		rr(t1, "1", "KANALKOD", "", "OPERATIONCODE", "OP1"),   // eksik tag
		rr(t1, "2", "KANALKOD", "01", "OPERATIONCODE", "OPX"), // paydasız pay
	}
	den := []Record{
		rr(t1, "50", "KANALKOD", "01", "OPERATIONCODE", "OP1"),  // 10 %
		rr(t2, "100", "KANALKOD", "01", "OPERATIONCODE", "OP1"), // 3 %
		rr(t1, "40", "KANALKOD", "01", "OPERATIONCODE", "OP2"),  // pay yok → 0 %
		rr(t1, "4", "KANALKOD", "01", "OPERATIONCODE", "OP3"),   // payda < 20 → atla
		rr(t1, "0", "KANALKOD", "01", "OPERATIONCODE", "OP4"),   // payda 0 → atla
	}
	out, st := BuildRatioRecords(num, den, gb, RatioSpec{})
	got := ratioVals(out, gb)
	want := map[string]string{
		"01\x1fOP1\x1f" + t1: "10",
		"01\x1fOP1\x1f" + t2: "3",
		"01\x1fOP2\x1f" + t1: "0",
	}
	if len(got) != len(want) {
		t.Fatalf("kova sayısı %d, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%q = %q, want %q", k, got[k], v)
		}
	}
	if st.Joined != 3 || st.LowDen != 2 || st.NoDen != 1 || st.BadValue != 1 || st.MissingTag != 1 || st.Unsettled != 0 {
		t.Errorf("stats: %+v", st)
	}
	// Çıktı kayıtları yalnız groupBy + _time + _value taşır (BuildMetricsRequest sözleşmesi).
	for _, r := range out {
		if len(r.Values) != len(gb)+2 {
			t.Errorf("fazla kolon: %v", r.Values)
		}
	}
	// Deterministik sıra: _time, sonra anahtar.
	if out[0].Values["OPERATIONCODE"] != "OP1" || out[1].Values["OPERATIONCODE"] != "OP2" || out[2].Values["_time"] != t2 {
		t.Errorf("sıra: %v", out)
	}
}

// Settle 1/2: pay satır verdiyse payın en yeni kovasından SONRAKİ payda
// kovaları bekler — sahte %0 basılıp watermark'la mühürlenmesin.
func TestBuildRatioRecords_SettleBehindNumerator(t *testing.T) {
	gb := []string{"OP"}
	t1, t2, t3 := "2026-09-07T09:41:00Z", "2026-09-07T09:42:00Z", "2026-09-07T09:43:00Z"
	num := []Record{rr(t1, "5", "OP", "A")}
	den := []Record{rr(t1, "50", "OP", "A"), rr(t2, "50", "OP", "A"), rr(t3, "50", "OP", "A")}
	out, st := BuildRatioRecords(num, den, gb, RatioSpec{})
	if len(out) != 1 || out[0].Values["_time"] != t1 || st.Unsettled != 2 {
		t.Fatalf("yalnız t1 yazılır, t2/t3 bekler: %v %+v", out, st)
	}
	// Bir sonraki poll: pay t3'e ilerledi → t2 pay yok = %0, t3 = %4.
	num = append(num, rr(t3, "2", "OP", "A"))
	out, st = BuildRatioRecords(num, den, gb, RatioSpec{})
	got := ratioVals(out, gb)
	if len(out) != 3 || got["A\x1f"+t2] != "0" || got["A\x1f"+t3] != "4" || st.Unsettled != 0 {
		t.Fatalf("pay ilerleyince kovalar açılır: %v %+v", got, st)
	}
}

// Settle 2/2: pay HİÇ satır vermediyse (2 saatte hata yok) paydanın en yeni
// N kovası bekler; kova genişliği paydanın damgalarından çıkar.
func TestBuildRatioRecords_SettleWhenNumeratorEmpty(t *testing.T) {
	gb := []string{"OP"}
	var den []Record
	for m := 0; m < 6; m++ {
		den = append(den, rr("2026-09-07T09:4"+strconv.Itoa(m)+":00Z", "50", "OP", "A"))
	}
	out, st := BuildRatioRecords(nil, den, gb, RatioSpec{SettleBuckets: 2})
	if len(out) != 4 || st.Unsettled != 2 {
		t.Fatalf("son 2 kova bekler, 4 kova %%0 yazılır: %d %+v", len(out), st)
	}
	for _, r := range out {
		if r.Values["_value"] != "0" {
			t.Errorf("pay yok = %%0, got %v", r.Values)
		}
	}
	// Varsayılan settle 2 (RatioSpec sıfır) aynı sonucu verir.
	out2, _ := BuildRatioRecords(nil, den, gb, RatioSpec{})
	if len(out2) != len(out) {
		t.Errorf("varsayılan settle %d, got %d kova", RatioDefaultSettle, len(out2))
	}
	if bucketWidth(den).Minutes() != 1 {
		t.Errorf("kova genişliği 1 dk, got %v", bucketWidth(den))
	}
	if bucketWidth(den[:1]).Minutes() != 1 {
		t.Errorf("tek damga → 1 dk varsayılan")
	}
}

// _time'sız kayıtlar (düz sum) "" zamanında birleşir ve bekletilmez.
func TestBuildRatioRecords_NoTime(t *testing.T) {
	gb := []string{"OP"}
	out, st := BuildRatioRecords([]Record{rr("", "1", "OP", "A")}, []Record{rr("", "25", "OP", "A")}, gb, RatioSpec{MinDenominator: 1})
	if len(out) != 1 || out[0].Values["_value"] != "4" || out[0].Values["_time"] != "" || st.Unsettled != 0 {
		t.Fatalf("%v %+v", out, st)
	}
}

// v0.10.548 — pay oranın anahtarından FAZLA boyut taşıyorsa (TFAIL: KANALKOD ×
// FUNCTIONCODE × OPERATIONCODE; toplam: KANALKOD × OPERATIONCODE) birleşim
// paydanın anahtarında toplar: aynı kanal/operasyon/zaman kovasındaki iki
// fonksiyon satırı (2 + 3) ÷ 50 = %10. Fazla boyut çıktıda yer almaz.
func TestBuildRatioRecords_NumeratorExtraDimensionSums(t *testing.T) {
	gb := []string{"KANALKOD", "OPERATIONCODE"}
	num := []Record{
		rr("2026-09-08T06:00:00Z", "2", "KANALKOD", "01", "OPERATIONCODE", "op1", "FUNCTIONCODE", "f1"),
		rr("2026-09-08T06:00:00Z", "3", "KANALKOD", "01", "OPERATIONCODE", "op1", "FUNCTIONCODE", "f2"),
	}
	den := []Record{rr("2026-09-08T06:00:00Z", "50", "KANALKOD", "01", "OPERATIONCODE", "op1")}
	out, st := BuildRatioRecords(num, den, gb, RatioSpec{})
	if st.Joined != 1 || st.MissingTag != 0 || len(out) != 1 {
		t.Fatalf("birleşim: %+v %d", st, len(out))
	}
	if v := out[0].Values["_value"]; v != "10" {
		t.Fatalf("oran %s, 10 bekleniyordu", v)
	}
	if _, ok := out[0].Values["FUNCTIONCODE"]; ok {
		t.Fatal("fazla boyut çıktıya sızmamalı")
	}
}
