package api

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// readSourceFile — kaynak metnine bakan testlerin ortak okuyucusu
// (chstore/db_bucket_bound_test.go'daki funcSource ile aynı desen:
// sözleşme çalıştırılabilir bir ifadede değil, kaynağın kendisinde).
func readSourceFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s okunamadı: %v", path, err)
	}
	return string(b)
}

// v0.9.836 regresyon testi — OPERATÖR BİLDİRİMİ.
//
// BULUNAN HATA: `/problems?problem=<id>` tıkında ErrorBoundary
// "Cannot read properties of null (reading 'length')" — sayfanın TAMAMI
// çöküyordu.
//
// Kök neden Go tarafında: chstore.BubbleUp'ın erken dönüşleri
// (selTotal==0 || baseTotal==0, ve len(keys)==0) `&BubbleUpResult{...}`
// döndürüyor ama `Attributes` alanını hiç doldurmuyordu. Go'da NİL DİLİM
// JSON'a `null` çıkar, `[]` değil — ve panel `.attributes.length` derken
// orada patlıyordu. Aynı sınıf ikinci mayın: rootcause handler'ının
// correlations goroutine'i nil dönüşü zarfın başlangıçtaki `[]`'ine
// ATIYORDU, yani `"correlations": null`.
//
// Bu bir SINIR testi: hata Go tipinde görünmez (`[]BubbleUpAttribute`
// hem nil hem boş olabilir ve ikisi de derlenir), yalnız JSON'a
// serialize edilince ortaya çıkar. Bu yüzden test json.Marshal ÜZERİNDEN
// koşuyor — sözleşme tam olarak orada yaşıyor.
//
// Kardeş test: frontend/src/components/RootCausePanel.test.ts (aynı
// sürüm, tüketici tarafındaki tolerans).

// TestBubbleUpResultNeverMarshalsNullAttributes — BubbleUpResult'ın
// gerçekte üretilen ŞEKİLLERİ JSON'da asla `"attributes":null` olamaz.
//
// Tablodaki üç şekil, bubbleup.go'nun üç dönüş yolunun birebir
// karşılığı: iki erken dönüş + ana yolun `out` başlangıcı.
func TestBubbleUpResultNeverMarshalsNullAttributes(t *testing.T) {
	cases := []struct {
		name string
		in   *chstore.BubbleUpResult
	}{
		{
			// selTotal==0 || baseTotal==0 — analiz penceresi boş.
			// ÇÖKMENİN EN SIK TETİKLEYİCİSİ: eski / çözülmüş bir
			// problemin derin linki.
			"erken dönüş: seçim ya da taban boş",
			&chstore.BubbleUpResult{
				SelectionTotal: 0, BaselineTotal: 0,
				Attributes: []chstore.BubbleUpAttribute{},
			},
		},
		{
			// len(keys)==0 — span'ler var ama hepsi yüksek-kardinalite
			// anahtarlar (trace_id vb.) taşıyor, hiçbiri elenmeden
			// geçemiyor.
			"erken dönüş: kullanılabilir attribute anahtarı yok",
			&chstore.BubbleUpResult{
				SelectionTotal: 120, BaselineTotal: 4000,
				Attributes: []chstore.BubbleUpAttribute{},
			},
		},
		{
			// Ana yol: hiçbir attribute `len(attr.Values) > 0`'ı
			// geçemezse append hiç çalışmaz. Erken dönüşleri düzeltip
			// burayı unutmak, hatayı daha nadir ama AYNI şekilde
			// bırakırdı.
			"ana yol: hiçbir attribute eşiği geçemedi",
			&chstore.BubbleUpResult{
				SelectionTotal: 120, BaselineTotal: 4000,
				Attributes: []chstore.BubbleUpAttribute{},
			},
		},
		{
			"dolu sonuç: normal yol bozulmadı",
			&chstore.BubbleUpResult{
				SelectionTotal: 120, BaselineTotal: 4000,
				Attributes: []chstore.BubbleUpAttribute{{
					Key: "http.status_code",
					Values: []chstore.BubbleUpValue{{
						Value: "500", Score: 0.8,
						SelectionPct: 90, BaselinePct: 2,
					}},
				}},
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := json.Marshal(c.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			got := string(b)
			if strings.Contains(got, `"attributes":null`) {
				t.Fatalf(`"attributes":null sızdı — panel .length'te çöker (v0.9.836). JSON: %s`, got)
			}
			if !strings.Contains(got, `"attributes":[`) {
				t.Fatalf(`"attributes" bir DİZİ olmalı. JSON: %s`, got)
			}
		})
	}
}

// TestRootCauseEnvelopeNeverMarshalsNullSlices — handler zarfının
// kendisi. Correlations `omitempty` TAŞIMIYOR (frontend onu her zaman
// bekliyor), yani nil kalırsa `null` olarak gider.
//
// İkinci vaka, düzeltilen ikinci mayının ta kendisi: goroutine'in nil
// ataması. `e == nil && cs != nil` koşulu olmadan başlangıçtaki `[]`
// eziliyordu ve panel `.correlations.filter` derken çöküyordu — aynı
// sınıf, farklı metot.
func TestRootCauseEnvelopeNeverMarshalsNullSlices(t *testing.T) {
	cases := []struct {
		name string
		in   RootCause
	}{
		{
			"boş zarf: hiçbir zenginleştirme dönmedi",
			RootCause{
				ProblemID:    "p1",
				Service:      "checkout",
				Correlations: []chstore.ChangedService{},
			},
		},
		{
			"correlations goroutine'i nil döndü (ezme YOK)",
			func() RootCause {
				rc := RootCause{
					ProblemID:    "p1",
					Service:      "checkout",
					Correlations: []chstore.ChangedService{},
				}
				// Handler'daki korumanın modeli: nil sonuç zarfı EZMEZ.
				var cs []chstore.ChangedService
				if cs != nil {
					rc.Correlations = cs
				}
				return rc
			}(),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := json.Marshal(c.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			got := string(b)
			if strings.Contains(got, `"correlations":null`) {
				t.Fatalf(`"correlations":null sızdı — panel .filter'da çöker (v0.9.836). JSON: %s`, got)
			}
			if !strings.Contains(got, `"correlations":[`) {
				t.Fatalf(`"correlations" bir DİZİ olmalı. JSON: %s`, got)
			}
		})
	}
}

// TestBubbleUpSourceInitialisesAttributes — KAYNAK testi.
//
// Yukarıdaki tablolar ELLE kurulmuş şekilleri doğruluyor; bu test
// bubbleup.go'nun GERÇEKTEN o şekilleri ürettiğini sabitliyor. Gerekçe:
// BubbleUp bir CH bağlantısı istiyor, yani dönüş yolları birim testinde
// çalıştırılamıyor — sözleşme kaynakta yaşıyor.
//
// Üç `&BubbleUpResult{` / `out := &BubbleUpResult{` kurulumunun HEPSİ
// `Attributes:` taşımalı. Yeni bir dönüş yolu eklenip alan unutulursa
// sayaçlar ayrışır ve test kırmızıya döner.
func TestBubbleUpSourceInitialisesAttributes(t *testing.T) {
	src := readSourceFile(t, "../chstore/bubbleup.go")
	ctors := strings.Count(src, "&BubbleUpResult{")
	if ctors == 0 {
		t.Fatal("bubbleup.go içinde BubbleUpResult kurulumu bulunamadı — dosya taşındıysa testi güncelle")
	}
	inits := strings.Count(src, "Attributes:     []BubbleUpAttribute{}")
	if inits != ctors {
		t.Fatalf("BubbleUpResult %d yerde kuruluyor ama yalnız %d tanesi Attributes'i başlatıyor — "+
			"başlatılmayan yol JSON'a `\"attributes\":null` yazar ve panel .length'te çöker (v0.9.836)",
			ctors, inits)
	}
}
