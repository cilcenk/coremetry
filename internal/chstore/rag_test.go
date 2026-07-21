package chstore

import (
	"reflect"
	"testing"
)

// v0.9.161 — BM25 köprüsü keyword tokenizer'ı. ragQueryTerms retrieval'ın
// alaka kapısını besler: <3 harf + stopword ele, dedup, Türkçe harfleri
// koru, tavan 12. Yanlış tokenizasyon ya alakasız chunk getirir (halüsinasyon
// zemini) ya da alakalı runbook'u kaçırır.
func TestRagQueryTerms(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"basic + stopwords", "payment-gw'de connection pool nasıl artırılır",
			[]string{"payment", "connection", "pool", "artırılır"}}, // gw<3, de<3, nasıl stopword
		{"dedup + lower", "Pool POOL pool timeout",
			[]string{"pool", "timeout"}},
		{"turkish letters kept", "bağlantı havuzu tükendi",
			[]string{"bağlantı", "havuzu", "tükendi"}},
		{"punctuation split", "checkout-db: p99 latency > 500ms!",
			[]string{"checkout", "p99", "latency", "500ms"}}, // db<3, >500ms split → 500ms
		{"all stopwords/short → empty", "ve ile bu ne mı",
			nil},
		{"digits kept", "http 503 error",
			[]string{"http", "503", "error"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ragQueryTerms(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("ragQueryTerms(%q) = %#v, want %#v", c.in, got, c.want)
			}
		})
	}
}

// Tavan: 12 terimden fazlası kesilir (sorgu-genişlemesi bounded).
func TestRagQueryTermsCap(t *testing.T) {
	got := ragQueryTerms("aaa bbb ccc ddd eee fff ggg hhh iii jjj kkk lll mmm nnn ooo")
	if len(got) != 12 {
		t.Fatalf("cap: len = %d, want 12", len(got))
	}
}
