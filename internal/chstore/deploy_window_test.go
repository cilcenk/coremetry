// v0.9.552 regresyon testi — deploy zenginleştirme penceresi.
//
// Bug: pencere alt sınırı `if i == 0 || p.StartedAt < minStarted` ile
// kuruluyordu. i==0 dalı, ilk problemin Service'i boşsa bir üstteki
// `continue` yüzünden HİÇ çalışmıyor; sonraki turlarda i!=0 olduğu için
// karşılaştırma minStarted=0 ile yapılıyor ve pozitif bir unix-ns asla
// 0'dan küçük olmadığı için minStarted 0'da kalıyordu.
//
// Sonuç: from = 1970. Aşağıdaki sorgu `FROM spans` ve tek zaman sınırı
// `time >= ?` — yani sınır yok olunca TÜM tablo taranıyordu (LIMIT de
// yok, tek koruma max_execution_time=10).
//
// Tetikleyici kenar durum DEĞİL: ES watcher kuralları servissiz problem
// üretir (evaluator/watcher_eval.go) ve ListProblems started_at DESC
// sıralar — en yeni açık problem bir watcher alarmıysa problems[0]
// servissizdir.
//
// Belirti sessizdi ve YANLIŞ yöne bakıyordu: sorgu timeout'a takılıyor,
// çağıran zenginleştirmeyi atlıyor, RecentDeploy nil kalıyor.
//
// ⚠ v0.9.612 — bu testin ÖNEMİ DEĞİŞTİ, kendisi değil. O tarihte
// postDeploy dalı computePriority'den KALDIRILDI (operatör kararı:
// prod'da deploy sıklığı yüksek, tetikleyici P1'i sulandırıyordu).
// Yani pencere hatasının ETKİSİ artık "P1'ler P2 görünüyor" değil:
// RecentDeploy nil kalırsa ProblemDetail'deki DeployBox hiç çizilmez
// ve operatör problemi deploy'la ilişkilendirecek bilgiyi kaybeder.
// Karar operatörün, ama karar verebilmesi için bilgiyi GÖRMESİ şart.
package chstore

import (
	"testing"
	"time"
)

func TestDeployEnrichWindow(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) int64 { return now.Add(-d).UnixNano() }
	const lookback = 30 * time.Minute

	t.Run("ilk problem SERVİSSİZ — pencere 1970'e açılmamalı", func(t *testing.T) {
		// Tam bug senaryosu: en yeni problem bir watcher alarmı.
		problems := []Problem{
			{Service: "", StartedAt: at(1 * time.Minute)},           // watcher, servissiz
			{Service: "odeme-api", StartedAt: at(10 * time.Minute)}, // gerçek servis
			{Service: "hesap-api", StartedAt: at(20 * time.Minute)},
		}
		svc, from, to, ok := deployEnrichWindow(problems, lookback, now)
		if !ok {
			t.Fatal("ok=false — servisli problem VAR, zenginleştirme atlanmamalıydı")
		}
		if len(svc) != 2 {
			t.Errorf("servis sayısı %d, beklenen 2 (servissiz olan sayılmaz)", len(svc))
		}
		// Beklenen alt sınır: EN ESKİ SERVİSLİ problem − lookback.
		want := now.Add(-20 * time.Minute).Add(-lookback)
		if !from.Equal(want) {
			t.Errorf("from = %v, beklenen %v", from, want)
		}
		// Asıl regresyon iddiası: pencere yıllara açılmış olamaz.
		if from.Before(now.Add(-24 * time.Hour)) {
			t.Errorf("PENCERE PATLADI: from = %v (24 saatten eski) — bu, "+
				"spans tablosunun tam taranması demek", from)
		}
		if !to.Equal(now.Add(-10 * time.Minute)) {
			t.Errorf("to = %v, beklenen en YENİ servisli problem %v",
				to, now.Add(-10*time.Minute))
		}
	})

	t.Run("tek problem ve o da servissiz → atla", func(t *testing.T) {
		_, _, _, ok := deployEnrichWindow(
			[]Problem{{Service: "", StartedAt: at(time.Minute)}}, lookback, now)
		if ok {
			t.Error("ok=true — servisli problem YOK, sorgu hiç koşmamalı")
		}
	})

	t.Run("hepsi servisli — normal yol bozulmadı", func(t *testing.T) {
		problems := []Problem{
			{Service: "a", StartedAt: at(5 * time.Minute)},
			{Service: "b", StartedAt: at(15 * time.Minute)},
		}
		_, from, to, ok := deployEnrichWindow(problems, lookback, now)
		if !ok {
			t.Fatal("ok=false")
		}
		if !from.Equal(now.Add(-15 * time.Minute).Add(-lookback)) {
			t.Errorf("from = %v", from)
		}
		if !to.Equal(now.Add(-5 * time.Minute)) {
			t.Errorf("to = %v", to)
		}
	})

	t.Run("servissiz problem EN ESKİ olsa da pencereyi genişletmemeli", func(t *testing.T) {
		// Servissiz satırın started_at'i pencereye KATILMAMALI: o servis
		// için deploy sorgusu zaten koşmayacak, penceresini genişletmesi
		// bedavaya tarama demek.
		problems := []Problem{
			{Service: "a", StartedAt: at(5 * time.Minute)},
			{Service: "", StartedAt: at(10 * 24 * time.Hour)}, // 10 gün eski, servissiz
		}
		_, from, _, ok := deployEnrichWindow(problems, lookback, now)
		if !ok {
			t.Fatal("ok=false")
		}
		if !from.Equal(now.Add(-5 * time.Minute).Add(-lookback)) {
			t.Errorf("from = %v — servissiz satır pencereyi genişletmiş", from)
		}
	})

	t.Run("bozuk StartedAt=0 → taban devreye girer", func(t *testing.T) {
		// İkinci kalkan. Hesap bir gün yine hata yaparsa sorgu tam tablo
		// taramasına dönüşmesin.
		problems := []Problem{{Service: "a", StartedAt: 0}}
		_, from, _, ok := deployEnrichWindow(problems, lookback, now)
		if !ok {
			t.Fatal("ok=false")
		}
		floor := now.Add(-deployWindowFloor)
		if !from.Equal(floor) {
			t.Errorf("from = %v, tabana (%v) çekilmeliydi", from, floor)
		}
		if from.Year() < 2020 {
			t.Errorf("from = %v — taban kalkanı çalışmadı, pencere 1970'e açık", from)
		}
	})

	t.Run("boş liste", func(t *testing.T) {
		if _, _, _, ok := deployEnrichWindow(nil, lookback, now); ok {
			t.Error("ok=true — boş listede zenginleştirme koşmamalı")
		}
	})
}
