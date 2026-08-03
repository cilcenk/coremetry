// v0.9.580 — korelasyon kimliği örnekleri.
//
// Operatör: "CoSRE örnek request_id, CHANNEL_CODE değerlerini de
// söylesin." Bir cevabın eyleme dönüşebilmesi için operatörün
// ARAYABİLECEĞİ bir şey vermesi gerekir.
//
// Saf çekirdek ayrı test ediliyor çünkü bu oturumda İKİ KEZ (v0.9.543,
// v0.9.572) SQL'e dokunmayan testler yüzünden çalışmayan kod
// gönderildi. Ayrım bilinçli: seçim mantığı burada, sorgu orada.
package chstore

import "testing"

func TestIsCorrelationKey(t *testing.T) {
	yes := []string{
		"request_id", "requestId", "http.request_id", "x-request-id",
		"correlation_id", "correlationId", "islem_no", "transaction_id", "txid",
	}
	for _, k := range yes {
		if !isCorrelationKey(k) {
			t.Errorf("%q korelasyon anahtarı sayılmadı — operatöre arayabileceği "+
				"bir kimlik verilemez", k)
		}
	}

	// trace/span BİLEREK dışarıda: cevabın başka yerinde (exemplar)
	// zaten veriliyor, burada tekrarlamak yer kaplar.
	no := []string{
		"trace_id", "traceId", "span_id", "spanID",
		"http.method", "db.system", "service.name", "CHANNEL_CODE",
	}
	for _, k := range no {
		if isCorrelationKey(k) {
			t.Errorf("%q korelasyon anahtarı sayıldı — prompt gereksiz "+
				"tekrarla şişer", k)
		}
	}
}

func TestPickCorrelationSamples(t *testing.T) {
	rows := [][2][]string{
		{{"request_id", "http.method"}, {"aaa", "GET"}},
		{{"request_id", "http.method"}, {"bbb", "POST"}},
		{{"request_id"}, {"aaa"}}, // TEKRAR — atlanmalı
		{{"request_id"}, {"ccc"}},
		{{"request_id"}, {"ddd"}}, // sınırın üstü
		{{"correlation_id"}, {"xyz"}},
		{{"trace_id"}, {"deadbeef"}}, // korelasyon DEĞİL
	}
	got := pickCorrelationSamples(rows)

	if len(got) != 2 {
		t.Fatalf("%d anahtar, beklenen 2 (request_id + correlation_id): %+v", len(got), got)
	}
	// Deterministik sıra: aynı girdi her çağrıda aynı çıktı vermeli,
	// yoksa prompt her seferinde değişir ve cache anahtarları ıskalar.
	if got[0].Key != "correlation_id" || got[1].Key != "request_id" {
		t.Errorf("sıra deterministik değil: %+v", got)
	}
	req := got[1]
	if len(req.Values) != corrSampleValuesPerKey {
		t.Errorf("%d örnek, beklenen %d (sınır uygulanmadı): %v",
			len(req.Values), corrSampleValuesPerKey, req.Values)
	}
	for _, v := range req.Values {
		if v == "deadbeef" {
			t.Error("trace_id değeri korelasyon örneklerine sızmış")
		}
	}
	// Tekrar eden "aaa" bir kez geçmeli.
	n := 0
	for _, v := range req.Values {
		if v == "aaa" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("tekrar eden değer %d kez geçmiş", n)
	}
}

func TestPickCorrelationSamplesEmpty(t *testing.T) {
	if got := pickCorrelationSamples(nil); len(got) != 0 {
		t.Errorf("boş girdide %d örnek üretilmiş — uydurma yok olmalı", len(got))
	}
	// Anahtar var ama değer boş → örnek YOK (boş string gösterilmez).
	got := pickCorrelationSamples([][2][]string{{{"request_id"}, {"   "}}})
	if len(got) != 0 {
		t.Errorf("boş değer örnek sayıldı: %+v", got)
	}
}

// Anahtar/değer dizileri farklı uzunlukta gelirse panik OLMAMALI —
// attr dizileri CH'den geliyor ve hizasızlık teorik olarak mümkün.
func TestPickCorrelationSamplesMismatchedArrays(t *testing.T) {
	got := pickCorrelationSamples([][2][]string{
		{{"request_id", "extra_key"}, {"aaa"}}, // değer eksik
	})
	if len(got) != 1 || got[0].Values[0] != "aaa" {
		t.Errorf("hizasız dizide beklenmeyen sonuç: %+v", got)
	}
}
