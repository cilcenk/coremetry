package api

import (
	"errors"
	"testing"
)

// TestEmbedOrTextOnly guards the v0.9.173 fix — operator-reported: a down /
// misconfigured bge-m3 embed endpoint made RAG upload HARD-FAIL. Embedding is
// best-effort: when the endpoint is not Ready OR Embed errors, the document
// ingests text-only (nil embeddings, keyword retrieval), never blocking upload.
// The result length MUST always equal the chunk count.
func TestEmbedOrTextOnly(t *testing.T) {
	pieces := []string{"a", "b", "c"}

	t.Run("not ready -> text-only, embed not called", func(t *testing.T) {
		got := embedOrTextOnly(pieces, false, func([]string) ([][]float32, error) {
			t.Fatal("embed must not be called when not ready")
			return nil, nil
		})
		assertTextOnly(t, got, len(pieces))
	})

	t.Run("embed error -> text-only fallback (no hard fail)", func(t *testing.T) {
		got := embedOrTextOnly(pieces, true, func([]string) ([][]float32, error) {
			return nil, errors.New("bge-m3 connection refused")
		})
		assertTextOnly(t, got, len(pieces))
	})

	t.Run("embed ok -> embeddings passed through", func(t *testing.T) {
		want := [][]float32{{1}, {2}, {3}}
		got := embedOrTextOnly(pieces, true, func([]string) ([][]float32, error) {
			return want, nil
		})
		if len(got) != len(want) || len(got[0]) != 1 || got[0][0] != 1 {
			t.Fatalf("embeddings not passed through: %v", got)
		}
	})
}

func assertTextOnly(t *testing.T, got [][]float32, n int) {
	t.Helper()
	if len(got) != n {
		t.Fatalf("len = %d, want %d", len(got), n)
	}
	for i, e := range got {
		if e != nil {
			t.Fatalf("chunk %d: expected nil embedding (text-only), got %v", i, e)
		}
	}
}
