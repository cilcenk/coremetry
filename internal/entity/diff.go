package entity

import (
	"sort"
	"time"
)

// diff.go — ZAMAN GEÇERLİLİĞİ: ömür farkı (saf; design §1.3).
//
// Her varlık/ilişki satırı bir ÖMÜR: (id, valid_from) dedup anahtarı,
// valid_to (sıfır = açık), last_seen. Pod adları geri dönüştürülür; geçmiş
// bir trace bugünkü pod'a bağlanmamalı — bu yüzden ad aynı olsa da uid
// değişince ya da podGap'ten uzun görülmeyince YENİ ömür açılır.
// Ulaşılamayan cluster'da hiçbir şey kapanmaz: yokluk bilinmiyor, silme
// zaten hiç yok (görev kısıtı: "son görülme" ile eskitme).

// Lifetime — açık ya da kapanmış bir ömür.
type Lifetime struct {
	ID        string
	UID       string
	ValidFrom time.Time
	ValidTo   time.Time // sıfır = açık
	LastSeen  time.Time
}

// Change — bir tick'in yazım kararı.
type Change struct {
	Open    []Lifetime // yeni ömürler (valid_from = now)
	Close   []Lifetime // kapanan ömürler (valid_to = eski last_seen)
	Refresh []Lifetime // last_seen tazelenen açık ömürler
}

// DiffLifetimes — prev: id → AÇIK ömür; seen: id → bu tick görülen varlık;
// reachable: cluster'a bu tick başarıyla ulaşıldı mı.
func DiffLifetimes(now time.Time, prev map[string]Lifetime, seen map[string]Entity, podGap time.Duration, reachable bool) Change {
	var ch Change
	if !reachable {
		return ch
	}
	for id, e := range seen {
		p, open := prev[id]
		switch {
		case !open:
			ch.Open = append(ch.Open, Lifetime{ID: id, UID: e.UID, ValidFrom: now, LastSeen: now})
		case uidChanged(p.UID, e.UID) || now.Sub(p.LastSeen) > podGap:
			closed := p
			closed.ValidTo = p.LastSeen
			ch.Close = append(ch.Close, closed)
			ch.Open = append(ch.Open, Lifetime{ID: id, UID: e.UID, ValidFrom: now, LastSeen: now})
		default:
			r := p
			r.LastSeen = now
			if r.UID == "" {
				r.UID = e.UID
			}
			ch.Refresh = append(ch.Refresh, r)
		}
	}
	for id, p := range prev {
		if _, ok := seen[id]; ok {
			continue
		}
		closed := p
		closed.ValidTo = p.LastSeen
		ch.Close = append(ch.Close, closed)
	}
	byID := func(ls []Lifetime) { sort.Slice(ls, func(i, j int) bool { return ls[i].ID < ls[j].ID }) }
	byID(ch.Open)
	byID(ch.Close)
	byID(ch.Refresh)
	return ch
}

func uidChanged(prev, cur string) bool {
	return prev != "" && cur != "" && prev != cur
}
