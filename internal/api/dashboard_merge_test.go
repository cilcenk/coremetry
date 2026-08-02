// v0.9.563 regresyon testi — dashboard güncellemesinde alan taşıma.
//
// Korunan sözleşme: gövdede GELMEYEN alan mevcut satırdan taşınır;
// AÇIKÇA boşaltılan alan boş kalır.
//
// Bu ayrım olmadan iki yönlü hata var:
//   - hiç taşımamak → bugünkü sessiz veri kaybı ($service siliniyor,
//     paneller filo geneline geçiyor, hiçbir uyarı yok)
//   - hepsini taşımak → operatörün bilerek sildiği panel/değişken
//     geri geliyor
package api

import (
	"encoding/json"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestMergeDashboardUpdate(t *testing.T) {
	existing := chstore.Dashboard{
		ID:        "preset-service-performance",
		CreatedAt: 1111,
		Panels:    json.RawMessage(`[{"id":"p1"}]`),
		Variables: json.RawMessage(`[{"name":"service","type":"service"}]`),
	}

	t.Run("gövde variables GÖNDERMEDİ → korunur", func(t *testing.T) {
		// TAM BUG SENARYOSU: frontend {name, description, panels}
		// gönderiyor, variables yok. Öncesinde $service kalıcı
		// siliniyordu ve ${service} referanslı paneller sessizce
		// filo geneline geçiyordu.
		in := chstore.Dashboard{Name: "yeni ad", Panels: json.RawMessage(`[{"id":"p2"}]`)}
		got := mergeDashboardUpdate(in, existing)

		if string(got.Variables) != string(existing.Variables) {
			t.Errorf("variables kayboldu: %s — $service silinince ${service} "+
				"referanslı paneller sessizce FİLO GENELİNE geçer",
				string(got.Variables))
		}
		if string(got.Panels) != `[{"id":"p2"}]` {
			t.Errorf("gönderilen paneller taşınmamış: %s", string(got.Panels))
		}
		if got.Name != "yeni ad" {
			t.Errorf("ad güncellenmemiş: %q", got.Name)
		}
	})

	t.Run("gövde variables AÇIKÇA boşalttı → boş kalır", func(t *testing.T) {
		// Operatörün bilerek sildiği değişken geri GELMEMELİ.
		in := chstore.Dashboard{Name: "x", Variables: json.RawMessage(`[]`)}
		got := mergeDashboardUpdate(in, existing)
		if string(got.Variables) != `[]` {
			t.Errorf("açıkça boşaltılan variables geri gelmiş: %s", string(got.Variables))
		}
	})

	t.Run("gövde panels göndermedi → korunur", func(t *testing.T) {
		in := chstore.Dashboard{Name: "x"}
		got := mergeDashboardUpdate(in, existing)
		if string(got.Panels) != `[{"id":"p1"}]` {
			t.Errorf("paneller kayboldu: %s", string(got.Panels))
		}
	})

	t.Run("panels AÇIKÇA boşaltıldı → boş kalır", func(t *testing.T) {
		in := chstore.Dashboard{Name: "x", Panels: json.RawMessage(`[]`)}
		got := mergeDashboardUpdate(in, existing)
		if string(got.Panels) != `[]` {
			t.Errorf("açıkça boşaltılan paneller geri gelmiş: %s", string(got.Panels))
		}
	})

	t.Run("kimlik ve oluşturma zamanı DAİMA mevcuttan", func(t *testing.T) {
		// İstemci bunları göndermeye ya da değiştirmeye çalışsa bile.
		in := chstore.Dashboard{ID: "sahte", CreatedAt: 9999, Name: "x"}
		got := mergeDashboardUpdate(in, existing)
		if got.ID != existing.ID {
			t.Errorf("kimlik istemciden alınmış: %q", got.ID)
		}
		if got.CreatedAt != existing.CreatedAt {
			t.Errorf("oluşturma zamanı ezilmiş: %d", got.CreatedAt)
		}
	})
}

// nil ile boş dizi ayrımı bu düzeltmenin TEMELİ — JSON decode'un bu
// ayrımı gerçekten taşıdığını sabitler.
func TestDashboardBodyOmitsDistinguishesNilFromEmpty(t *testing.T) {
	var omitted chstore.Dashboard
	if err := json.Unmarshal([]byte(`{"name":"x"}`), &omitted); err != nil {
		t.Fatal(err)
	}
	if !dashboardBodyOmits(omitted.Variables) {
		t.Error("gövdede olmayan alan nil DEĞİL — taşıma mantığı çöker")
	}

	var cleared chstore.Dashboard
	if err := json.Unmarshal([]byte(`{"name":"x","variables":[]}`), &cleared); err != nil {
		t.Fatal(err)
	}
	if dashboardBodyOmits(cleared.Variables) {
		t.Error("açıkça boşaltılan alan nil görünüyor — operatörün silme " +
			"niyeti 'göndermedi' sanılır ve değişken geri gelir")
	}
}
