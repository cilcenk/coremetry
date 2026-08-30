package main

// body_test.go — v0.10.188: perfcheck P5 gövdesi FE'nin opt-in'ini taşır.
// v0.10.186 sütunsal kodlamayı gövdede `enc:"col"` ile açtı; probe eski
// gövdeyi gönderince 1,33 MB'lık «değişmedi» ölçtü. Bu test gövdenin
// enc/from/to/requests dörtlüsünü pinler — probe tarayıcıyla aynı şeyi
// istemeli.

import (
	"encoding/json"
	"testing"
)

func TestBundleBodyCarriesColumnarOptIn(t *testing.T) {
	b, err := bundleBody(10, 20, []map[string]any{{"id": "p1", "type": "spanMetric", "agg": "count", "step": 15}})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["enc"] != "col" {
		t.Fatalf("enc=%v, FE gövdesi enc:\"col\" gönderir (lib/api.ts dashboardData)", got["enc"])
	}
	if got["from"] != float64(10) || got["to"] != float64(20) {
		t.Fatalf("from/to kaybı: %v", got)
	}
	rs, _ := got["requests"].([]any)
	if len(rs) != 1 {
		t.Fatalf("requests=%v", got["requests"])
	}
}
