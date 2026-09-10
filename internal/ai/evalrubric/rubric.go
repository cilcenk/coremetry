// Package evalrubric — v0.10.666 (CoSRE değerlendirme programı Faz A,
// docs/audit/cosre-evaluation-program-2026-09-10.md §3).
//
// Evalset replay'i (internal/api/evalset_test.go, -tags evalset) bugüne
// kadar İKİLİ kapı üretiyordu: kalkan geçti / geçmedi. Bu paket her vakayı
// BOYUTLARA ayırıp 0–1 arası ağırlıklı bir skora indirger, koşumu bir JSON
// artefakta yazar ve iki koşumu diff'ler — "prompt v42 v41'den iyi mi"
// sorusu sayıyla cevaplanır.
//
// DETERMİNİSTİK: hiçbir boyut LLM yargıcı kullanmaz (o Faz B, operatör
// kararı). Boyutlar:
//
//	grounded          0/1   kalkanın bilinmeyen varlık sayısı ≤ vakanın tavanı
//	answers_question  0..2  mustContain kapsanma oranı (2×); mustNotContain ihlali → 0
//	language_tr       0/1   düz metin yüzeyde Türkçe (heuristik); JSON yüzeyde n/a
//	length_ok         0/1   answer ≤ MaxChars (0 = n/a)
//	tool_calls_valid  0/1   yalnız serbest döngü yüzeylerinde (nil = n/a)
//
// n/a boyutlar ağırlıktan DÜŞER (yeniden normalize); böylece JSON yüzeyi
// dil boyutundan ceza almaz. Eşik 0,8 (evaluator-optimizer deseni) — bu
// dilimde KAPI DEĞİL, yalnız raporlanır.
package evalrubric

import (
	"math"
	"regexp"
	"strings"
)

// Threshold — "yeterince iyi" eşiği (raporlanır, kapı değil).
const Threshold = 0.8

// Dim — bir rubrik boyutu. Max 0 ise n/a (ağırlığa girmez).
type Dim struct {
	Name   string  `json:"name"`
	Score  float64 `json:"score"`
	Max    float64 `json:"max"`
	Weight float64 `json:"weight"`
	Note   string  `json:"note,omitempty"`
}

// Result — bir vakanın rubrik sonucu. Total 0..1 (uygulanabilir boyutların
// ağırlıklı ortalaması), Applicable = ağırlığa giren boyut sayısı.
type Result struct {
	Dims       []Dim   `json:"dims"`
	Total      float64 `json:"total"`
	Applicable int     `json:"applicable"`
}

// Input — vaka beklentisi + model çıktısı. Skorlayıcı yalnız buradaki
// alanlara bakar; evalset dosya şekli (evalCase) çağıranın işi.
type Input struct {
	Answer         string
	Err            error
	UnknownCount   int // kalkanın kanıtta bulamadığı ad/sayı sayısı
	MaxUnknown     int // vakanın tavanı (0 = hiç olmamalı)
	MustContain    []string
	MustNotContain []string
	ExpectTurkish  bool  // düz metin yüzey: dil boyutu uygulanır
	MaxChars       int   // 0 = n/a
	ToolCallsValid *bool // nil = n/a
}

// Ağırlıklar — toplam 1,0; n/a boyutlar düşünce yeniden normalize edilir.
const (
	wGrounded = 0.40
	wAnswers  = 0.30
	wLanguage = 0.10
	wLength   = 0.10
	wTools    = 0.10
)

// Score — deterministik rubrik. Err != nil → her uygulanabilir boyut 0
// (cevap yok), ama boyut listesi yine döner (diff'te "kayboldu" değil "0").
func Score(in Input) Result {
	var dims []Dim
	failed := in.Err != nil

	// grounded
	g := Dim{Name: "grounded", Max: 1, Weight: wGrounded}
	if !failed && in.UnknownCount <= in.MaxUnknown {
		g.Score = 1
	} else if !failed {
		g.Note = "kanıtsız ad/sayı: " + itoa(in.UnknownCount) + " > " + itoa(in.MaxUnknown)
	}
	dims = append(dims, g)

	// answers_question
	a := Dim{Name: "answers_question", Max: 2, Weight: wAnswers}
	if !failed {
		low := strings.ToLower(in.Answer)
		violated := false
		for _, s := range in.MustNotContain {
			if s != "" && strings.Contains(low, strings.ToLower(s)) {
				violated = true
				a.Note = "yasak ifade: " + s
				break
			}
		}
		switch {
		case violated:
			a.Score = 0
		case len(in.MustContain) == 0:
			a.Score = 2 // beklenti yok: cevap var ve yasak yok
		default:
			hit := 0
			for _, s := range in.MustContain {
				if s != "" && strings.Contains(low, strings.ToLower(s)) {
					hit++
				}
			}
			a.Score = 2 * float64(hit) / float64(len(in.MustContain))
			if hit < len(in.MustContain) {
				a.Note = itoa(hit) + "/" + itoa(len(in.MustContain)) + " beklenen ifade"
			}
		}
	}
	dims = append(dims, a)

	// language_tr
	l := Dim{Name: "language_tr", Weight: wLanguage}
	if in.ExpectTurkish {
		l.Max = 1
		if !failed && IsTurkish(in.Answer) {
			l.Score = 1
		} else if !failed {
			l.Note = "Türkçe değil"
		}
	}
	dims = append(dims, l)

	// length_ok
	ln := Dim{Name: "length_ok", Weight: wLength}
	if in.MaxChars > 0 {
		ln.Max = 1
		if !failed && len([]rune(in.Answer)) <= in.MaxChars {
			ln.Score = 1
		} else if !failed {
			ln.Note = itoa(len([]rune(in.Answer))) + " > " + itoa(in.MaxChars) + " karakter"
		}
	}
	dims = append(dims, ln)

	// tool_calls_valid
	tc := Dim{Name: "tool_calls_valid", Weight: wTools}
	if in.ToolCallsValid != nil {
		tc.Max = 1
		if !failed && *in.ToolCallsValid {
			tc.Score = 1
		} else if !failed {
			tc.Note = "geçersiz tool çağrısı"
		}
	}
	dims = append(dims, tc)

	return finalize(dims)
}

// finalize — n/a boyutları (Max 0) ağırlıktan düşürüp yeniden normalize eder.
func finalize(dims []Dim) Result {
	var wsum, acc float64
	n := 0
	for _, d := range dims {
		if d.Max <= 0 {
			continue
		}
		wsum += d.Weight
		acc += d.Weight * (d.Score / d.Max)
		n++
	}
	r := Result{Dims: dims, Applicable: n}
	if wsum > 0 {
		r.Total = math.Round(acc/wsum*1000) / 1000
	}
	return r
}

// Türkçe heuristiği: Türkçeye özgü harfler VEYA sık Türkçe bağlaçlar,
// İngilizce sık sözcüklerden daha fazlaysa Türkçe. Kısa/boş metin → false.
var (
	trLetters = regexp.MustCompile(`[çğıöşüÇĞİÖŞÜ]`)
	trWords   = regexp.MustCompile(`(?i)\b(ve|için|bir|değil|ile|bu|olarak|servis|hata|neden|sonra|ama|çünkü|artış|düşüş)\b`)
	enWords   = regexp.MustCompile(`(?i)\b(the|and|is|are|with|this|that|because|error|service|increase)\b`)
)

// IsTurkish — deterministik dil sezgisi (yargıç değil). Türkçe harf
// sayısı ≥ 2 ya da Türkçe sözcük sayısı > İngilizce sözcük sayısı.
func IsTurkish(text string) bool {
	t := strings.TrimSpace(text)
	if len(t) < 8 {
		return false
	}
	if len(trLetters.FindAllString(t, -1)) >= 2 {
		return true
	}
	return len(trWords.FindAllString(t, -1)) > len(enWords.FindAllString(t, -1))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
