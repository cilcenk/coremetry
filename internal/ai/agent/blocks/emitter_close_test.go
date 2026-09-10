package blocks

// emitter_close_test.go — v0.10.648 (ai-ui-patterns bulgusu #1).
//
// SÖZLEŞME: akış başladıysa (en az bir çerçeve düştü) ve handler terminal
// bir olay (answer/error/done) basmadan döndüyse, Close() istemciye
// `event: error` ile "akış tamamlanmadan kapandı" der. Aksi hâlde istemci
// turu sonsuza dek `pending` tutar ("yazıyor▌" asılı kalır, takip çipi
// gelmez) — pod restart, panic ya da proxy kapatması bu yola düşer.
//
// Terminal olay zaten gittiyse Close HİÇBİR ŞEY yazmaz (çift `done`/`error`
// explain yüzeylerini yanlış "başarısız" gösterirdi). Hiç çerçeve düşmediyse
// de yazmaz: çağıran gerçek HTTP statüsü basar (explain sözleşmesi).
//
// Mutasyon (ölçüldü, v0.10.648): Close'taki `if e.frames > 0 && !e.terminal`
// koşulunu kaldırmak birinci vakayı, terminal bayrağını Emit'te set etmemek
// ikinci vakayı düşürür.

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func closeOutput(t *testing.T, events ...string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	em, ok := NewEmitter(rec, Options{})
	if !ok {
		t.Fatal("httptest.ResponseRecorder Flusher olmalı")
	}
	for _, ev := range events {
		em.Emit(ev, map[string]string{"x": "1"})
	}
	em.Close()
	return rec.Body.String()
}

func TestEmitterCloseWithoutTerminalEmitsError(t *testing.T) {
	out := closeOutput(t, "delta", "delta")
	if !strings.Contains(out, "event: error") || !strings.Contains(out, "stream closed before completion") {
		t.Fatalf("terminal olaysız kapanış error çerçevesi basmalı; çıktı:\n%s", out)
	}
}

func TestEmitterCloseAfterTerminalIsSilent(t *testing.T) {
	for _, term := range []string{"done", "error", "answer"} {
		out := closeOutput(t, "delta", term)
		if strings.Count(out, "event: "+term) != 1 || strings.Contains(out, "stream closed before completion") {
			t.Fatalf("%s sonrası Close ek çerçeve basmamalı; çıktı:\n%s", term, out)
		}
	}
}

func TestEmitterCloseWithoutFramesWritesNothing(t *testing.T) {
	if out := closeOutput(t); out != "" {
		t.Fatalf("çerçevesiz Close gövdeye yazmamalı: %q", out)
	}
}
