package mcptools

// exception_samples_test.go — v0.9.1233 saf çekirdek pinleri.
//
// Bu tool'un tüm riski saf seamlerde: pencere kelepçesi, RUNE tavanları
// (byte kesmesi UTF-8'i böler ve model bozuk stack okur), pencere dışı
// süzmenin sebebini SÖYLEMESİ. Okuma tarafı mevcut ve zaten testli
// (chstore.GetExceptionGroupSamples).

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestExSamplesWindowS(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 3600},            // varsayılan 1h
		{-5, 3600},           // negatif → varsayılan
		{60, 60},             // kova TABANI yok: 60 sn gerçekten 60 sn
		{7200, 7200},         // aralıkta aynen
		{7 * 86400, 604800},  // tam tavan
		{99 * 86400, 604800}, // tavanın üstü kelepçelenir
	}
	for _, c := range cases {
		if got := exSamplesWindowS(c.in); got != c.want {
			t.Errorf("exSamplesWindowS(%d) = %d, beklenen %d", c.in, got, c.want)
		}
	}
}

// RUNE tavanı — çok baytlı girdiyle. Byte kesmesi olsaydı çıktı geçersiz
// UTF-8 olurdu; ayrıca kesildiği metnin İÇİNDE yazmalı.
func TestCapRunesSpoken(t *testing.T) {
	tr := strings.Repeat("ış", 400) // 800 rune, 1600 byte

	got, cut := capRunesSpoken(tr, 300)
	if !cut {
		t.Fatal("800 rune, 300 tavanında kesilmeliydi")
	}
	head := []rune(got)[:300]
	if string(head) != string([]rune(tr)[:300]) {
		t.Error("kesim rune sınırında değil — çok baytlı karakter bölünmüş")
	}
	if !strings.Contains(got, "truncated") || !strings.Contains(got, "800") {
		t.Errorf("kesildiği metnin İÇİNDE söylenmiyor: %q", got[len(got)-60:])
	}

	// Tam sınırda kesilmez.
	exact := strings.Repeat("a", 300)
	if got, cut := capRunesSpoken(exact, 300); cut || got != exact {
		t.Errorf("tam sınır kesilmemeli (cut=%v)", cut)
	}
	// Kısa metin dokunulmadan geçer.
	if got, cut := capRunesSpoken("kısa", 300); cut || got != "kısa" {
		t.Errorf("kısa metin değişmemeli: %q cut=%v", got, cut)
	}
}

func TestExSampleRowsShapeAndCaps(t *testing.T) {
	longStack := strings.Repeat("çerçeve\n", 500) // 4000 rune
	rows, anyCut := exSampleRows("bsa-pay", "java.lang.NullPointerException", []chstore.ExceptionSample{{
		TraceID:    "abc123",
		SpanID:     "def456",
		Time:       1700000000000000000,
		Message:    "order 42 not found",
		Stacktrace: longStack,
		SpanName:   "POST /api/v1/pay",
		StatusMsg:  "NPE",
	}})
	if len(rows) != 1 {
		t.Fatalf("1 satır beklenirdi, %d geldi", len(rows))
	}
	r := rows[0]
	if r.Service != "bsa-pay" || r.ExType != "java.lang.NullPointerException" {
		t.Errorf("servis/tip satıra taşınmamış: %+v", r)
	}
	if r.TraceID != "abc123" || r.SpanID != "def456" || r.SpanName != "POST /api/v1/pay" {
		t.Errorf("pivot alanları yanlış eşlendi: %+v", r)
	}
	if r.TimeISO != "2023-11-14T22:13:20Z" {
		t.Errorf("time_iso yanlış: %q", r.TimeISO)
	}
	if r.Message != "order 42 not found" {
		t.Errorf("kısa mesaj değiştirilmiş: %q", r.Message)
	}
	if !anyCut {
		t.Error("4000 rune'luk stack kesilmeliydi → truncated true")
	}
	if !strings.Contains(r.Stacktrace, "truncated") {
		t.Error("stack kesildi ama metnin içinde söylenmiyor")
	}
	if n := len([]rune(r.Stacktrace)); n > exSampleStackMaxRunes+80 {
		t.Errorf("stack tavanı uygulanmamış: %d rune", n)
	}
	// MESAJ tavanı ayrı bir dal — stack kısa, mesaj uzun.
	rows2, cut2 := exSampleRows("s", "T", []chstore.ExceptionSample{{
		Message: strings.Repeat("ö", 900), Stacktrace: "kısa stack",
	}})
	if !cut2 {
		t.Error("900 rune'luk mesaj kesilmeliydi")
	}
	if n := len([]rune(rows2[0].Message)); n > exSampleMsgMaxRunes+80 {
		t.Errorf("mesaj tavanı uygulanmamış: %d rune", n)
	}
	if rows2[0].Stacktrace != "kısa stack" {
		t.Error("kısa stack mesaj kesilirken bozulmuş")
	}
	// Hiçbir tavan ısırmıyorsa truncated FALSE kalmalı (yoksa bayrak
	// tören olur ve model her yanıtı yarım sanar).
	if _, cut := exSampleRows("s", "T", []chstore.ExceptionSample{{Message: "a", Stacktrace: "b"}}); cut {
		t.Error("kesim yokken truncated true")
	}
}

func TestExSamplesInWindow(t *testing.T) {
	const cutoff = 1000
	in := []chstore.ExceptionSample{
		{TraceID: "yeni", Time: 2000},
		{TraceID: "sınır", Time: 1000}, // kapsayıcı: >= cutoff
		{TraceID: "eski", Time: 999},
	}
	kept, dropped := exSamplesInWindow(in, cutoff)
	if len(kept) != 2 || dropped != 1 {
		t.Fatalf("kept=%d dropped=%d, beklenen 2/1", len(kept), dropped)
	}
	if kept[0].TraceID != "yeni" || kept[1].TraceID != "sınır" {
		t.Errorf("sıra korunmadı: %+v", kept)
	}
	// Hepsi pencere dışıysa dropped sayısı boş cevabın SEBEBİ olur.
	if kept, dropped := exSamplesInWindow(in, 5000); len(kept) != 0 || dropped != 3 {
		t.Errorf("hepsi düşmeliydi: kept=%d dropped=%d", len(kept), dropped)
	}
}

// BOŞ cevabın dört sebebi ayrışmalı — "örnek yok" tek başına modele
// "bu istisna sorun değil" diye okunur.
func TestExSamplesReasonsDistinguishesEmptyCases(t *testing.T) {
	cases := []struct {
		name    string
		found   bool
		env     chstore.ExceptionSamples
		dropped int
		want    string
	}{
		{"parmak izi yok", false, chstore.ExceptionSamples{}, 0, "no exception group"},
		{"pencere dışı", true, chstore.ExceptionSamples{WindowExhausted: true}, 4, "older than range_s"},
		{"aday bütçesi", true, chstore.ExceptionSamples{ScanCapped: true}, 0, "candidate scan hit its ceiling"},
		{"span retention", true, chstore.ExceptionSamples{WindowExhausted: true}, 0, "span retention"},
		{"belirsiz", true, chstore.ExceptionSamples{}, 0, "widen range_s"},
	}
	for _, c := range cases {
		got := exSamplesReasons(c.found, c.env, c.dropped, 3600)
		if len(got) == 0 {
			t.Fatalf("%s: sebep listesi boş", c.name)
		}
		if !strings.Contains(got[0], c.want) {
			t.Errorf("%s: %q içinde %q yok", c.name, got[0], c.want)
		}
	}
	// Pencere-dışı sebebi SAYIYI ve range_s'i taşımalı (model neyi
	// genişleteceğini bilsin).
	got := exSamplesReasons(true, chstore.ExceptionSamples{}, 7, 1800)[0]
	if !strings.Contains(got, "7") || !strings.Contains(got, "1800") {
		t.Errorf("pencere-dışı sebebi sayı/pencere taşımıyor: %q", got)
	}
}

// Zincir komşuluğu: tool'lar tools/list'te sıralı gelir ve model
// komşuluğu okur. get_exception_samples, list_exception_groups'un
// hemen yanında durmalı — ayrı düşerse "sayıyı gördüm, stack nerede"
// sorusu katalogda cevapsız görünür.
func TestExceptionSamplesGroupedWithExceptionGroups(t *testing.T) {
	tools := ToolList(Deps{})
	pos := map[string]int{}
	for i, tool := range tools {
		pos[tool.Name] = i
	}
	si, sok := pos["get_exception_samples"]
	li, lok := pos["list_exception_groups"]
	if !sok || !lok {
		t.Fatalf("katalogda eksik: samples=%v groups=%v", sok, lok)
	}
	if diff := si - li; diff > 3 || diff < -3 {
		t.Errorf("get_exception_samples, list_exception_groups'tan %d sıra uzakta — aile birlikte dursun", diff)
	}
}

// Şema DÜRÜSTLÜĞÜ — range_s'in maliyeti düşürmediği ve stack tavanı
// şemada/açıklamada yazılı olmalı. Bir gelecek düzenleme range_s'i
// "pencere" gibi anlatırsa model ucuz sandığı bir çağrı kurar.
func TestExceptionSamplesSchemaIsHonest(t *testing.T) {
	tool := toolByName(t, ToolList(Deps{}), "get_exception_samples")
	props := schemaProps(t, tool)
	rangeProp, _ := props["range_s"].(map[string]any)
	if rangeProp == nil {
		t.Fatal("range_s property yok")
	}
	desc, _ := rangeProp["description"].(string)
	if !strings.Contains(desc, "Does NOT bound the scan") {
		t.Errorf("range_s açıklaması taramayı bağlamadığını söylemiyor: %q", desc)
	}
	if max, _ := rangeProp["maximum"].(int); max != exSamplesMaxRangeS {
		t.Errorf("range_s tavanı %v, beklenen %d", rangeProp["maximum"], exSamplesMaxRangeS)
	}
	limitProp, _ := props["limit"].(map[string]any)
	if limitProp == nil {
		t.Fatal("limit property yok")
	}
	if max, _ := limitProp["maximum"].(int); max != exSamplesMaxRows {
		t.Errorf("limit tavanı %v, beklenen %d", limitProp["maximum"], exSamplesMaxRows)
	}
	if !strings.Contains(tool.Description, "trace_id") {
		t.Error("açıklama trace pivotunu söylemiyor — zincirin asıl sebebi o")
	}
	if !strings.Contains(tool.Description, "list_exception_groups") {
		t.Error("açıklama zinciri (list_exception_groups → bu) söylemiyor")
	}
}
