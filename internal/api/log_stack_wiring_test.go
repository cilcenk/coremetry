package api

// v0.9.1182 — TEK-ANAHTAR okumasının geri gelmemesi (kaynak kapısı).
//
// fromlog_test.go çözücüyü ölçüyor; bu test BAĞLANMAYI ölçüyor ve ayrı
// olmasının sebebi somut: kusur çözücüde değildi, çözücü YOKTU. İki okuma
// yolu da `lg.Attributes["exception.stacktrace"]` diye tek bir yazıma
// bakıyordu ve o anahtar boş olan her kurulumda (ECS'in alanı
// `error.stack_trace`; Java'nın yaygın deseninde stack gövdenin içinde)
// "bu kayıtta stacktrace yok" diyordu. Yani kırılan şey bir ifade değil,
// bir VARSAYIMDI — ve varsayımlar ancak kaynağa bakan bir kapıyla
// çivilenir.
//
// Kapı DAR: yalnız log okuyan iki yol. Span EVENT'lerinden okuyan yerler
// (chstore/exception.go JSON_VALUE'ları) kapsam DIŞI ve olmalı da: orada
// kaynak OTel span event'i, yazım gerçekten `exception.stacktrace` ve
// başka bir yazım hiç doğmuyor.

import (
	"os"
	"strings"
	"testing"
)

func TestLogStackReadersUseTheResolver(t *testing.T) {
	for _, f := range []struct{ path, what string }{
		{"explain_trace_input.go", "trace explain kanıt paketi"},
		{"../anomaly/exception_context.go", "exception kök-sebep girdisi"},
	} {
		b, err := os.ReadFile(f.path)
		if err != nil {
			t.Fatalf("%s okunamadı: %v", f.path, err)
		}
		src := string(b)
		if !strings.Contains(src, "stackparse.FromLog(") {
			t.Errorf("%s (%s): stackparse.FromLog kullanılmıyor — stack yine tek "+
				"yazımdan okunuyor olabilir (v0.9.1182)", f.path, f.what)
		}
		// Doğrudan öznitelik okuması geri gelmemeli. Yorumlarda anahtarın
		// ADI geçebilir (gerekçe anlatılıyor), o yüzden aranan şey İFADE:
		// `Attributes["exception.stacktrace"]`.
		if strings.Contains(src, `Attributes["exception.stacktrace"]`) {
			t.Errorf("%s (%s): `Attributes[\"exception.stacktrace\"]` doğrudan "+
				"okunuyor — ECS alanını (`error.stack_trace`) ve gövdedeki "+
				"stack'i kaçırır, v0.9.1182'nin düzelttiği bug", f.path, f.what)
		}
	}
}
