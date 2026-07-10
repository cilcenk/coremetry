package rag

import (
	"strings"
	"testing"
)

// chunk_test.go — v0.8.438. Paragraf korunumu, hedef/tavan boyları,
// dev-paragraf kelime-sınırı bölmesi, boş girdi — her dal (unit-mixing).
func TestChunkText(t *testing.T) {
	t.Run("empty and whitespace-only → no chunks", func(t *testing.T) {
		if got := ChunkText(""); len(got) != 0 {
			t.Fatalf("empty: %d chunks", len(got))
		}
		if got := ChunkText("\n\n   \n\n"); len(got) != 0 {
			t.Fatalf("whitespace: %d chunks", len(got))
		}
	})

	t.Run("small paragraphs coalesce into one chunk", func(t *testing.T) {
		got := ChunkText("birinci paragraf.\n\nikinci paragraf.")
		if len(got) != 1 || !strings.Contains(got[0], "birinci") || !strings.Contains(got[0], "ikinci") {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("paragraph boundary respected at target size", func(t *testing.T) {
		a := strings.Repeat("aa ", 600) // ~1800 rune
		b := strings.Repeat("bb ", 600)
		got := ChunkText(a + "\n\n" + b)
		if len(got) != 2 {
			t.Fatalf("want 2 chunks, got %d", len(got))
		}
		if strings.Contains(got[0], "bb") || strings.Contains(got[1], "aa") {
			t.Fatal("paragraphs bled across chunks")
		}
	})

	t.Run("giant paragraph hard-splits on a word boundary", func(t *testing.T) {
		giant := strings.Repeat("kelime ", 1000) // ~7000 rune tek paragraf
		got := ChunkText(giant)
		if len(got) < 2 {
			t.Fatalf("giant paragraph must split, got %d", len(got))
		}
		for i, c := range got {
			r := []rune(c)
			if len(r) > chunkMaxRunes {
				t.Fatalf("chunk %d over hard cap: %d", i, len(r))
			}
			if strings.HasPrefix(c, "elime") { // yarım kelime kontrolü
				t.Fatalf("chunk %d starts mid-word: %q", i, c[:20])
			}
		}
	})

	t.Run("windows newlines normalized", func(t *testing.T) {
		got := ChunkText("a\r\n\r\nb")
		if len(got) != 1 {
			t.Fatalf("got %d", len(got))
		}
	})
}
