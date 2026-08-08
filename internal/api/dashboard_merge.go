// v0.9.563 — dashboard güncellemesinde alan taşıma.
//
// Bulunuşu: preset dashboard paketini yenilemek için yapılan
// çekişmeli incelemede, yeni paketin $service değişkenine
// güvenemeyeceği ortaya çıktı — çünkü değişken KAYDETMEDE siliniyordu.
//
// Hata zinciri (dördü de doğrulandı):
//
//  1. Frontend kaydederken gövdeye yalnız {name, description, panels}
//     koyuyor — `variables` YOK.
//  2. Handler gövdeyi TAZE bir chstore.Dashboard struct'ına decode
//     ediyor; ileri taşıdığı tek alan CreatedAt.
//  3. UpsertDashboard boş Variables'ı "[]" yapıyor.
//  4. Tablo ReplacingMergeTree — tam satır değişimi. Değişkenler gitti.
//
// Belirti SESSİZ: hata yok, uyarı yok. VariablesBar çizilmiyor ve
// ${service} referansı taşıyan her panel satırı, substituteVars'ın
// "tanımsız değişkenli satırı düşür" kuralı gereği filtreden düşüyor —
// paneller sessizce FİLO GENELİNE geçiyor. Operatör bir servisi
// izlediğini sanırken tüm filoyu görüyor.
//
// Bu, CLAUDE.md'nin ReplacingMergeTree değişmezinin doğrudan ihlali:
// "Whole-row replace: carry ALL fields forward." Kural yazılıydı,
// handler uymuyordu.
package api

import (
	"encoding/json"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// mergeDashboardUpdate — gövdede GELMEYEN alanları mevcut satırdan
// taşır. SAF (test edilebilir).
//
// Kritik ayrım: `nil` ile `[]` AYNI ŞEY DEĞİL.
//
//	nil  ⇒ istemci alanı HİÇ GÖNDERMEDİ  → mevcut değer korunur
//	"[]" ⇒ istemci AÇIKÇA boşalttı        → boş kalır
//
// json.RawMessage bu ayrımı taşıyabiliyor ve taşıması şart: "hepsini
// koru" demek, operatörün bilerek sildiği bir paneli geri getirirdi;
// "hiçbirini koruma" demek bugünkü sessiz veri kaybı. Doğru davranış
// ikisinin arasında ve tam olarak bu ayrımda duruyor.
func mergeDashboardUpdate(incoming, existing chstore.Dashboard) chstore.Dashboard {
	out := incoming
	out.ID = existing.ID
	out.CreatedAt = existing.CreatedAt

	if out.Panels == nil {
		out.Panels = existing.Panels
	}
	if out.Variables == nil {
		out.Variables = existing.Variables
	}
	// v0.9.780 — etiketler de aynı sözleşmeye tabi. Bu dal olmasaydı
	// panel/değişkenle BİREBİR aynı sessiz kayıp yaşanırdı: Dashboard
	// düzenleme kaydı ve Explore'un "panoya pinle"si gövdeye `tags`
	// koymuyor, dolayısıyla ilk kaydetmede operatörün etiketleri
	// silinirdi — hata yok, uyarı yok, sadece boşalan bir kolon.
	if out.Tags == nil {
		out.Tags = existing.Tags
	}
	return out
}

// dashboardBodyOmits — bir alanın gövdede hiç gelmediğini söyler.
// Yalnız okunabilirlik için; json.RawMessage(nil) testi tek yerde
// dursun diye.
func dashboardBodyOmits(raw json.RawMessage) bool { return raw == nil }
