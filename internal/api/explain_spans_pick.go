package api

import (
	"sort"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// pickExplainSpans (v0.9.462, dürüstlük A6) — copilot trace-explain'in
// span seçimi. Eski davranış kronolojik ilk-cap dilimiydi: 5000 span'lik
// trace'te hatalı/en yavaş span'lar 100'ün dışında kalınca model onları
// hiç görmüyor, "en yavaş span" kanıtı yanlış span'ı işaret
// edebiliyordu. Sıra garantisi: hatalar (en çok cap/2) → en yavaşlar →
// kronolojik dolgu; seçilen küme zaman sırasına geri konur (model akışı
// sırayla okur). cap'ten küçük trace aynen döner. Saf — tablo-testli.
func pickExplainSpans(spans []chstore.SpanRow, cap int) []chstore.SpanRow {
	if len(spans) <= cap {
		return spans
	}
	picked := make(map[int]bool, cap)
	n := 0
	// 1) Hatalar — en çok cap/2 (hata fırtınası diğer sinyalleri boğmasın).
	for i, sp := range spans {
		if n >= cap/2 {
			break
		}
		if sp.StatusCode == "error" {
			picked[i] = true
			n++
		}
	}
	// 2) En yavaşlar — kalan bütçenin yarısı.
	idx := make([]int, 0, len(spans))
	for i := range spans {
		if !picked[i] {
			idx = append(idx, i)
		}
	}
	sort.Slice(idx, func(a, b int) bool {
		da := spans[idx[a]].EndTime - spans[idx[a]].StartTime
		db := spans[idx[b]].EndTime - spans[idx[b]].StartTime
		return da > db
	})
	slowBudget := (cap - n + 1) / 2
	for _, i := range idx {
		if slowBudget <= 0 || n >= cap {
			break
		}
		picked[i] = true
		n++
		slowBudget--
	}
	// 3) Kronolojik dolgu — kalan bütçe baştan (kök/akış bağlamı).
	for i := range spans {
		if n >= cap {
			break
		}
		if !picked[i] {
			picked[i] = true
			n++
		}
	}
	out := make([]chstore.SpanRow, 0, n)
	for i := range spans {
		if picked[i] {
			out = append(out, spans[i])
		}
	}
	return out
}
