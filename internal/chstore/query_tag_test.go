package chstore

// query_tag_test.go — v0.10.254 sorgu etiketi sözleşmesi: sanitize (tırnak/
// yeni satır/uzunluk), etiket ctx'te taşınır, ayar birleştirme (async
// insert ayarları etiketle EZİLMEZ), route etiketi; kaynak taraması:
// clickhouse.WithSettings yalnız query_tag.go'dan (WithQuerySettings kapısı)
// ve serveCached etiketi basıyor.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2"
)

func TestQueryTagSanitize(t *testing.T) {
	in := "  route:GET /api/traces\n" + string(rune(39)) + " OR 1=1 --  "
	if got := sanitizeQueryTag(in); got != "route:GET /api/traces OR 1=1 --" {
		t.Errorf("sanitize: %q", got)
	}
	long := strings.Repeat("a", 300)
	if got := sanitizeQueryTag(long); len(got) != queryTagMax {
		t.Errorf("uzunluk %d, istenen %d", len(got), queryTagMax)
	}
	if sanitizeQueryTag("  ") != "" || sanitizeQueryTag("ç x") != "x" {
		t.Error("boş / ascii-dışı temizliği")
	}
	if got := RouteQueryTag("GET /api/traces", "/x"); got != "route:GET /api/traces" {
		t.Errorf("route tag: %q", got)
	}
	if got := RouteQueryTag("", "/api/x"); got != "route:/api/x" {
		t.Errorf("route tag fallback: %q", got)
	}
}

func TestQueryTagContextAndMerge(t *testing.T) {
	ctx := WithQuerySettings(context.Background(), clickhouse.Settings{"async_insert": 1, "max_execution_time": 5})
	ctx = WithQueryTag(ctx, "worker:evaluator")
	if QueryTag(ctx) != "worker:evaluator" {
		t.Fatal("etiket ctx'te yok")
	}
	tagged := applyQueryTag(ctx)
	got, _ := tagged.Value(querySettingsKey{}).(clickhouse.Settings)
	if got["log_comment"] != "worker:evaluator" || got["async_insert"] != 1 || got["max_execution_time"] != 5 {
		t.Errorf("ayarlar birleşmedi: %v", got)
	}
	if applyQueryTag(tagged) != tagged {
		t.Error("etiket zaten uygulanmışken yeni ctx üretilmemeli")
	}
	plain := context.Background()
	if applyQueryTag(plain) != plain {
		t.Error("etiketsiz ctx aynen dönmeli")
	}
	if WithQueryTag(plain, "  ") != plain {
		t.Error("boş etiket ctx'i değiştirmemeli")
	}
}

// Kaynak taraması: clickhouse.WithSettings yalnız query_tag.go'da; başka
// bir dosya doğrudan çağırırsa ayarları (ve etiketi) ezer.
func TestQueryTagSingleSettingsGate(t *testing.T) {
	var bad []string
	for _, pat := range []string{"*.go", "../api/*.go", "../*/*.go"} {
		files, _ := filepath.Glob(pat)
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") || filepath.Base(f) == "query_tag.go" {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			if strings.Contains(string(b), "clickhouse.WithSettings(") {
				bad = append(bad, f)
			}
		}
	}
	if len(bad) > 0 {
		t.Fatalf("clickhouse.WithSettings doğrudan çağrılıyor (WithQuerySettings kapısını kullan): %v", bad)
	}
	b, err := os.ReadFile("../api/cache.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "chstore.WithQueryTag(") {
		t.Error("serveCached sorgu etiketini basmıyor (route:<pattern>)")
	}
}
