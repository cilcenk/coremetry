package api

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.10.703 — Exceptions listesi öncelik sırası: P1 önce, eşitlikte en taze,
// sonra fingerprint; asc tersi; sayfa dilimi sınırlı; handler bu yolu kullanır.
func TestSortExceptionGroupsByPriority(t *testing.T) {
	g := func(fp, p string, last int64) chstore.ExceptionGroup {
		return chstore.ExceptionGroup{Fingerprint: fp, Priority: p, LastSeen: last}
	}
	items := []chstore.ExceptionGroup{g("d", "P3", 50), g("b", "P1", 10), g("c", "P2", 40), g("a", "P1", 30), g("z", "", 99), g("a0", "P1", 30)}
	sortExceptionGroupsByPriority(items, "desc")
	got := make([]string, 0, len(items))
	for _, it := range items {
		got = append(got, it.Fingerprint)
	}
	if strings.Join(got, ",") != "a,a0,b,c,d,z" {
		t.Fatalf("desc: P1 önce, eşitlikte taze önce, sonra fp: %v", got)
	}
	sortExceptionGroupsByPriority(items, "asc")
	got = got[:0]
	for _, it := range items {
		got = append(got, it.Fingerprint)
	}
	if strings.Join(got, ",") != "z,d,c,a,a0,b" {
		t.Fatalf("asc: tersi: %v", got)
	}
	// Determinizm: aynı girdi iki kez aynı çıktı.
	again := []chstore.ExceptionGroup{g("d", "P3", 50), g("b", "P1", 10), g("c", "P2", 40), g("a", "P1", 30), g("z", "", 99), g("a0", "P1", 30)}
	sortExceptionGroupsByPriority(again, "desc")
	if again[0].Fingerprint != "a" || again[5].Fingerprint != "z" {
		t.Fatalf("determinizm bozuk: %+v", again)
	}
}

func TestPageExceptionGroups(t *testing.T) {
	items := make([]chstore.ExceptionGroup, 7)
	for i := range items {
		items[i].Fingerprint = string(rune('a' + i))
	}
	if p := pageExceptionGroups(items, 0, 3); len(p) != 3 || p[0].Fingerprint != "a" {
		t.Fatalf("ilk sayfa: %+v", p)
	}
	if p := pageExceptionGroups(items, 6, 3); len(p) != 1 || p[0].Fingerprint != "g" {
		t.Fatalf("son kısmi sayfa: %+v", p)
	}
	if p := pageExceptionGroups(items, 7, 3); len(p) != 0 {
		t.Fatalf("sınır dışı boş: %+v", p)
	}
	if p := pageExceptionGroups(items, -5, 0); len(p) != 0 {
		t.Fatalf("limit 0 → boş: %+v", p)
	}
}

// Handler sort=priority'yi tavanlı küme + Go sıralaması + dilimle karşılamalı;
// öncelik hesabı dilimden ÖNCE (yoksa sıralama boş anahtarla yapılır).
func TestListExceptionGroupsPrioritySortWiring(t *testing.T) {
	src := readRepoFile(t, "api.go")
	i := strings.Index(src, "func (s *Server) listExceptionGroups(")
	if i < 0 {
		t.Fatal("listExceptionGroups bulunamadı")
	}
	body := src[i:]
	if j := strings.Index(body, "\nfunc "); j > 0 {
		body = body[:j]
	}
	for _, want := range []string{
		"s.listExceptionGroupsPage(ctx, f)",
		"pg.apply(items, total)",
		`"capped": pg.Capped`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("listExceptionGroups %q içermeli", want)
		}
	}
	prio := strings.Index(body, "exceptionPriority(items[i])")
	sortAt := strings.Index(body, "pg.apply(items, total)")
	if prio < 0 || sortAt < 0 || prio > sortAt {
		t.Error("öncelik satır başına hesaplandıktan SONRA sıralanmalı")
	}
	helper := readRepoFile(t, "exception_priority_sort.go")
	for _, want := range []string{"f.Sort == exceptionPrioritySortKey", "f.Limit, f.Offset = exceptionPrioritySortCap, 0", "sortExceptionGroupsByPriority(items, pg.dir)", "pageExceptionGroups(items, pg.Offset, pg.Limit)"} {
		if !strings.Contains(helper, want) {
			t.Errorf("exception_priority_sort.go %q içermeli", want)
		}
	}
}

// apply sözleşmesi: prio değilse dokunmaz; prio ise sıralar, diler, Capped'i toplamdan türetir.
func TestExceptionPageApply(t *testing.T) {
	items := []chstore.ExceptionGroup{{Fingerprint: "b", Priority: "P3"}, {Fingerprint: "a", Priority: "P1"}}
	plain := exceptionPage{Limit: 1, Offset: 0}
	if got := plain.apply(items, 5000); len(got) != 2 || got[0].Fingerprint != "b" || plain.Capped {
		t.Fatalf("prio değilken no-op olmalı: %+v capped=%v", got, plain.Capped)
	}
	pg := exceptionPage{Limit: 1, Offset: 0, prio: true, dir: "desc"}
	if got := pg.apply(items, 5000); len(got) != 1 || got[0].Fingerprint != "a" || !pg.Capped {
		t.Fatalf("prio: P1 ilk sayfa, capped: %+v capped=%v", got, pg.Capped)
	}
	pg2 := exceptionPage{Limit: 50, Offset: 0, prio: true, dir: "desc"}
	pg2.apply(items, 2)
	if pg2.Capped {
		t.Fatal("toplam tavan altındayken capped=false")
	}
}
