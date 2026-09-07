package devops

import (
	"strings"
	"testing"
)

// v0.10.544 — kod alıntısı ±1 satır bağlam + vurgu (operatör isteği).
func TestExpandQuotes(t *testing.T) {
	cc := CodeContext{Repo: "r", Windows: []CodeWindow{{
		Path: "DCManagement/src/com/example/util/HostResponseControlUtil.java", FromLine: 60, ToLine: 66,
		Content: "60| a\n61| b\n62| HostResponseControlUtil.HandleHostResponse(retcode, response);\n63| c\n64| d\n65| e\n66| f",
	}}}
	in := "Kök neden: …\n```java\n// /DCManagement/src/com/example/util/HostResponseControlUtil.java:62\n62| HostResponseControlUtil.HandleHostResponse(retcode, resp);\n```\nSonuç."
	got := ExpandQuotes(in, cc)
	want := "Kök neden: …\n```java\n// /DCManagement/src/com/example/util/HostResponseControlUtil.java:62\n61| b\n>>> 62| HostResponseControlUtil.HandleHostResponse(retcode, response);\n63| c\n```\nSonuç."
	if got != want {
		t.Fatalf("tek satır → ±1 + vurgu, pencere metni:\n%s\nwant\n%s", got, want)
	}
	// Pencere kenarı: 60 alıntısı → 60-61 (59 yok).
	edge := "```java\n// HostResponseControlUtil.java:60\n60| a\n```"
	if got := ExpandQuotes(edge, cc); got != "```java\n// HostResponseControlUtil.java:60\n>>> 60| a\n61| b\n```" {
		t.Fatalf("kenar: %q", got)
	}
	// Gövdedeki >>> işaretleri vurgu kümesidir (aralık 2 satır, biri işaretli).
	marked := "```java\n// HostResponseControlUtil.java:62-63\n62| x\n>>> 63| y\n```"
	if got := ExpandQuotes(marked, cc); got != "```java\n// HostResponseControlUtil.java:62-63\n61| b\n62| HostResponseControlUtil.HandleHostResponse(retcode, response);\n>>> 63| c\n64| d\n```" {
		t.Fatalf("işaretli vurgu: %q", got)
	}
	// Uzun blok, bilinmeyen yol, pencere dışı aralık, kapanmamış çit → aynen.
	for _, keep := range []string{
		"```java\n// HostResponseControlUtil.java:60-64\n60| a\n61| b\n62| c\n63| d\n64| e\n```",
		"```java\n// Other.java:62\n62| z\n```",
		"```java\n// HostResponseControlUtil.java:99\n99| z\n```",
		"```java\n// HostResponseControlUtil.java:62\n62| z",
		"düz metin ```inline``` yok",
	} {
		if got := ExpandQuotes(keep, cc); got != keep {
			t.Errorf("aynen kalmalı:\n%s\ngot\n%s", keep, got)
		}
	}
	if ExpandQuotes(in, CodeContext{}) != in {
		t.Fatal("pencere yokken dokunma")
	}
	if !strings.Contains(ExpandQuotes("```\n// HostResponseControlUtil.java:62 (frame)\n62| q\n```", cc), ">>> 62| HostResponseControlUtil") {
		t.Fatal("parantezli başlık da çözülmeli")
	}
}
