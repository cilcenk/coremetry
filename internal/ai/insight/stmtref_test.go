package insight

import (
	"os"
	"strings"
	"testing"
)

// stmtref_test.go — v0.9.1137 (Faz 2.4).
//
// Kabul testi FE kodeğiyle (decodeStmtParam, stmtParam.ts) BİREBİR
// olmalı. Ayrışmanın iki yüzü de sessiz:
//   - sunucu kabul eder / FE reddederse: kart açılır, "İfade detayı"
//     çipi çekmeceyi HİÇ açmaz ("düğme bozuk" gibi okunur, v0.9.963);
//   - FE kabul eder / sunucu reddederse: paylaşılan link 400 döner.
func TestParseStmtRef(t *testing.T) {
	for _, tc := range []struct {
		name       string
		in         string
		wantOK     bool
		wantHash   uint64
		wantSystem string
		wantParam  string
	}{
		{name: "yalnız hash", in: "12345", wantOK: true, wantHash: 12345, wantParam: "12345"},
		{name: "hash + motor", in: "12345|oracle", wantOK: true,
			wantHash: 12345, wantSystem: "oracle", wantParam: "12345|oracle"},
		{name: "uint64 tavanı", in: "18446744073709551615", wantOK: true,
			wantHash: 18446744073709551615, wantParam: "18446744073709551615"},
		{name: "yüzde kaçışı çözülür", in: "7|ms%20sql", wantOK: true,
			wantHash: 7, wantSystem: "ms sql",
			// Boşluk güvenli kümenin DIŞINDA → kanonik param motoru
			// DÜŞÜRÜR (kapsam genişler ama link çalışır).
			wantParam: "7"},
		{name: "boş", in: "", wantOK: false},
		{name: "boşluk", in: "   ", wantOK: false},
		// "0" MV'nin "ifade yok" nöbetçisi — hiçbir sınıfı adreslemez.
		{name: "sıfır nöbetçisi", in: "0", wantOK: false},
		{name: "sıfır dolgulu", in: "000", wantOK: false},
		{name: "rakam dışı", in: "12a45", wantOK: false},
		{name: "negatif", in: "-5", wantOK: false},
		{name: "20 haneden uzun", in: "123456789012345678901", wantOK: false},
		{name: "uint64 taşması (20 hane)", in: "99999999999999999999", wantOK: false},
		{name: "üç parça", in: "1|oracle|extra", wantOK: false},
		{name: "boş ikinci parça", in: "1|", wantOK: false},
		{name: "boşluklu ikinci parça", in: "1|%20", wantOK: false},
		{name: "bozuk yüzde kaçışı", in: "1|%zz", wantOK: false},
		{name: "hash'siz", in: "|oracle", wantOK: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseStmtRef(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ParseStmtRef(%q) ok = %v; want %v (%+v)", tc.in, ok, tc.wantOK, got)
			}
			if !ok {
				return
			}
			if got.Hash != tc.wantHash || got.System != tc.wantSystem || got.Param != tc.wantParam {
				t.Errorf("ParseStmtRef(%q) = %+v; want hash=%d system=%q param=%q",
					tc.in, got, tc.wantHash, tc.wantSystem, tc.wantParam)
			}
		})
	}
}

// Kanonik param → tekrar ayrıştırılabilir olmalı (round-trip): kartın
// ürettiği link, aynı kartı tekrar açabilmeli.
func TestStmtParamRoundTrips(t *testing.T) {
	for _, in := range []string{"1", "12345|oracle", "999|postgresql", "42|ms-sql_2019.x"} {
		ref, ok := ParseStmtRef(in)
		if !ok {
			t.Fatalf("%q ayrıştırılamadı", in)
		}
		again, ok := ParseStmtRef(ref.Param)
		if !ok {
			t.Fatalf("kanonik param %q yeniden ayrıştırılamadı", ref.Param)
		}
		if again.Hash != ref.Hash || again.System != ref.System {
			t.Errorf("round-trip kaybı: %+v → %+v", ref, again)
		}
	}
}

// TestStmtRefMatchesFrontendCodec — KAYNAK PİNİ. FE kodeğinin kabul
// kuralları (rakam regex'i, sıfır nöbetçisi, iki parça sınırı) burada da
// geçerli olmalı; oradaki bir gevşeme/sıkılaşma burada da yapılmalı.
func TestStmtRefMatchesFrontendCodec(t *testing.T) {
	const src = "../../../frontend/src/pages/slowqueries/stmtParam.ts"
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("%s okunamadı (kodek taşındıysa pini yeniden konumlandır): %v", src, err)
	}
	body := string(b)
	for _, want := range []string{
		`const HASH_RE = /^[0-9]{1,20}$/`, // 20 hane sınırı
		`if (/^0+$/.test(parts[0])) return null`,
		`if (parts.length > 2 || !parts[0]) return null`,
		`if (parts.length === 2 && !parts[1]) return null`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("FE kodeği %q kuralını artık taşımıyor — ParseStmtRef ile ayrıştı", want)
		}
	}
}
