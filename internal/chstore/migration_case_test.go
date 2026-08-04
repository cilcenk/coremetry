package chstore

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// v0.9.626 — migration'lar attribute anahtarını TEK YAZIMLA okuyordu.
//
// 0002_rollup_wide.sql GENİŞ aile MV'sini `indexOf(attr_keys,
// 'CHANNEL_CODE')` ile besliyordu; prod ise küçük harf yazıyor
// ('channel_code' — operatör ölçümü, 10 dakikada 2.67M span).
// Sonuç: rollup tablolarının channel_code / function_code kolonları
// SABİT BOŞ doldu — üstelik ikisi de ORDER BY önekinde ve bloom
// index'inde, yani birincil anahtarın bir bileşeni hiçbir şey elemedi.
//
// Bu test kuralı çalıştırılabilir yapıyor: bir migration bir terfi
// attribute'unun BİR yazımını okuyorsa DİĞERİNİ de okumak zorunda.
// Aynı kural Go tarafında promotedAttrExpr'de.

// stripSQLComments — `--` yorumlarını atar.
//
// ZORUNLU: bu dosyanın ve migration'ların YORUMLARI da 'CHANNEL_CODE'
// geçiriyor (hatanın hikâyesini anlatıyorlar). Yorumları sıyırmadan
// tarayan bir test KENDİ AÇIKLAMASIYLA eşleşir ve hep yeşil kalır —
// bu oturumda üç kez yapılan hata (Go ve TS taramalarında).
func stripSQLComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func TestStripSQLCommentsRemovesTrailingAndWholeLine(t *testing.T) {
	in := "SELECT 1 -- 'CHANNET'\n-- tamamen yorum 'CHANNEL_CODE'\nSELECT 2\n"
	got := stripSQLComments(in)
	if strings.Contains(got, "CHANNEL_CODE") || strings.Contains(got, "CHANNET") {
		t.Fatalf("yorumlar sıyrılmadı: %q", got)
	}
	if !strings.Contains(got, "SELECT 1") || !strings.Contains(got, "SELECT 2") {
		t.Fatalf("kod satırları kayboldu: %q", got)
	}
}

var attrKeyLiteralRe = regexp.MustCompile(`indexOf\(\s*attr_keys\s*,\s*'([A-Za-z0-9_.]+)'\s*\)`)

func TestMigrationsReadEveryPromotedSpelling(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("migrations dizini okunamadı: %v", err)
	}

	// promotedAttrs'ten türet — yeni bir terfi attribute'u eklendiğinde
	// bu test onu OTOMATİK kapsar; ikinci bir liste tutmak, bu oturumun
	// tekrar eden "iki yer, iki kural" hatası olurdu.
	spellingOf := map[string][]string{}
	for _, a := range promotedAttrs {
		for _, k := range a.keys {
			spellingOf[k] = a.keys
		}
	}

	checked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("%s okunamadı: %v", e.Name(), err)
		}
		body := stripSQLComments(string(raw))

		seen := map[string]bool{}
		for _, m := range attrKeyLiteralRe.FindAllStringSubmatch(body, -1) {
			seen[m[1]] = true
		}
		for key := range seen {
			siblings, promoted := spellingOf[key]
			if !promoted {
				continue
			}
			checked++
			for _, want := range siblings {
				if !seen[want] {
					t.Errorf("%s: '%s' okunuyor ama '%s' okunmuyor — prod'un kullandığı yazım kaçarsa boyut SABİT BOŞ dolar",
						e.Name(), key, want)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("hiçbir migration'da terfi attribute'u bulunamadı — regex ya da dizin yolu bozulmuş olabilir")
	}
}
