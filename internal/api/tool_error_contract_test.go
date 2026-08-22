package api

// tool_error_contract_test.go — v0.9.1234.
//
// Orijinal semptom: tool handler'ı hata döndürdüğünde modele HAM Go
// hatası gidiyordu (ClickHouse sürücü dökümü, ES taşıma hatası, ctx
// deadline) — uzun, İngilizce, bazen sorgu içi sızdıran ve "şimdi ne
// yapmalı"ya kör. Düzeltme merkezî bir sınıflandırıcı
// (internal/mcp/toolerr.go) ve İKİ tüketicinin de aynı sözleşmeyi
// basması.
//
// Bu dosya sınıflandırıcının KENDİ testi değil (o mcp paketinde,
// yanında yaşıyor). Burada mcp'nin göremediği iki şey çivileniyor:
//
//	(a) SENTINEL PİNİ — internal/mcp bilerek sıfır coremetry
//	    bağımlılığı taşıyor, yani logstore.ErrBackendSlow'u errors.Is
//	    ile göremiyor ve METİNLE eşliyor. Gerçek sentinel'i
//	    sınıflandırıcıya sokan test, iki paketi de gören TEK yer
//	    burası. Sentinel'in metni değişirse burada kırılır — sessiz
//	    kayma yerine kırmızı test.
//
//	(b) BAĞLANMA PİNİ — saf test, çağrı yerinin bağlı olduğunu
//	    göstermez (bu deponun tekrar eden dersi). Sözleşmeyi üreten
//	    fonksiyon dururken çağrı yerini eski hâline döndürmek
//	    derlenir ve hiçbir birim testi kırmaz.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/logstore"
	"github.com/cilcenk/coremetry/internal/mcp"
)

func TestBackendSlowSentinelClassifies(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"çıplak sentinel", logstore.ErrBackendSlow, mcp.ToolErrBackendUnavailable},
		{"sentinel + bağlantı sebebi",
			fmt.Errorf("%w: %v", logstore.ErrBackendSlow,
				errors.New("dial tcp 10.0.0.9:9200: connect: connection refused")),
			mcp.ToolErrBackendUnavailable},
		// MapBackendSlow deadline'ı sentinel'e sarar; sarmalanan sebep
		// timeout olduğunda modelin eylemi "pencereyi daralt"tır, o
		// yüzden timeout kazanır — sınıflandırıcıdaki sıra bilinçli.
		{"sentinel + deadline sebebi",
			fmt.Errorf("%w: %v", logstore.ErrBackendSlow, context.DeadlineExceeded),
			mcp.ToolErrTimeout},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mcp.ClassifyToolError(c.err)
			if got.Error != c.want {
				t.Fatalf("sınıf = %q, beklenen %q (hata: %v)\n"+
					"logstore.ErrBackendSlow metni değiştiyse mcp/toolerr.go'daki "+
					"toolErrUnavailableSignals listesini güncelle", got.Error, c.want, c.err)
			}
			if !got.Retryable {
				t.Errorf("arka uç/timeout sınıfı retryable olmalı")
			}
		})
	}
}

// TestToolErrorContractIsBound — iki tüketici de sözleşmeyi basıyor mu?
//
// Kapı hem VARLIĞI (mcp.ToolErrorJSON çağrısı) hem YOKLUĞU (ham
// err.Error()'ın modele/çipe basılması) sorar: yalnız varlık sorsaydı
// birinin yanına eski satırı geri koymak kapıyı geçerdi.
func TestToolErrorContractIsBound(t *testing.T) {
	files := map[string]string{
		"copilot_chat.go":  "sohbet döngüsünün ToolResult'ı",
		"chat_step_ids.go": "operatörün ⚙ kanıt çipi",
		"../mcp/mcp.go":    "MCP teli (tools/call isError content'i)",
	}
	for f, what := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s okunamadı: %v", f, err)
		}
		// Yorumlar SIYRILIR: bu düzeltmenin açıklamaları hem eski
		// hem yeni yazılışı ALINTILIYOR (ne olduğunu anlatmak için),
		// sıyırmayan tarayıcı kendi açıklamasıyla eşleşirdi.
		src := stripGoComments(string(b))
		if !strings.Contains(src, "ToolErrorJSON(") {
			t.Errorf("%s (%s) tool hata sözleşmesini basmıyor — "+
				"mcp.ToolErrorJSON(err) bekleniyor", f, what)
		}
		for _, raw := range []string{`"error: " + `, `Text: err.Error()`, `+ herr.Error()`} {
			if strings.Contains(src, raw) {
				t.Errorf("%s (%s) ham hata metnini basıyor (%q) — modele/çipe giden "+
					"metin sınıflandırıcıdan geçmeli", f, what, raw)
			}
		}
	}
}
