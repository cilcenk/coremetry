package auth

import (
	"strings"
	"testing"
)

// v0.10.4 — dedektör saf ve tablo-testli. İki yön de pahalı: yanlış
// pozitif operatörün geçerli anahtarına "zayıf" der ve uyarıyı gürültüye
// çevirir; yanlış negatif prod'da olan şeyi bir kez daha kaçırır.

func TestWeakSecretReason(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		weak bool
	}{
		// ── PROD'DA GERÇEKTEN OLAN DEĞER ─────────────────────────────
		// Bu satır bu dosyanın var olma sebebi: dolu, 29 karakter, ve
		// sistem tamamen sağlıklı görünüyordu.
		{"prod'da bulunan yer tutucu", "CHANGE_ME_openssl_rand_hex_32", true},

		// ── yer tutucu aileleri ──────────────────────────────────────
		{"changeme bitişik", "changeme-super-secret-value-here", true},
		{"CHANGE-ME tireli", "CHANGE-ME-and-make-it-long-enough", true},
		{"replace_me", "replace_me_with_a_real_secret_ok", true},
		{"placeholder", "this_is_a_placeholder_value_here", true},
		{"example", "example_secret_value_for_testing1", true},
		{"BÜYÜK harf yakalanır", "CHANGEME0000000000000000000000000", true},

		// ── kısa ─────────────────────────────────────────────────────
		{"çok kısa", "hunter2", true},
		{"31 karakter — eşiğin altı", strings.Repeat("a", 31), true},

		// ── SAĞLAM: uyarı ÇIKMAMALI ──────────────────────────────────
		{"32 karakter — eşikte", strings.Repeat("a", 32), false},
		{"openssl rand -hex 32 çıktısı (64 hex)",
			"9f2c4a7e1b8d3f60a5c9e2d7b4e8a1c6" + "3e7d9b2a5c8f1e4d7a0c3b6e9f2d5a81", false},
		{"gerçek rastgele anahtar",
			"7b3f9a1c5e8d2064af73c19e5b8d4f26a091c7e3b5d8f2a406c9e1b7d3f5a802", false},

		// ── boş: NewService'in KENDİ dalı, burada raporlanmaz ─────────
		{"boş — ayrı dal", "", false},
		{"yalnız boşluk", "   ", false},

		// ── ayırt edici: meşru anahtar içinde tesadüfen geçen harfler ─
		// "example" ararken "exam" gibi bir parça yeterli OLMAMALI;
		// aksi hâlde rastgele hex'lerde sahte pozitif çıkar.
		{"rastgele hex, işaret YOK",
			"a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := WeakSecretReason(tc.in)
			if (got != "") != tc.weak {
				t.Errorf("WeakSecretReason(%d karakter) = %q; zayıf=%v bekleniyordu", len(tc.in), got, tc.weak)
			}
		})
	}
}

func TestWeakSecretReasonNeverLeaksTheSecret(t *testing.T) {
	// Zayıf bir anahtarı teşhis etmek onu yaymak için gerekçe değil.
	// Sebep dizgesi /admin/stats'a ve boot loguna gidiyor; anahtarın
	// kendisinden bir parça taşırsa, teşhis kanalı sızıntı kanalı olur.
	secret := "CHANGE_ME_openssl_rand_hex_32"
	reason := WeakSecretReason(secret)
	if reason == "" {
		t.Fatal("bu değer zayıf sayılmalıydı")
	}
	if strings.Contains(reason, secret) || strings.Contains(reason, "CHANGE_ME") {
		t.Errorf("sebep anahtarı sızdırıyor: %q", reason)
	}
	// Ve sebep, anahtar görülmeden anlaşılabilir olmalı.
	if len(reason) < 10 {
		t.Errorf("sebep %q operatöre ne yapacağını söylemiyor", reason)
	}
}
