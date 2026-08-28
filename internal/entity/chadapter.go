package entity

import (
	"context"
	"time"
)

// chadapter.go — chstore'un Entity* metotlarını Store arayüzüne bağlar.
// Yapısal tipleme: entity paketi chstore'u import ETMEZ (chstore entity'yi
// import ediyor — döngü olmasın); main.go somut *chstore.Store'u geçirir.

type CHStore interface {
	EntityOpenLifetimes(ctx context.Context, cid string) (map[string]Lifetime, error)
	EntityApply(ctx context.Context, cid string, rows []EntityRow, rels []RelationRow) error
	EntityRecordRun(ctx context.Context, run Run) error
	EntityIDsExisting(ctx context.Context, cid string, ids []string) (map[string]bool, error)
}

type chAdapter struct{ ch CHStore }

func StoreFromCH(ch CHStore) Store { return chAdapter{ch: ch} }

func (a chAdapter) OpenLifetimes(ctx context.Context, cid string) (map[string]Lifetime, error) {
	return a.ch.EntityOpenLifetimes(ctx, cid)
}
func (a chAdapter) Apply(ctx context.Context, cid string, rows []EntityRow, rels []RelationRow) error {
	return a.ch.EntityApply(ctx, cid, rows, rels)
}
func (a chAdapter) RecordRun(ctx context.Context, run Run) error {
	return a.ch.EntityRecordRun(ctx, run)
}
func (a chAdapter) Existing(ctx context.Context, cid string, ids []string) (map[string]bool, error) {
	return a.ch.EntityIDsExisting(ctx, cid, ids)
}

// CHSeen — chstore.EntitySeenRecent (yapısal tipleme).
type CHSeen interface {
	EntitySeenRecent(ctx context.Context, since time.Time) ([]SeenRow, error)
	EntitySeenRecentFor(ctx context.Context, since time.Time, clusterValue string) ([]SeenRow, error)
}

type chSeen struct{ ch CHSeen }

func SeenFromCH(ch CHSeen) SeenReader { return chSeen{ch: ch} }

func (a chSeen) RecentSeen(ctx context.Context, since time.Time) ([]SeenRow, error) {
	return a.ch.EntitySeenRecent(ctx, since)
}
func (a chSeen) RecentSeenFor(ctx context.Context, since time.Time, clusterValue string) ([]SeenRow, error) {
	return a.ch.EntitySeenRecentFor(ctx, since, clusterValue)
}
