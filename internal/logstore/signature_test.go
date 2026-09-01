package logstore

// signature_test.go — v0.10.229 (Influx D4): NormalizeSignature sözleşmesi.
// Her yer tutucu sınıfı AYRI satır (unit-mixing dersi: her dal test edilir).

import "testing"

func TestNormalizeSignature(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"uuid", "order 3f2b1c4e-9d8a-4b7c-8e6f-1a2b3c4d5e6f failed", "order <x> failed"},
		{"iso time", "at 2026-09-02T10:15:30.123Z timeout", "at <x> timeout"},
		{"iso time space", "2026-09-02 10:15:30 timeout", "<x> timeout"},
		{"ip:port", "upstream 10.0.12.34:8080 refused", "upstream <x> refused"},
		{"ip", "from 192.168.1.1 denied", "from <x> denied"},
		{"long hex trace", "trace 0af7651916cd43dd8448eb211c80319c not found", "trace <x> not found"},
		{"number ≥2 digits", "retry 17 of 250 for customer 12345", "retry <x> of <x> for customer <x>"},
		{"single digit kept", "attempt 3 failed", "attempt 3 failed"},
		{"whitespace", "  a   b\t\nc  ", "a b c"},
		{"turkish", "Müşteri 12345 için işlem başarısız: hata kodu 5", "Müşteri <x> için işlem başarısız: hata kodu 5"},
		{"empty", "   ", ""},
		{"combined", "req 3f2b1c4e-9d8a-4b7c-8e6f-1a2b3c4d5e6f from 10.1.2.3 at 2026-09-02T10:00:00Z took 1500ms", "req <x> from <x> at <x> took <x>ms"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NormalizeSignature(c.in); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestNormalizeSignature_CapAndHash(t *testing.T) {
	long := ""
	for i := 0; i < 700; i++ {
		long += "x"
	}
	if got := NormalizeSignature(long); len(got) != signatureMaxLen {
		t.Fatalf("cap at %d, got %d", signatureMaxLen, len(got))
	}
	if SignatureHash("a") == SignatureHash("b") || SignatureHash("a") != SignatureHash("a") {
		t.Fatal("hash must be deterministic and discriminating")
	}
}
