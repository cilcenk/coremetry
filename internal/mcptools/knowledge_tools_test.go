package mcptools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.10.559 — Faz 5 bilgi araçları, saf yarılar: terimler, lexical skor (başlık
// +0.25, tavan 1), kesit, /runbook URL → id çözümü (dış URL boş), runbook
// sıralaması (kapalı runbook dışarı), zarf tavanları, şema/hatalar.
func TestKnowledgeTermsAndScore(t *testing.T) {
	terms := knowledgeTerms("Kafka lag için runbook nedir? Consumer LAG, kafka")
	if strings.Join(terms, ",") != "kafka,lag,consumer" {
		t.Fatalf("terimler: %v", terms)
	}
	if s := lexicalScore(terms, "Kafka lag runbook", "kafka consumer lag artınca ..."); s != 1 {
		t.Fatalf("başlık + tüm terimler → 1, geldi %v", s)
	}
	if s := lexicalScore(terms, "x", "sadece consumer"); s != float64(1)/3 {
		t.Fatalf("1/3 bekleniyordu: %v", s)
	}
	if lexicalScore(terms, "x", "hiç") != 0 || lexicalScore(nil, "a", "a") != 0 {
		t.Fatal("eşleşme yok → 0")
	}
	body := strings.Repeat("a ", 400) + "KAFKA lag burada " + strings.Repeat("b ", 400)
	sn := snippetAround(body, terms, 120)
	if !strings.Contains(strings.ToLower(sn), "kafka") || len([]rune(sn)) > 124 || !strings.HasPrefix(sn, "…") {
		t.Fatalf("kesit: %q", sn)
	}
}

func TestRunbookIDFromURL(t *testing.T) {
	for in, want := range map[string]string{
		"/runbook?id=rb-1":                     "rb-1",
		"https://apm.example.com/runbook?id=x": "x",
		"/runbooks/rb-2":                       "rb-2",
		"/runbook?runbook=rb-3":                "rb-3",
		"https://wiki.example.com/page/1":      "",
		"/runbooks":                            "",
		"":                                     "",
	} {
		if got := runbookIDFromURL(in); got != want {
			t.Errorf("%q → %q, bekl. %q", in, got, want)
		}
	}
}

func TestRankRunbooksAndView(t *testing.T) {
	books := []chstore.Runbook{
		{ID: "a", Title: "Kafka lag", Enabled: true, Steps: []chstore.RunbookStep{{Order: 1, Kind: "manual", Title: "Bak", Instructions: strings.Repeat("x", 700)}}},
		{ID: "b", Title: "Disk", Enabled: true, Description: "kafka broker diski"},
		{ID: "c", Title: "Kafka lag (kapalı)", Enabled: false},
		{ID: "d", Title: "İlgisiz", Enabled: true},
	}
	rows := rankRunbooks(books, knowledgeTerms("kafka lag"))
	if len(rows) != 2 || rows[0].ID != "a" || rows[1].ID != "b" || rows[0].Href != "/runbook?id=a" {
		t.Fatalf("sıralama: %+v", rows)
	}
	v := runbookView(books[0])
	steps := v["steps"].([]map[string]any)
	if v["stepCount"] != 1 || len([]rune(steps[0]["instructions"].(string))) != runbookStepMax+1 {
		t.Fatalf("görünüm tavanı: %+v", v)
	}
}

func TestKnowledgeToolsSchema(t *testing.T) {
	tools := ToolList(Deps{})
	for _, name := range []string{"get_runbook", "search_knowledge"} {
		tool := toolByName(t, tools, name)
		if tool.ShortDescription == "" || tool.Description == "" {
			t.Errorf("%s: açıklama boş", name)
		}
	}
	sk := toolByName(t, tools, "search_knowledge")
	if _, err := sk.Handler(context.Background(), json.RawMessage(`{"query":"ab"}`)); err == nil || !strings.Contains(err.Error(), "3+") {
		t.Fatalf("kısa sorgu reddedilmeli: %v", err)
	}
	if _, err := sk.Handler(context.Background(), json.RawMessage(`{"query":"kafka","source":"wiki"}`)); err == nil || !strings.Contains(err.Error(), "source") {
		t.Fatalf("bilinmeyen kaynak: %v", err)
	}
	gr := toolByName(t, tools, "get_runbook")
	if _, err := gr.Handler(context.Background(), json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "runbook_id") {
		t.Fatalf("anahtar yokken hata: %v", err)
	}
}
