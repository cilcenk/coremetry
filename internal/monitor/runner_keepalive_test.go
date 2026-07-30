package monitor

import (
	"os"
	"strings"
	"testing"
)

// v0.9.446 (hacim denetimi yan bulgusu — kaçırılan-alarm sınıfı): DOWN
// problemi yalnız geçişte açılıyor ve hiç tazelenmiyordu; evaluator'ın
// stale sweep'i (updated_at > 3×interval) hâlâ DOWN monitörün problemini
// ~3 dk'da "source silent" diye kapatıyordu ve monitör up→down döngüsü
// yapmadan satır geri açılamıyordu. Pinler:
//
//  1. Aynı-durum erken dönüşünden ÖNCE down keep-alive koşar.
//  2. Keep-alive, satır gerçekten yoksa (nil/nil) yeniden açar; okuma
//     HATASINDA açmaz (çift satır + çift bildirim).
func TestDownKeepAlivePins(t *testing.T) {
	b, err := os.ReadFile("runner.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, `if status == "down" {
			r.keepDownProblemAlive(ctx, m, msg)
		}
		return`) {
		t.Error("same-status early return no longer keeps the DOWN problem alive — the stale sweep will close a problem whose monitor is still down, and it can never re-open without an up→down flip")
	}
	if !strings.Contains(src, "r.handleStateChange(ctx, m, \"down\", msg)") {
		t.Error("keep-alive lost its re-open path — a falsely-swept DOWN problem stays closed while the monitor is still down")
	}
	if !strings.Contains(src, `if err != nil {
		// Okuma hatasında yeniden AÇMA`) {
		t.Error("keep-alive re-opens on read errors — a transient CH blip would fork a duplicate problem and re-page")
	}
}
