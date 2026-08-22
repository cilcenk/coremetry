package mcp

// v0.9.1234 — tool hata sözleşmesinin regresyon testi.
//
// Orijinal semptom: handler hatası modele HAM gidiyordu — mcp.go'nun
// tools/call yolu `err.Error()`'ı isError content'ine, sohbet döngüsü
// (api/copilot_chat.go) "error: "+err.Error()'ı ToolResult'a
// koyuyordu. Bir ClickHouse istisnası (code: 241 bellek dökümü) tek
// başına kilobaytlarca sürücü metnini modelin bağlamına ve operatörün
// ⚙ çipine taşıyordu; model de "şimdi ne yapmalı"yı okuyamadığı için
// aynı çağrıyı aynı argümanlarla tekrarlıyordu.
//
// Burada çivilenenler:
//   - beş sınıfın her biri gerçek hata metinleriyle,
//   - sınıflandırılamayan → internal (ham metin KORUNUR ama kırpılır),
//   - SARMALANMIŞ context.DeadlineExceeded (sürücüler ctx hatasını
//     kendi metinleriyle sarar — errors.Is olmadan kaçardı),
//   - kapak: tavandan uzun sürücü dökümünün KUYRUĞU content'e ASLA
//     ulaşmaz (asıl bütçe bug'ı buydu),
//   - paketin sıfır-coremetry-bağımlılığı (sınıflandırıcının neden
//     BURADA yaşadığının şartı).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestClassifyToolError(t *testing.T) {
	// Gerçek metinler: chstore/ES sürücülerinden ve mcptools
	// handler'larından birebir alındı (uydurma metinle test etmek,
	// sinyal listesini gerçek dünyaya karşı DEĞİL kendine karşı
	// doğrulamak olurdu).
	cases := []struct {
		name      string
		err       error
		wantClass string
		wantRetry bool
	}{
		{"ctx deadline", context.DeadlineExceeded, ToolErrTimeout, true},
		{"ctx canceled", context.Canceled, ToolErrTimeout, true},
		{"sarmalanmış deadline", fmt.Errorf("clickhouse: read block: %w", context.DeadlineExceeded),
			ToolErrTimeout, true},
		{"CH 159", errors.New("code: 159, message: Timeout exceeded: elapsed 30.1 seconds, maximum: 30"),
			ToolErrTimeout, true},
		{"max_execution_time", errors.New("DB::Exception: estimated query execution time is too long, max_execution_time"),
			ToolErrTimeout, true},

		{"CH 241 bellek", errors.New("code: 241, DB::Exception: Memory limit (total) exceeded: would use 9.31 GiB"),
			ToolErrBackendUnavailable, true},
		{"bağlantı reddi", errors.New("dial tcp 10.0.0.4:9000: connect: connection refused"),
			ToolErrBackendUnavailable, true},
		{"logstore sentinel metni", errors.New("log backend slow/unreachable: context deadline exceeded"),
			// Sentinel metni "slow/unreachable" taşıyor ama sarmaladığı
			// sebep "deadline"; sıra gereği timeout kazanır ve DOĞRUSU
			// odur: modelin eylemi pencereyi daraltmaktır.
			ToolErrTimeout, true},
		{"logstore yapılandırılmamış", errors.New("log backend not configured"),
			ToolErrBackendUnavailable, true},
		{"net.OpError", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("no route to host")},
			ToolErrBackendUnavailable, true},

		{"decode args", fmt.Errorf("decode args: %w", errors.New("unexpected EOF")),
			ToolErrBadArgs, false},
		{"zorunlu alan TR", errors.New("service zorunlu — önce list_services"),
			ToolErrBadArgs, false},
		{"zorunlu alan EN", errors.New("service is required — get it from list_services"),
			ToolErrBadArgs, false},
		{"biçim hatası", errors.New(`span_id must be 16 hex chars, got "abc"`),
			ToolErrBadArgs, false},
		{"RFC3339", errors.New(`at_iso RFC3339 değil: parsing time "dün" as RFC3339`),
			ToolErrBadArgs, false},

		{"trace yok", errors.New("trace 9f2c not found"), ToolErrNotFound, false},
		{"veri yok", errors.New("service checkout has no data in last 30m"), ToolErrNotFound, false},
		{"bilinmeyen tool", errors.New(`unknown tool "get_moon_phase"`), ToolErrNotFound, false},

		{"sınıflandırılamayan", errors.New("something went sideways in the correlator"),
			ToolErrInternal, false},
		{"nil (çağıran hatası)", nil, ToolErrInternal, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ClassifyToolError(c.err)
			if got.Error != c.wantClass {
				t.Errorf("class = %q, beklenen %q (hata: %v)", got.Error, c.wantClass, c.err)
			}
			if got.Retryable != c.wantRetry {
				t.Errorf("retryable = %v, beklenen %v", got.Retryable, c.wantRetry)
			}
			if strings.TrimSpace(got.Hint) == "" {
				t.Errorf("ipucu boş — sınıf %q eyleme dönük olmalı", got.Error)
			}
			if c.err != nil && got.Detail == "" {
				t.Errorf("detail boş — ham metin tamamen kaybolmamalı")
			}
		})
	}
}

// TestClassifyToolErrorHintsAreActionable — her sınıfın ipucu SOMUT
// bir vida ya da tool adı anmalı. "Girdiyi kontrol edin" sınıfı öğüt
// modelin bir sonraki turunu değiştirmiyor; bu test o kaymayı tutar.
func TestClassifyToolErrorHintsAreActionable(t *testing.T) {
	for class, p := range toolErrPolicy {
		if !strings.ContainsAny(p.hint, "_") && !strings.Contains(p.hint, "tool") {
			t.Errorf("%s ipucu somut bir vida/tool anmıyor: %q", class, p.hint)
		}
		if utf8.RuneCountInString(p.hint) > 220 {
			t.Errorf("%s ipucu çok uzun (%d rune) — her hatada bağlam yiyor",
				class, utf8.RuneCountInString(p.hint))
		}
	}
}

// TestToolErrorJSONCapsDriverDump — ASIL bug. Sürücü dökümünün
// kuyruğu modele giden metne ASLA ulaşmamalı.
func TestToolErrorJSONCapsDriverDump(t *testing.T) {
	tail := "KUYRUK_SIZINTI_İMZASI"
	dump := "code: 241, DB::Exception: Memory limit (total) exceeded: " +
		strings.Repeat("while executing AggregatingTransform over spans_local, ", 200) + tail
	out := ToolErrorJSON(errors.New(dump))

	if strings.Contains(out, tail) {
		t.Fatalf("kırpma çalışmadı: dökümün kuyruğu content'e sızdı (%d bayt)", len(out))
	}
	var te ToolError
	if err := json.Unmarshal([]byte(out), &te); err != nil {
		t.Fatalf("content geçerli JSON değil: %v", err)
	}
	if n := utf8.RuneCountInString(te.Detail); n > toolErrDetailMaxRunes+1 {
		t.Fatalf("detail %d rune, tavan %d(+1 ellipsis)", n, toolErrDetailMaxRunes)
	}
	if !strings.HasSuffix(te.Detail, "…") {
		t.Errorf("kırpma İLAN EDİLMEDİ — sessiz kesme yasak sınıf: %q", te.Detail)
	}
	// Teşhis için gereken baş kısım KORUNMALI: kırpma "hiçbir şey
	// söyleme"ye dönüşürse sözleşme işe yaramaz.
	if !strings.Contains(te.Detail, "code: 241") {
		t.Errorf("hatanın teşhis edilebilir başı kayboldu: %q", te.Detail)
	}
	if te.Error != ToolErrBackendUnavailable {
		t.Errorf("class = %q, beklenen %q", te.Error, ToolErrBackendUnavailable)
	}
}

// TestCapRunesMultibyte — bayttan kesme çok baytlı runeyi ikiye böler
// ve JSON kodlayıcı U+FFFD üretir; Türkçe servis adları bu yolda
// sürekli geçiyor (clipStepPreview'ün aynı dersi).
func TestCapRunesMultibyte(t *testing.T) {
	s := strings.Repeat("ö", 500) // 2 bayt/rune
	got := capRunes(s, 10)
	if !utf8.ValidString(got) {
		t.Fatalf("geçersiz UTF-8 üretildi: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != 11 { // 10 + "…"
		t.Fatalf("rune sayısı %d, beklenen 11", n)
	}
	if got := capRunes("kısa", 100); got != "kısa" {
		t.Errorf("tavanın altındaki metin değişmemeli: %q", got)
	}
}

// timeoutErr — net.Error taklidi (ES taşıma katmanının şekli).
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "es transport failure" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestClassifyNetTimeout(t *testing.T) {
	var e net.Error = timeoutErr{}
	if got := ClassifyToolError(fmt.Errorf("search: %w", e)); got.Error != ToolErrTimeout {
		t.Fatalf("net.Error timeout sınıfı = %q, beklenen %q", got.Error, ToolErrTimeout)
	}
	// os.ErrDeadlineExceeded de net.Error yolundan geçer.
	if got := ClassifyToolError(os.ErrDeadlineExceeded); got.Error != ToolErrTimeout {
		t.Fatalf("os.ErrDeadlineExceeded sınıfı = %q", got.Error)
	}
	_ = time.Second
}

// TestMCPPackageHasNoCoremetryImports — sınıflandırıcının NEDEN bu
// pakette yaşadığının şartı.
//
// internal/mcp bilerek sıfır coremetry bağımlılığı taşır (protokol
// katmanı depolamadan bağımsız). Bu tutuyor olduğu için mcptools ve
// api'nin İKİSİ de buradan import edebiliyor; bir gün biri buraya
// chstore/logstore sokarsa sınıflandırıcı taşınmak zorunda kalır ve
// import döngüsü riski geri gelir. Kapı ucuz, kayma sessiz.
func TestMCPPackageHasNoCoremetryImports(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("hiç .go dosyası bulunamadı — kapı kör koşuyor")
	}
	scanned := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue // test dosyaları serbest: derlenen ikiliye girmiyorlar
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		if i := strings.Index(string(b), `"github.com/cilcenk/coremetry/`); i >= 0 {
			t.Errorf("%s coremetry paketi import ediyor — protokol katmanı depolamadan "+
				"bağımsız kalmalı (toolerr.go paket yorumu)", f)
		}
	}
	if scanned == 0 {
		t.Fatal("yalnız test dosyaları tarandı — kapı kör")
	}
}
