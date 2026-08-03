// v0.9.581 — CH zaman bağlama tipi. ÜÇÜNCÜ kez ısıran sınıf.
//
//	v0.9.572  toStartOfInterval DÖNÜŞ tipi   → code 43
//	v0.9.578  time_bucket'a BAĞLAMA tipi     → code 53
//
// Kural tek cümle: toStartOfInterval(...) üzerine kurulmuş HER kolon
// DateTime'dır (DateTime64 DEĞİL), çünkü saniye grenli bir INTERVAL
// DateTime64 girdiden düz DateTime üretir.
//
//	DateTime   kolon → chDateTimeArg     (kesirsiz)
//	DateTime64 kolon → chDateTime64Arg   (nanosaniyeli)
//
// Yanlış eşleşme DERLENİR, testlerden GEÇER ve yalnız canlıda patlar —
// bu yüzden kapı burada.
package chstore

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestCHDateTimeArgHasNoFraction(t *testing.T) {
	ts := time.Date(2026, 8, 2, 8, 26, 15, 176482289, time.UTC)

	got := chDateTimeArg(ts)
	if strings.Contains(got, ".") {
		t.Errorf("chDateTimeArg kesirli kısım taşıyor (%q) — CH'nin DateTime "+
			"ayrıştırıcısı bunu code 53 ile reddeder", got)
	}
	if strings.Contains(got, "Z") || strings.Contains(got, "T") {
		t.Errorf("chDateTimeArg RFC3339 biçiminde (%q) — CH boşluk ayırıcı ister "+
			"ve 'Z' kabul etmez (v0.8.197 aynı aile)", got)
	}
	if got != "2026-08-02 08:26:15" {
		t.Errorf("chDateTimeArg = %q", got)
	}

	// Kardeşi kesri KORUMALI — ikisi farklı işler.
	if g64 := chDateTime64Arg(ts); !strings.Contains(g64, ".176482289") {
		t.Errorf("chDateTime64Arg kesri kaybetmiş (%q) — DateTime64 kolonlarda "+
			"nanosaniye hassasiyeti gerekiyor", g64)
	}
}

// timeBucketBind — bir sorgu gövdesinde time_bucket karşılaştırması VE
// nanosaniyeli bağlama birlikte geçiyor mu?
var timeBucketQ = regexp.MustCompile(`time_bucket\s*[<>]=?\s*\?`)

// ASIL KAPI: time_bucket'a (DateTime) nanosaniyeli argüman bağlanmamalı.
//
// Tarama pencereli: aynı Query( çağrısı içinde hem time_bucket
// karşılaştırması hem chDateTime64Arg varsa bu, v0.9.578'in tekrarıdır.
func TestNoDateTime64BindAgainstTimeBucket(t *testing.T) {
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		name := f.Name()
		if f.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(stripGoLineComments(string(b)), "\n")
		for i, l := range lines {
			if !timeBucketQ.MatchString(l) {
				continue
			}
			// Bağlama argümanları sorgudan sonraki birkaç satırda gelir.
			for j := i; j < len(lines) && j < i+25; j++ {
				if strings.Contains(lines[j], "chDateTime64Arg") {
					t.Errorf("%s:%d — time_bucket (DateTime) karşılaştırmasına "+
						"nanosaniyeli argüman bağlanıyor (satır %d). CH bunu code 53 "+
						"ile reddeder; chDateTimeArg kullan. "+
						"toStartOfInterval üzerine kurulu her kolon DateTime'dır.",
						name, i+1, j+1)
					break
				}
				// Bir sonraki Query'ye geçtiysek pencereyi kapat.
				if j > i && strings.Contains(lines[j], ".Query(") {
					break
				}
			}
		}
	}
}
