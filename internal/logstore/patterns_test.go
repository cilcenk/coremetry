package logstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// patterns_test.go — v0.10.296: scripted Store ile örnekleme + gruplama.

type pagedStore struct {
	Store
	rows    []*LogRecord
	calls   int
	lastLim int
	err     error
}

func (p *pagedStore) Search(_ context.Context, f Filter) (*Page, error) {
	p.calls++
	p.lastLim = f.Limit
	if p.err != nil {
		return nil, p.err
	}
	if !f.WantCursor {
		return nil, errors.New("WantCursor beklenmiyordu")
	}
	start := 0
	if f.Cursor != "" {
		fmt.Sscanf(f.Cursor, "c%d", &start)
	}
	end := start + f.Limit
	if end > len(p.rows) {
		end = len(p.rows)
	}
	pg := &Page{Total: len(p.rows), Logs: p.rows[start:end]}
	if end < len(p.rows) && end-start == f.Limit {
		pg.NextCursor = fmt.Sprintf("c%d", end)
	}
	return pg, nil
}

func rec(ts int64, svc, body string, sev uint8) *LogRecord {
	return &LogRecord{Timestamp: ts, ServiceName: svc, Body: body, Severity: sev}
}

func TestGroupBySignatureGroupsAndSorts(t *testing.T) {
	st := &pagedStore{rows: []*LogRecord{
		rec(30, "api", "connection refused to 10.0.0.1:5432 after 1500ms", 17),
		rec(20, "api", "connection refused to 10.0.0.2:5432 after 900ms", 13),
		rec(25, "worker", "connection refused to 10.0.0.3:5432 after 12ms", 17),
		rec(10, "api", "user 7b8c9d0e-1111-2222-3333-444455556666 logged in", 9),
		rec(5, "api", "", 9),
	}}
	res, err := GroupBySignature(context.Background(), st, Filter{}, 50)
	if err != nil {
		t.Fatal(err)
	}
	// v0.10.441 (C4) — kapsanan uç okunan satırları izler (boş gövdeli ts=5
	// satırı da dahil), gruplanan satırları değil.
	if res.CoveredFromNs != 5 || res.CoveredToNs != 30 {
		t.Errorf("kapsanan pencere %d..%d; 5..30 bekleniyordu", res.CoveredFromNs, res.CoveredToNs)
	}
	if res.Sampled != 5 || res.Total != 5 || res.Truncated || res.Distinct != 2 {
		t.Errorf("sampled=%d total=%d trunc=%v distinct=%d", res.Sampled, res.Total, res.Truncated, res.Distinct)
	}
	if len(res.Groups) != 2 {
		t.Fatalf("groups %d", len(res.Groups))
	}
	g := res.Groups[0]
	if g.Count != 3 || g.Template != "connection refused to <x> after <x>ms" {
		t.Errorf("ilk grup: count=%d tmpl=%q", g.Count, g.Template)
	}
	if g.Sample != "connection refused to 10.0.0.1:5432 after 1500ms" {
		t.Errorf("örnek ilk görülen mesaj VERBATİM olmalı: %q", g.Sample)
	}
	if g.FirstSeen != 20 || g.LastSeen != 30 || g.Severity != 17 {
		t.Errorf("first=%d last=%d sev=%d", g.FirstSeen, g.LastSeen, g.Severity)
	}
	if strings.Join(g.Services, ",") != "api,worker" || g.ServiceCount != 2 {
		t.Errorf("services %v (%d)", g.Services, g.ServiceCount)
	}
	if len(g.Hash) != 16 {
		t.Errorf("hash %q", g.Hash)
	}
	if g.Query != `"connection refused to" AND "after"` {
		t.Errorf("query %q", g.Query)
	}
	if res.Groups[1].Template != "user <x> logged in" {
		t.Errorf("ikinci grup %q", res.Groups[1].Template)
	}
}

func TestGroupBySignatureSamplesUpToCapWithCursor(t *testing.T) {
	rows := make([]*LogRecord, 0, PatternsSampleCap+700)
	for i := 0; i < PatternsSampleCap+700; i++ {
		rows = append(rows, rec(int64(i), "s", fmt.Sprintf("tick %d", 1000+i), 9)) // ≥2 hane: sigNum \d{2,}
	}
	st := &pagedStore{rows: rows}
	res, err := GroupBySignature(context.Background(), st, Filter{Limit: 7, Offset: 99, Cursor: "junk"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sampled != PatternsSampleCap {
		t.Errorf("sampled %d; tavan %d", res.Sampled, PatternsSampleCap)
	}
	if !res.Truncated || res.Total != len(rows) {
		t.Errorf("truncated=%v total=%d", res.Truncated, res.Total)
	}
	if st.calls != PatternsSampleCap/patternsPageSize {
		t.Errorf("sayfa sayısı %d; %d bekleniyordu", st.calls, PatternsSampleCap/patternsPageSize)
	}
	if len(res.Groups) != 1 || res.Groups[0].Count != int64(PatternsSampleCap) {
		t.Errorf("tek şablon 'tick <x>' beklenirdi: %+v", res.Groups)
	}
	if res.Distinct != 1 {
		t.Errorf("distinct %d", res.Distinct)
	}
	// v0.10.441 (C4) — tavan dolunca kapsanan uç YALNIZ okunan 2000 satır
	// (ts 0..1999), 2700 satırlık tüm küme değil (min/max; sahte mağaza
	// artan sıralı döndürür, gerçek backend en yeni-önce — sıraya bağlı
	// iddia yok). ts=0 satırı 1970 çekmesin diye ts>0 kapısı → 1.
	if res.CoveredFromNs != 1 || res.CoveredToNs != int64(PatternsSampleCap-1) {
		t.Errorf("kapsanan pencere %d..%d; 1..%d bekleniyordu", res.CoveredFromNs, res.CoveredToNs, PatternsSampleCap-1)
	}
}

func TestGroupBySignatureLimitAndErrors(t *testing.T) {
	var rows []*LogRecord
	for i := 0; i < 30; i++ {
		rows = append(rows, rec(int64(i), "s", fmt.Sprintf("shape-%c happened", 'a'+i%5), 9))
	}
	res, err := GroupBySignature(context.Background(), &pagedStore{rows: rows}, Filter{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Groups) != 2 || res.Distinct != 5 {
		t.Errorf("limit=2 → 2 grup, distinct 5; got %d/%d", len(res.Groups), res.Distinct)
	}
	if _, err := GroupBySignature(context.Background(), &pagedStore{err: errors.New("boom")}, Filter{}, 5); err == nil {
		t.Error("backend hatası yüzeye çıkmalı")
	}
	if _, err := GroupBySignature(context.Background(), nil, Filter{}, 5); err == nil {
		t.Error("nil store hata")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := GroupBySignature(ctx, &pagedStore{rows: rows}, Filter{}, 5); !errors.Is(err, context.Canceled) {
		t.Errorf("iptal edilen ctx: %v", err)
	}
}

func TestPatternSearchQuery(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"connection refused to <x>:<x> after <x>ms", `"connection refused to" AND "after"`},
		{"user <x> logged in", `"user" AND "logged in"`},
		{"<x>", ""},
		{"", ""},
		{`say "hi" <x>`, `"say \"hi"`},
		{"a b c d e f g h i j k l m n o p q r s t u v w x y z <x> A B C <x> D E F <x> G H I <x> J K L <x> M N O <x> P Q R <x> S T U", `"a b c d e f g h i j k l m n o p q r s t u v w x y z" AND "A B C" AND "D E F" AND "G H I" AND "J K L" AND "M N O"`},
	} {
		if got := PatternSearchQuery(tc.in); got != tc.want {
			t.Errorf("PatternSearchQuery(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
	// Üretilen dize iki arka ucun da anladığı yazım: logql ile ayrışmalı.
	for _, q := range []string{PatternSearchQuery("connection refused to <x>:<x> after <x>ms"), PatternSearchQuery(`say "hi" <x>`)} {
		if LooksLikeFieldQuery(q) {
			t.Errorf("desen sorgusu alan yazımı üretmemeli: %q", q)
		}
	}
}
