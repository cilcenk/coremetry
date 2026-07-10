package rag

import "strings"

// chunk.go — paragraf-duyarlı pure splitter. Hedef ~chunkTargetRunes
// karakterlik parçalar (≈500 token); paragraf sınırları korunur,
// tek başına hedefi aşan dev paragraf kelime sınırından bölünür.
// Boş/beyaz-boşluk parçalar hiç üretilmez.

const (
	chunkTargetRunes = 2000 // ≈500 token (TR/EN karışık ~4 char/token)
	chunkMaxRunes    = 3000 // tek paragraf taşması için sert tavan
)

// ChunkText — metni retrieval parçalarına böler.
func ChunkText(text string) []string {
	paras := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n")
	var out []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}
	for _, p := range paras {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Dev paragraf: kelime sınırından sert böl.
		for len([]rune(p)) > chunkMaxRunes {
			cut := chunkMaxRunes
			r := []rune(p)
			// geriye doğru en yakın boşluk (yarım kelime bırakma)
			for cut > chunkTargetRunes && r[cut] != ' ' && r[cut] != '\n' {
				cut--
			}
			flush()
			out = append(out, strings.TrimSpace(string(r[:cut])))
			p = strings.TrimSpace(string(r[cut:]))
		}
		if cur.Len() > 0 && len([]rune(cur.String()))+len([]rune(p)) > chunkTargetRunes {
			flush()
		}
		if cur.Len() > 0 {
			cur.WriteString("\n\n")
		}
		cur.WriteString(p)
	}
	flush()
	return out
}
