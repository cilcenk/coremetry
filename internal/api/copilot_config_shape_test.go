// v0.9.1037 — /api/copilot/config yanıt ŞEKLİNİN kilidi.
//
// Bu sürüm yanıta `model` ekledi. Eklenen her alan bir SIZINTI SORUSU
// doğurur: "bunu kim görebiliyor ve yanına yanlışlıkla ne gelir?"
//
// İki iddia:
//
//  1. UÇ KİMLİK İSTER. auth.SkipPath bu yolu atlamıyor, yani anonim
//     yüzeyler (public trace snapshot'ı, public status) buradan hiçbir
//     şey okuyamaz — "model adı yalnız kimlikli yanıta girer" kuralı
//     handler'da bir dal DEĞİL, middleware'in sonucudur. SkipPath'e
//     ileride bu yolu eklemek kuralı sessizce bozardı; test onu durdurur.
//  2. YANIT DAR. Tip yalnız {enabled, model} taşır. baseUrl bir ADRES,
//     apiKey bir SIR, provider ise gereksiz — üçü de bu uçta işi yok.
//     Alan eklemek testi kırar ve karar bilinçli olmaya zorlanır.
package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/cilcenk/coremetry/internal/auth"
)

func TestCopilotConfigRequiresAuth(t *testing.T) {
	if auth.SkipPath(http.MethodGet, "/api/copilot/config") {
		t.Fatal("/api/copilot/config auth'u ATLIYOR — model adı anonim " +
			"yüzeylere (public trace / public status) sızar")
	}
	// Karşılaştırma tabanı: gerçekten public olan bir yolun true
	// döndüğünü de görelim, yoksa SkipPath her şeye false dönüyor
	// olabilir ve test yanlış sebeple yeşil kalırdı.
	if !auth.SkipPath(http.MethodGet, "/api/health") {
		t.Fatal("SkipPath /api/health için false döndü — test tabanı bozuk")
	}
}

func TestCopilotConfigResponseShape(t *testing.T) {
	raw, err := json.Marshal(copilotConfigResponse{Enabled: true, Model: "gemma4-26b-a4b-it"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := map[string]any{"enabled": true, "model": "gemma4-26b-a4b-it"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("yanıt şekli değişti\n got = %v\nwant = %v", got, want)
	}
	// Sır/adres sınıfı alan adları hiç doğmasın diye ayrıca yoklanıyor:
	// yukarıdaki DeepEqual zaten yakalar, ama hata mesajı NEDENİ söylesin.
	for _, forbidden := range []string{"baseUrl", "baseURL", "apiKey", "key", "provider", "host"} {
		if _, ok := got[forbidden]; ok {
			t.Fatalf("%q /api/copilot/config yanıtına girmiş — bu uç kimlikli "+
				"ama yine de yalnız GÖSTERİM verisi taşır", forbidden)
		}
	}
}

// Model kapalı kurulumda hiç görünmemeli: "çip yok" hâli, boş string
// değil ALAN YOKLUĞU olarak ifade ediliyor (omitempty).
func TestCopilotConfigOmitsEmptyModel(t *testing.T) {
	raw, err := json.Marshal(copilotConfigResponse{Enabled: false})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != `{"enabled":false}` {
		t.Fatalf("kapalı kurulum yanıtı = %s, want {\"enabled\":false}", raw)
	}
}
