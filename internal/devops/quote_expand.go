package devops

import (
	"regexp"
	"strconv"
	"strings"
)

// quote_expand.go — v0.10.544 (operatör 2026-09-07, CoSRE "Kod incelemesi":
// "bir satır öncesi bir satır sonrası kod bloğunda gözükse ama ilgili satır
// highlight daha iyi"). Model kod alıntısını çoğunlukla TEK satır olarak
// veriyor (`// yol:62` + `62| …`); bağlamsız tek satır okunmuyor. Prompt'la
// "±1 satır ver" demek küçük modelde güvenilir değil; sunucu elindeki kaynak
// PENCERESİNDEN deterministik yeniden kurar: alıntı aralığının bir öncesi ve
// bir sonrası eklenir, alıntılanan satır(lar) `>>>` ile vurgulanır (FE
// codeQuote.ts stripMarker/gutterLines sözleşmesi, Markdown.tsx codeLineMark).
//
// Kurallar:
//   - Yalnız `// yol:a[-b]` başlıklı çitler; yol pencereyle sonek eşleşir.
//   - Alıntı ≤ 3 satırsa genişletilir (±1, pencere sınırında kırpılır);
//     daha uzun blok zaten bağlam taşır, DOKUNULMAZ.
//   - Vurgu: gövdede `>>>` işaretli satırlar varsa onlar; yoksa a..b.
//   - Satır metni PENCEREDEN alınır (modelin yeniden yazdığı metin değil):
//     kaynak alıntısı kaynağın kendisi olmalı.
//   - Pencerede olmayan aralık / bilinmeyen yol → blok aynen.

var (
	quoteHdrRe    = regexp.MustCompile(`^\s*(?://|#|--)\s*([^\s:]+):(\d+)(?:-(\d+))?\s*(?:\([^)]*\))?\s*$`)
	quoteGutterRe = regexp.MustCompile(`^\s*(?:>>>\s?)?(\d{1,6})\|\s?(.*)$`)
	quoteMarkRe   = regexp.MustCompile(`^\s*>>>`)
)

const quoteExpandMaxSpan = 3

// windowLines — pencere içeriği (N| metin) → satır no → metin.
func windowLines(w CodeWindow) map[int]string {
	out := map[int]string{}
	for _, l := range strings.Split(w.Content, "\n") {
		m := quoteGutterRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err == nil {
			out[n] = m[2]
		}
	}
	return out
}

func pathMatches(header, window string) bool {
	h := strings.TrimLeft(strings.TrimSpace(header), "/")
	w := strings.TrimLeft(strings.TrimSpace(window), "/")
	if h == "" || w == "" {
		return false
	}
	return h == w || strings.HasSuffix(w, "/"+h) || strings.HasSuffix(h, "/"+w)
}

func (c CodeContext) windowFor(path string) (CodeWindow, bool) {
	for _, w := range c.Windows {
		if pathMatches(path, w.Path) {
			return w, true
		}
	}
	return CodeWindow{}, false
}

// ExpandQuotes — cevap metnindeki uygun çitleri yeniden kurar; diğer her
// şey bayt-bayt korunur. SAF.
func ExpandQuotes(answer string, cc CodeContext) string {
	if cc.Empty() || !strings.Contains(answer, "```") {
		return answer
	}
	lines := strings.Split(answer, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if !strings.HasPrefix(strings.TrimSpace(l), "```") {
			out = append(out, l)
			continue
		}
		// çit açıldı: kapanışı bul
		j := i + 1
		for j < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[j]), "```") {
			j++
		}
		if j >= len(lines) { // kapanmamış çit — aynen
			out = append(out, lines[i:]...)
			break
		}
		block := lines[i : j+1]
		out = append(out, expandBlock(block, cc)...)
		i = j
	}
	return strings.Join(out, "\n")
}

// expandBlock — [çit, başlık, gövde…, çit] → genişletilmiş ya da aynen.
func expandBlock(block []string, cc CodeContext) []string {
	if len(block) < 3 {
		return block
	}
	m := quoteHdrRe.FindStringSubmatch(block[1])
	if m == nil {
		return block
	}
	a, _ := strconv.Atoi(m[2])
	b := a
	if m[3] != "" {
		b, _ = strconv.Atoi(m[3])
	}
	if a <= 0 || b < a || b-a+1 > quoteExpandMaxSpan {
		return block
	}
	w, ok := cc.windowFor(m[1])
	if !ok {
		return block
	}
	src := windowLines(w)
	if _, has := src[a]; !has {
		return block
	}
	hl := map[int]bool{}
	for _, l := range block[2 : len(block)-1] {
		if quoteMarkRe.MatchString(l) {
			if g := quoteGutterRe.FindStringSubmatch(l); g != nil {
				if n, err := strconv.Atoi(g[1]); err == nil {
					hl[n] = true
				}
			}
		}
	}
	if len(hl) == 0 {
		for n := a; n <= b; n++ {
			hl[n] = true
		}
	}
	from, to := a-1, b+1
	out := []string{block[0], block[1]}
	for n := from; n <= to; n++ {
		text, has := src[n]
		if !has {
			continue
		}
		prefix := ""
		if hl[n] {
			prefix = ">>> "
		}
		out = append(out, prefix+strconv.Itoa(n)+"| "+text)
	}
	out = append(out, block[len(block)-1])
	return out
}
