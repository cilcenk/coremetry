package entity

import (
	"testing"
	"time"
)

// v0.10.129 — zaman geçerliliği: ömür farkı (design §1.3; görev "pod adı
// yeniden kullanımı, UID değişimi", "kısmi senkronizasyon").
//
// Sözleşme (DiffLifetimes):
//   - görülen & açık ömür yok           → AÇ (valid_from = now)
//   - görülen & açık ömür var, uid farklı (ikisi de dolu) → KAPAT eskiyi
//     (valid_to = eski last_seen) + AÇ yeni
//   - görülen & aynı uid (ya da uid yok), now - last_seen > podGap → KAPAT + AÇ
//     (ad yeniden kullanıldı: StatefulSet sabit adı)
//   - görülen & taze → TAZELE (last_seen = now)
//   - görülmeyen açık ömür: cluster'a başarıyla ULAŞILDIYSA KAPAT
//     (valid_to = last_seen); ulaşılamadıysa DOKUNMA (bilinmiyor) —
//     hiçbir şey SİLİNMEZ.

func TestDiffLifetimes(t *testing.T) {
	t0 := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	gap := 10 * time.Minute
	open := func(id, uid string, from, last time.Time) Lifetime {
		return Lifetime{ID: id, UID: uid, ValidFrom: from, LastSeen: last}
	}
	prev := map[string]Lifetime{
		"pod:c/ns/a": open("pod:c/ns/a", "u-a", t0.Add(-time.Hour), t0.Add(-time.Minute)),   // taze, aynı uid → tazele
		"pod:c/ns/b": open("pod:c/ns/b", "u-b1", t0.Add(-time.Hour), t0.Add(-time.Minute)),  // uid değişti → kapat+aç
		"pod:c/ns/c": open("pod:c/ns/c", "", t0.Add(-2*time.Hour), t0.Add(-30*time.Minute)), // uid yok, gap aşıldı → kapat+aç
		"pod:c/ns/d": open("pod:c/ns/d", "u-d", t0.Add(-time.Hour), t0.Add(-time.Minute)),   // artık görülmüyor
		"pod:c/ns/e": open("pod:c/ns/e", "", t0.Add(-time.Hour), t0.Add(-5*time.Minute)),    // uid yok, gap içinde → tazele
	}
	seen := map[string]Entity{
		"pod:c/ns/a": {ID: "pod:c/ns/a", UID: "u-a"},
		"pod:c/ns/b": {ID: "pod:c/ns/b", UID: "u-b2"},
		"pod:c/ns/c": {ID: "pod:c/ns/c"},
		"pod:c/ns/e": {ID: "pod:c/ns/e"},
		"pod:c/ns/n": {ID: "pod:c/ns/n", UID: "u-n"}, // yeni
	}
	ch := DiffLifetimes(t0, prev, seen, gap, true)
	ids := func(ls []Lifetime) map[string]Lifetime {
		m := map[string]Lifetime{}
		for _, l := range ls {
			m[l.ID] = l
		}
		return m
	}
	opened, closed, refreshed := ids(ch.Open), ids(ch.Close), ids(ch.Refresh)
	// a, e → tazele
	for _, id := range []string{"pod:c/ns/a", "pod:c/ns/e"} {
		l, ok := refreshed[id]
		if !ok || !l.LastSeen.Equal(t0) || !l.ValidFrom.Equal(prev[id].ValidFrom) {
			t.Errorf("%s tazelenmeli (last_seen=now, valid_from korunur): %+v", id, l)
		}
	}
	// b, c → kapat + aç
	for _, id := range []string{"pod:c/ns/b", "pod:c/ns/c"} {
		c, ok := closed[id]
		if !ok || !c.ValidTo.Equal(prev[id].LastSeen) || !c.ValidFrom.Equal(prev[id].ValidFrom) {
			t.Errorf("%s eski ömür last_seen'de kapanmalı: %+v", id, c)
		}
		o, ok := opened[id]
		if !ok || !o.ValidFrom.Equal(t0) || !o.LastSeen.Equal(t0) {
			t.Errorf("%s yeni ömür now'da açılmalı: %+v", id, o)
		}
	}
	if opened["pod:c/ns/b"].UID != "u-b2" {
		t.Errorf("yeni ömür yeni uid taşımalı: %+v", opened["pod:c/ns/b"])
	}
	// d → görülmüyor, cluster ulaşıldı → kapat
	if c, ok := closed["pod:c/ns/d"]; !ok || !c.ValidTo.Equal(prev["pod:c/ns/d"].LastSeen) {
		t.Errorf("görülmeyen ömür kapanmalı: %+v", c)
	}
	// n → yeni
	if o, ok := opened["pod:c/ns/n"]; !ok || !o.ValidFrom.Equal(t0) || o.UID != "u-n" {
		t.Errorf("yeni varlık açılmalı: %+v", o)
	}
	if len(ch.Open) != 3 || len(ch.Close) != 3 || len(ch.Refresh) != 2 {
		t.Fatalf("sayılar: open=%d close=%d refresh=%d", len(ch.Open), len(ch.Close), len(ch.Refresh))
	}
	// Ulaşılamayan cluster: görülmeyenler KAPANMAZ, hiçbir şey silinmez.
	ch2 := DiffLifetimes(t0, prev, map[string]Entity{}, gap, false)
	if len(ch2.Close) != 0 || len(ch2.Open) != 0 || len(ch2.Refresh) != 0 {
		t.Fatalf("ulaşılamayan cluster'da değişiklik olmamalı: %+v", ch2)
	}
	// Deterministik sıra (id'ye göre).
	for i := 1; i < len(ch.Open); i++ {
		if ch.Open[i-1].ID > ch.Open[i].ID {
			t.Fatal("Open id sırasında olmalı")
		}
	}
}

// Aynı namespace + pod adı iki cluster'da: iki ayrı ömür, birbirine karışmaz.
func TestDiffLifetimesTwoClustersSameName(t *testing.T) {
	t0 := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	seen := map[string]Entity{
		PodID("c-1", "ns", "api-0"): {ID: PodID("c-1", "ns", "api-0"), UID: "u1"},
		PodID("c-2", "ns", "api-0"): {ID: PodID("c-2", "ns", "api-0"), UID: "u2"},
	}
	ch := DiffLifetimes(t0, nil, seen, 10*time.Minute, true)
	if len(ch.Open) != 2 || ch.Open[0].ID == ch.Open[1].ID {
		t.Fatalf("iki cluster iki ömür: %+v", ch.Open)
	}
}
