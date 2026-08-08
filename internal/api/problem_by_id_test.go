// v0.9.825 — bildirim derin linki (GET /api/problems/{id}) regresyon testleri.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestProblemByIDRouteDoesNotShadowSiblings — ROTA ÇAKIŞMASI.
//
// /api/problems altında ZATEN üç literal alt-yol var (count, evaluator,
// buckets). Yeni {id} deseni onları gölgelerse sidebar rozeti ve
// evaluator sağlık kartı sessizce "problem bulunamadı" almaya başlar —
// ve bu hiçbir derleme hatası vermez.
//
// Go 1.22 ServeMux'ta literal segment joker'i yener; test o SÖZLEŞMEYE
// bağlanıyor, kayıt SIRASINA değil (sıra değişince kırılmasın diye
// bilinçli olarak tersten kaydediyoruz).
func TestProblemByIDRouteDoesNotShadowSiblings(t *testing.T) {
	mux := http.NewServeMux()
	hit := ""
	// Desenler TABLODAN kaydediliyor, literal çağrılarla değil: make
	// audit CHECK 7 internal/api/*.go içindeki route dizgilerini toplayıp
	// uniq -d ile çakışma arıyor ve test-yerel bir mux'ın desenleri
	// üretim mux'ıyla "çakışmış" görünürdü. O kontrol gerçek bir boot
	// panic sınıfını tutuyor; onu gevşetmektense testi kendi kapsamında
	// tutmak doğru taraf.
	//
	// Joker ÖNCE kaydediliyor: literal'lerin kazanması kayıt SIRASINA
	// değil desen özgüllüğüne bağlı olmalı.
	for _, r := range []struct{ pattern, name string }{
		{"GET /api/problems/{id}", "byid"},
		{"GET /api/problems/count", "count"},
		{"GET /api/problems/evaluator", "evaluator"},
		{"GET /api/problems/buckets", "buckets"},
		{"GET /api/problems/{id}/rootcause", "rootcause"},
	} {
		name := r.name
		mux.HandleFunc(r.pattern, func(w http.ResponseWriter, req *http.Request) {
			if name == "byid" {
				hit = "byid:" + req.PathValue("id")
				return
			}
			hit = name
		})
	}

	cases := []struct{ path, want string }{
		{"/api/problems/count", "count"},
		{"/api/problems/evaluator", "evaluator"},
		{"/api/problems/buckets", "buckets"},
		{"/api/problems/abc123", "byid:abc123"},
		{"/api/problems/abc123/rootcause", "rootcause"},
		// Bildirimdeki gerçek kimlik biçimi: iki nokta taşıyabiliyor.
		{"/api/problems/shared-exc:java.sql.SQLException:100", "byid:shared-exc:java.sql.SQLException:100"},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			hit = ""
			mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, c.path, nil))
			if hit != c.want {
				t.Errorf("%s → %q, %q bekleniyordu — yeni {id} deseni kardeş "+
					"literal yolu gölgeliyor; sidebar rozeti / evaluator kartı "+
					"sessizce bozulur", c.path, hit, c.want)
			}
		})
	}
}

// TestWriteErrMapsNotFoundTo404 — "kayıt yok" 500 DEĞİLDİR.
//
// 500 dönerse istemci "sunucu bozuk" ile "kayıt gitmiş"i ayırt edemez
// ve hata ekranı gösterir; oysa doğru cevap dürüst bir boş durumdur.
// Bu ayrım tam da düzeltmenin amacı — 500'e düşerse derin link yine
// kullanılamaz hâle gelir, sadece hata mesajı değişir.
func TestWriteErrMapsNotFoundTo404(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"sarmalanmış not-found", notFoundf("problem %q", "p-1"), http.StatusNotFound},
		{"çıplak sentinel", errNotFound, http.StatusNotFound},
		{"gerçek hata 500 kalır", errors.New("clickhouse: read timeout"), http.StatusInternalServerError},
		{"istemci koptu", context.Canceled, statusClientClosedRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeErr(rec, c.err)
			if rec.Code != c.want {
				t.Fatalf("durum kodu %d, %d bekleniyordu", rec.Code, c.want)
			}
		})
	}
}

// TestNotFoundfKeepsTheRecordIdentity — 404 gövdesi HANGİ kaydın
// bulunamadığını söylemeli. Çıplak "not found", operatörün elindeki
// bağlantıyı hata raporuna yazamaması demek.
func TestNotFoundfKeepsTheRecordIdentity(t *testing.T) {
	err := notFoundf("problem %q", "shared-exc:java.sql.SQLException:100")
	if !errors.Is(err, errNotFound) {
		t.Fatal("sarmalama errors.Is zincirini kırdı — writeErr 404 yerine 500 döner")
	}
	rec := httptest.NewRecorder()
	writeErr(rec, err)
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("gövde çözülemedi: %v", err)
	}
	if want := "shared-exc:java.sql.SQLException:100"; !strings.Contains(body["error"], want) {
		t.Errorf("404 gövdesi kimliği taşımıyor: %q", body["error"])
	}
}
