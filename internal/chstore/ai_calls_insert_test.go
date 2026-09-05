package chstore

// v0.10.409 — ai_calls iki-boot sözleşmesi. İnceleme bulgusu: edit
// betiğinin çift koşumu genişletilmiş INSERT bloğunu üç kez ekleyip eski
// kolon dalını ULAŞILMAZ yapmıştı — ilk boot'ta (kolonlar yokken) her
// AI çağrısı kaydı Distributed tarafından reddedilecekti. Her iki dal
// saf yardımcılarla pinlenir: SQL kolon sayısı == arg sayısı.

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestAICallsInsertBranches(t *testing.T) {
	c := AICall{ID: "x", PromptVersion: "v", ErrorClass: "timeout", TTFTMs: 12, StreamFallback: true, ShieldHits: 1}
	for _, ext := range []bool{false, true} {
		sql := aiCallsInsertSQL(ext)
		inner := sql[strings.Index(sql, "(")+1 : strings.LastIndex(sql, ")")]
		n := len(strings.Split(inner, ","))
		args := aiCallsInsertArgs(c, time.Unix(0, 0).UTC(), ext)
		if n != len(args) {
			t.Fatalf("ext=%v: SQL %d kolon, %d arg", ext, n, len(args))
		}
		for _, col := range strings.Split(aiCallsExtCols, ", ") {
			if strings.Contains(inner, col) != ext {
				t.Errorf("ext=%v: kolon %q beklenmedik durumda", ext, col)
			}
		}
		if ext && args[len(args)-2] != uint8(1) {
			t.Errorf("stream_fallback UInt8(1) olmalı, %v", args[len(args)-2])
		}
	}
	if aiCallsInsertSQL(false) == aiCallsInsertSQL(true) {
		t.Fatal("iki dal aynı SQL'i üretiyor")
	}
}

// Probe yalnız INSERT hedefine bakar: yerel parçada olup Distributed'da
// henüz olmayan kolon "var" sayılırsa INSERT kırılır.
func TestAICallsProbeTargetsInsertTable(t *testing.T) {
	b, err := os.ReadFile("ai_calls.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if strings.Contains(src, "'ai_calls_local'") {
		t.Fatal("probe yerel parçaya bakmamalı — INSERT hedefi ai_calls")
	}
	if !strings.Contains(src, "table = 'ai_calls' AND name = 'error_class'") {
		t.Fatal("probe `table = 'ai_calls'` yüklemini taşımalı")
	}
}
