package api

// v0.9.1191 — spool runbook'unun tek-uçuş defteri.
//
// Korunan davranışlar küçük ama hepsi operatör-görünür: koşan bir flush
// varken ikinci istek REDDEDİLİR (409 dalı), biten uçuşun üstüne yenisi
// AÇILABİLİR, hata uçuş kaydında kalır (düğme "bastım, bir şey olmadı"
// hissi veremez). Saf durum makinesi — HTTP katmanı yalnız bunu sarar.

import (
	"errors"
	"testing"
)

func TestSpoolFlightsSingleFlight(t *testing.T) {
	var f spoolFlights

	st, started := f.begin("metric_points", "admin", 100)
	if !started || st.Table != "metric_points" || st.StartedAt != 100 {
		t.Fatalf("ilk uçuş başlamalıydı: %+v started=%v", st, started)
	}

	// Koşarken ikinci istek: reddedilir ve KOŞAN kaydı döner (FE
	// "zaten çalışıyor"u başlangıç zamanıyla gösterebilsin).
	cur, started2 := f.begin("metric_points", "admin2", 200)
	if started2 {
		t.Fatal("koşan uçuş varken ikincisi başlamamalıydı")
	}
	if cur.StartedAt != 100 || cur.StartedBy != "admin" {
		t.Errorf("dönen kayıt koşan uçuş olmalı: %+v", cur)
	}

	// Farklı tablo BAĞIMSIZ: metric_points koşarken spans flush'ı serbest.
	if _, ok := f.begin("spans", "admin", 150); !ok {
		t.Error("farklı tablonun uçuşu engellenmemeli")
	}

	// Bitiş + yeni uçuş.
	f.finish("metric_points", 300, nil)
	st3, started3 := f.begin("metric_points", "admin", 400)
	if !started3 || st3.StartedAt != 400 {
		t.Fatalf("biten uçuşun üstüne yenisi açılabilmeli: %+v", st3)
	}
}

func TestSpoolFlightsErrorIsKept(t *testing.T) {
	var f spoolFlights
	f.begin("metric_points", "admin", 100)
	f.finish("metric_points", 200, errors.New("code: 241"))

	snap := f.snapshot()
	if len(snap) != 1 {
		t.Fatalf("1 kayıt bekleniyordu: %d", len(snap))
	}
	if snap[0].Error == "" || snap[0].DoneAt != 200 {
		t.Errorf("hata uçuş kaydında kalmalı: %+v", snap[0])
	}

	// Bilinmeyen tablonun finish'i sessizce yutulur (panik değil):
	// yarış hâlinde geç gelen bir bitiş defteri bozamaz.
	f.finish("yok-boyle-tablo", 300, nil)
}

// TestSpoolFlightsSnapshotIsCopy — snapshot DEĞER kopyası döndürmeli.
// Pointer sızsa, HTTP yanıtını yazan goroutine ile finish() yarışır ve
// -race altında (haklı olarak) patlardı.
func TestSpoolFlightsSnapshotIsCopy(t *testing.T) {
	var f spoolFlights
	f.begin("metric_points", "admin", 100)
	snap := f.snapshot()
	f.finish("metric_points", 200, nil)
	if snap[0].DoneAt != 0 {
		t.Error("snapshot canlı kayda bağlı — kopya olmalı")
	}
}
