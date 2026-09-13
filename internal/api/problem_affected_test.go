package api

import (
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.10.707 — etkilenen varlıklar: sıra (çağıran desc, pod desc, cluster),
// tekrarsız, özne servis dışarıda, tavan 25; pencere onset−1h..çözüm|şimdi.
func TestBuildAffectedEntities(t *testing.T) {
	br := &chstore.BlastRadius{Callers: []chstore.BlastRadiusCaller{
		{Service: "shop-cart", Calls: 10}, {Service: "shop-web", Calls: 500, HasOpenProblem: true}, {Service: "shop-payment", Calls: 3},
	}}
	pods := []chstore.PodHit{{Pod: "p-2", Count: 1}, {Pod: "p-1", Count: 9}, {Pod: "", Count: 3}}
	got := buildAffectedEntities("shop-payment", br, pods, []string{"prod-eu", "prod-eu", ""})
	want := []string{"service|shop-web", "service|shop-cart", "pod|p-1", "pod|p-2", "cluster|prod-eu"}
	if len(got) != len(want) {
		t.Fatalf("%d satır, beklenen %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Kind+"|"+got[i].ID != w {
			t.Errorf("%d: %s|%s, beklenen %s", i, got[i].Kind, got[i].ID, w)
		}
	}
	if !got[0].HasOpenProblem || got[2].Count != 9 {
		t.Fatalf("alanlar taşınmalı: %+v", got)
	}
	if e := buildAffectedEntities("x", nil, nil, nil); len(e) != 0 || e == nil {
		t.Fatal("boş girdi → boş (nil değil) liste")
	}
	many := &chstore.BlastRadius{}
	for i := 0; i < 40; i++ {
		many.Callers = append(many.Callers, chstore.BlastRadiusCaller{Service: "s" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Calls: uint64(i)})
	}
	if e := buildAffectedEntities("x", many, nil, nil); len(e) != affectedCallersMax {
		t.Fatalf("tavan %d, geldi %d", affectedCallersMax, len(e))
	}
}

func TestAffectedWindow(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	started := now.Add(-30 * time.Minute)
	from, to := affectedWindow(chstore.Problem{StartedAt: started.UnixNano()}, now)
	if !to.Equal(now) || !from.Equal(started.Add(-time.Hour)) {
		t.Fatalf("açık problem: %v..%v", from, to)
	}
	res := now.Add(-10 * time.Minute).UnixNano()
	_, to = affectedWindow(chstore.Problem{StartedAt: started.UnixNano(), ResolvedAt: &res}, now)
	if !to.Equal(time.Unix(0, res)) {
		t.Fatal("çözülmüş problem çözüm anında biter")
	}
	old := now.Add(-3 * 24 * time.Hour)
	from, to = affectedWindow(chstore.Problem{StartedAt: old.UnixNano()}, now)
	if to.Sub(from) != 24*time.Hour {
		t.Fatal("24 saat tavanı")
	}
	from, to = affectedWindow(chstore.Problem{}, now)
	if to.Sub(from) != time.Hour {
		t.Fatal("onset yoksa son 1 saat")
	}
}
