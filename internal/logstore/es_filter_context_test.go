package logstore

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// v0.10.428 — log arama denetimi A3: yüklemler filtre bağlamında
// (bool.filter), yalnız serbest metin must'ta; boş sorgu match_all.
// Sonuç kümesi aynı (hiçbir okuma _score kullanmaz — sıralama zaman
// damgası); değişen şey skor hesabı ve filtre önbelleği.
func TestBuildQueryFilterContext(t *testing.T) {
	s := &ESStore{fields: ESFieldMap{Timestamp: "@timestamp", Body: "message", SeverityNo: "severity_no"}}
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	raw, err := json.Marshal(s.buildQuery(Filter{Service: "checkout", From: from, To: from.Add(time.Hour), SeverityMin: 17, Search: "timeout"}))
	if err != nil {
		t.Fatal(err)
	}
	var q struct {
		Bool struct {
			Filter []map[string]any `json:"filter"`
			Must   []map[string]any `json:"must"`
		} `json:"bool"`
	}
	if err := json.Unmarshal(raw, &q); err != nil {
		t.Fatal(err)
	}
	if len(q.Bool.Filter) < 3 {
		t.Fatalf("zaman + servis + seviye filtre bağlamında olmalı: %s", raw)
	}
	if len(q.Bool.Must) != 1 || q.Bool.Must[0]["query_string"] == nil {
		t.Fatalf("must yalnız serbest metin query_string taşımalı: %s", raw)
	}
	for _, f := range q.Bool.Filter {
		if f["query_string"] != nil {
			t.Fatalf("query_string filtre bağlamına girmemeli: %s", raw)
		}
	}
	js := string(raw)
	if !strings.Contains(js, `"range":{"@timestamp"`) && !strings.Contains(js, `"range":{"`) {
		t.Fatalf("zaman aralığı yok: %s", js)
	}

	// Serbest metin yoksa must anahtarı hiç yazılmaz.
	raw2, _ := json.Marshal(s.buildQuery(Filter{Service: "checkout"}))
	if strings.Contains(string(raw2), `"must":[`) && !strings.Contains(string(raw2), `"must_not"`) {
		t.Fatalf("serbest metin yokken üst düzey must olmamalı: %s", raw2)
	}
	if !strings.Contains(string(raw2), `"filter":[`) {
		t.Fatalf("servis filtresi filtre bağlamında olmalı: %s", raw2)
	}
	// Boş süzgeç: match_all korunur.
	raw3, _ := json.Marshal(s.buildQuery(Filter{}))
	if string(raw3) != `{"match_all":{}}` {
		t.Fatalf("boş süzgeç match_all olmalı: %s", raw3)
	}
	// Yalnız serbest metin: filter anahtarı yok, must var.
	raw4, _ := json.Marshal(s.buildQuery(Filter{Search: "x"}))
	if strings.Contains(string(raw4), `"filter"`) || !strings.Contains(string(raw4), `"must":[{"query_string"`) {
		t.Fatalf("yalnız serbest metin: %s", raw4)
	}
}
