package api

// api_go_size_test.go — v0.10.625. api.go BÜYÜMEZ kuralının taşınabilir
// kilidi: taban .claude/baselines/api_go_lines (ratchet, yalnız aşağı iner).
// scripts/guard-api-go-size.sh aynı sözleşmeyi hook/pre-commit/CI'da uygular;
// bu test `go test ./...` ile her yerde (release skill'i, CI backend job'ı)
// otomatik koşar — script kurulmamış bir makinede bile kural yaşar.
//
// İki yönlü: büyüme HATA (yeni route/handler kendi <domain>.go dosyasına,
// route_registry.go init() defteri); küçülme de HATA — taban aynı commit'te
// indirilmeli, yoksa bayat taban sonraki büyümeye yer bırakır (ratchet'in
// anlamı). Sayım wc -l ile aynı: '\n' sayısı.

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"testing"
)

const apiGoBaselinePath = "../../.claude/baselines/api_go_lines"

func TestApiGoDoesNotGrow(t *testing.T) {
	raw, err := os.ReadFile(apiGoBaselinePath)
	if err != nil {
		t.Fatalf("taban dosyası okunamadı (%s): %v", apiGoBaselinePath, err)
	}
	base, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || base <= 0 {
		t.Fatalf("taban geçersiz: %q", raw)
	}
	src, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	now := bytes.Count(src, []byte("\n"))
	switch {
	case now > base:
		t.Fatalf("api.go %d satır > taban %d. Yeni route/handler kendi internal/api/<domain>.go dosyasına (/api-route, registerRoutesExtra); tabanı yükseltmek CODEOWNERS onayı + 'Api-Go-Baseline:' trailer'ı ister.", now, base)
	case now < base:
		t.Fatalf("api.go küçüldü (%d → %d) — ratchet: %s dosyasını %d yap ve aynı commit'e ekle.", base, now, apiGoBaselinePath, now)
	}
}
