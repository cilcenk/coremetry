// v0.9.614 — DDL ertelemesinin regresyon testi.
//
// En tehlikeli kırılma biçimi SESSİZ SONSUZ ERTELEME: yürütücü aynı
// execDDL'den geçtiği için bayrak temizlenmeden çalışırsa her ifade
// yeniden birikir, hiçbir DDL asla uygulanmaz ve hiçbir yerde hata
// görünmez — şema sonsuza dek donar. Test o sıralamayı pinliyor.
package chstore

import (
	"context"
	"testing"
)

// TestDeferGate — kapı yalnız "küme + şema yerinde" ikilisinde açılır.
func TestDeferGate(t *testing.T) {
	cases := []struct {
		cluster, spans, want bool
		why                  string
	}{
		{true, true, true, "küme + şema yerinde → ertele (prod vakası)"},
		{true, false, false, "TAZE kurulum: API tablosuz servise başlayamaz — senkron ŞART"},
		{false, true, false, "tek düğüm: kuyruk hastalığı yok, erteleme yalnız test belirsizliği katar"},
		{false, false, false, "tek düğüm taze kurulum"},
	}
	for _, c := range cases {
		if got := deferMigrationDDL(c.cluster, c.spans); got != c.want {
			t.Errorf("deferMigrationDDL(%v,%v)=%v — %s", c.cluster, c.spans, got, c.why)
		}
	}
}

// TestExecDDLDefersWithoutTouchingConn — kip açıkken execDDL yürütmez.
//
// Store zero-value (conn nil): erteleme dalı conn'a dokunsaydı test
// panic'lerdi — "yürütmüyor" iddiasının en sert kanıtı bu.
func TestExecDDLDefersWithoutTouchingConn(t *testing.T) {
	s := &Store{deferDDL: true}
	if err := s.execDDL(context.Background(), "CREATE TABLE IF NOT EXISTS x (a String)"); err != nil {
		t.Fatalf("erteleme hatasız olmalı: %v", err)
	}
	if err := s.execDDL(context.Background(), "ALTER TABLE x ADD COLUMN IF NOT EXISTS b String"); err != nil {
		t.Fatalf("ikinci erteleme: %v", err)
	}
	if len(s.deferredDDL) != 2 {
		t.Fatalf("%d ifade birikti, 2 bekleniyordu", len(s.deferredDDL))
	}
	// HAM sql saklanmalı — adaptDDL yürütme anında uygulanır; şimdi
	// uygulansaydı ertelenen ifade _local/ON CLUSTER çevirisini
	// erteleme ANINDAKİ yapılandırmayla donduracaktı.
	if s.deferredDDL[0] != "CREATE TABLE IF NOT EXISTS x (a String)" {
		t.Errorf("ham sql saklanmadı: %q", s.deferredDDL[0])
	}
}

// TestFinishClearsBeforeReturning — SESSİZ SONSUZ ERTELEME kilidi.
func TestFinishClearsBeforeReturning(t *testing.T) {
	s := &Store{deferDDL: true, deferredDDL: []string{"A", "B"}}
	got := s.finishDeferredDDL()
	if len(got) != 2 {
		t.Fatalf("devir eksik: %d", len(got))
	}
	if s.deferDDL {
		t.Fatal("bayrak temizlenmedi — yürütücü aynı execDDL'den geçiyor; açık " +
			"bayrakla her ifade YENİDEN birikir, hiçbir DDL asla uygulanmaz ve " +
			"hiçbir yerde hata görünmez: şema sessizce sonsuza dek donar")
	}
	if s.deferredDDL != nil {
		t.Fatal("liste temizlenmedi — ikinci çağrı aynı ifadeleri yeniden uygular")
	}
	// Temizlik sonrası execDDL artık ertelemez (nil conn'da adaptDDL
	// yoluna girip panic'lemesi de bunun kanıtı olurdu; onu burada
	// tetiklemiyoruz — davranışı bayrak üzerinden doğruladık).
}

// TestRunDeferredEmptyIsNoop — boş listeyle yürütücü hiçbir şey yapmaz.
func TestRunDeferredEmptyIsNoop(t *testing.T) {
	s := &Store{} // conn nil — dokunursa panic
	s.runDeferredDDL(nil)
	s.runDeferredDDL([]string{})
}
