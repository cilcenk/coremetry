package chstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

// NotificationLog is one dispatched notification — the append-only
// audit trail of every channel send (success AND failure) fanned out
// by internal/notify. v0.8.241.
//
// The row is IMMUTABLE once written (a send happened at a point in
// time), so the engine is a plain MergeTree — NOT ReplacingMergeTree.
// Reads never use FINAL. Retained 90 days via a partition-aligned day
// TTL (see the CREATE TABLE in store.go migrate()).
type NotificationLog struct {
	ID          string `json:"id"`
	SentAt      int64  `json:"sentAt"`      // unix ns
	ChannelKind string `json:"channelKind"` // email|slack|mattermost|teams|zoomchat|webhook|whatsapp
	ChannelName string `json:"channelName"`
	Target      string `json:"target"` // full-fidelity recipient (operator policy); webhook URLs host-only (URL embeds a live credential)
	Subject     string `json:"subject"`
	BodyPreview string `json:"bodyPreview"` // first ~200 chars of the notification body
	RelatedKind string `json:"relatedKind"` // problem|test|runbook|incident|alert|monitor|…
	RelatedID   string `json:"relatedId"`
	OK          bool   `json:"ok"`
	Error       string `json:"error"`
}

// ── "Kimseye gitmedi" işareti (v0.9.1344) ───────────────────────────
//
// Bir problem hiçbir bildirim kanalıyla eşleşmediğinde internal/notify
// notification_log'a SENTETİK bir satır yazar: yeni tablo YOK (state
// için yeni şema açmama kuralı), zaten problem başına anahtarlı
// (related_id), zaten 90 gün TTL'li ve zaten /events'te görünen tek
// defter burası. "Sayfa gitti mi" sorusunun evi zaten bu tablo; üçüncü
// bir cevap (hiç denenmedi) eklemek yeni bir ev gerektirmiyor.
//
// Satırın kimliği (kind, name) = (none, unmatched) ve ok = 0.
//
// ⚠ BU SATIR ÜÇ OKUYUCUYU YANILTABİLİR — üçü de burada kapatıldı:
//
//  1. ChannelHealth (kind, name) ile grupluyor → Settings → Kanallar
//     listesinde ardışık hatası artan HAYALET bir kanal belirirdi.
//     buildChannelHealthQuery'de SQL düzeyinde eleniyor (aşağıda).
//  2. HasNotification `ok = 1` şartı taşıyor → ok=0 olan bu satır
//     hiçbir dedup kapısını tetikleyemez. Doğrulandı, ek önlem yok.
//  3. /events satırı `ok ? sent : failed` çiziyordu → işaret "failed"
//     görünürdü. GİTMEDİ ile BAŞARISIZ farklı şeyler: frontend üçüncü
//     bir rozet çiziyor (Events.tsx).
const (
	// NotifyUnmatchedChannelKind — sentetik işaret satırının
	// channel_kind'i. LowCardinality(String) sütununa tek bir yeni
	// değer ekler.
	NotifyUnmatchedChannelKind = "none"
	// NotifyUnmatchedChannelName — sentetik işaret satırının
	// channel_name'i. Gerçek bir kanal adıyla çakışması hâlinde
	// ChannelHealth elemesi zaten kind üzerinden yapıldığı için
	// güvenlidir.
	NotifyUnmatchedChannelName = "unmatched"
)

// InsertNotificationLog appends one send record. Fire-and-forget from
// the notify funnel — the caller logs-and-continues on error so a
// record failure never blocks (or re-fires) the notification itself.
// Uses the async_insert context like every other write path.
func (s *Store) InsertNotificationLog(ctx context.Context, e NotificationLog) error {
	if e.ID == "" {
		e.ID = newNotificationLogID()
	}
	if e.SentAt == 0 {
		e.SentAt = time.Now().UnixNano()
	}
	ctx = asyncInsertCtx(ctx)
	batch, err := s.conn.PrepareBatch(ctx, "INSERT INTO notification_log")
	if err != nil {
		return err
	}
	ok := uint8(0)
	if e.OK {
		ok = 1
	}
	// Value order MUST match the notification_log CREATE TABLE column
	// order in store.go (id … error).
	if err := batch.Append(
		e.ID,
		time.Unix(0, e.SentAt),
		e.ChannelKind,
		e.ChannelName,
		e.Target,
		e.Subject,
		e.BodyPreview,
		e.RelatedKind,
		e.RelatedID,
		ok,
		e.Error,
	); err != nil {
		return err
	}
	return batch.Send()
}

// ListNotificationLog reads the dispatch history newest-first. Always
// time-bounded (a zero from/to defaults to the full 90-day retention
// window) so the read carries a prefix predicate on the sent_at ORDER
// BY key — never a full-table scan. kind filters on channel_kind
// exactly; "" = all channels.
func (s *Store) ListNotificationLog(ctx context.Context, from, to time.Time, kind string, limit, offset int) ([]NotificationLog, error) {
	if to.IsZero() {
		to = time.Now()
	}
	if from.IsZero() {
		// Default to the retention window rather than unbounded so the
		// query keeps its time-prefix predicate.
		from = to.Add(-90 * 24 * time.Hour)
	}
	q, args := buildNotificationLogQuery(from, to, kind, limit, offset)
	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NotificationLog
	for rows.Next() {
		var e NotificationLog
		var ok uint8
		if err := rows.Scan(
			&e.ID, &e.SentAt, &e.ChannelKind, &e.ChannelName,
			&e.Target, &e.Subject, &e.BodyPreview,
			&e.RelatedKind, &e.RelatedID, &ok, &e.Error,
		); err != nil {
			return nil, err
		}
		e.OK = ok == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// buildNotificationLogQuery is the pure SQL builder for
// ListNotificationLog — extracted so the mandatory read bounds
// (time-bounded WHERE on the sent_at ORDER BY prefix + LIMIT +
// max_execution_time) are table-testable without a live CH. from/to
// are assumed already normalised (non-zero) by the caller.
func buildNotificationLogQuery(from, to time.Time, kind string, limit, offset int) (string, []any) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	conds := []string{"sent_at >= ?", "sent_at < ?"}
	args := []any{from, to}
	if kind != "" {
		conds = append(conds, "channel_kind = ?")
		args = append(args, kind)
	}
	q := `
		SELECT id,
		       toUnixTimestamp64Nano(sent_at),
		       channel_kind, channel_name, target,
		       subject, body_preview,
		       related_kind, related_id, ok, error
		FROM notification_log
		WHERE ` + joinAnd(conds) + `
		ORDER BY sent_at DESC
		LIMIT ? OFFSET ?
		SETTINGS max_execution_time = 10`
	args = append(args, limit, offset)
	return q, args
}

// ListNotificationLogByRelated reads the sends recorded for a SET of
// related ids (v0.9.196 — the /watchers history drawer joins one
// rule's problem ids to their notification rows). Newest-first,
// bounded to the table's 90-day retention window so the read keeps
// its sent_at ORDER BY prefix predicate. An empty id set returns nil
// without touching CH.
func (s *Store) ListNotificationLogByRelated(ctx context.Context, relatedIDs []string, limit int) ([]NotificationLog, error) {
	if len(relatedIDs) == 0 {
		return nil, nil
	}
	q, args := buildNotificationLogRelatedQuery(relatedIDs, time.Now().Add(-90*24*time.Hour), limit)
	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NotificationLog
	for rows.Next() {
		var e NotificationLog
		var ok uint8
		if err := rows.Scan(
			&e.ID, &e.SentAt, &e.ChannelKind, &e.ChannelName,
			&e.Target, &e.Subject, &e.BodyPreview,
			&e.RelatedKind, &e.RelatedID, &ok, &e.Error,
		); err != nil {
			return nil, err
		}
		e.OK = ok == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// buildNotificationLogRelatedQuery is the pure SQL builder for
// ListNotificationLogByRelated — extracted (like
// buildNotificationLogQuery above) so the mandatory read bounds
// (time-bounded WHERE + LIMIT + max_execution_time) and the literal
// IN placeholder expansion are table-testable without a live CH.
// The IN list is bound placeholders, not a subquery, so no GLOBAL IN
// concern on distributed installs.
func buildNotificationLogRelatedQuery(relatedIDs []string, since time.Time, limit int) (string, []any) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	holders := make([]string, len(relatedIDs))
	args := make([]any, 0, len(relatedIDs)+2)
	args = append(args, since)
	for i, id := range relatedIDs {
		holders[i] = "?"
		args = append(args, id)
	}
	q := `
		SELECT id,
		       toUnixTimestamp64Nano(sent_at),
		       channel_kind, channel_name, target,
		       subject, body_preview,
		       related_kind, related_id, ok, error
		FROM notification_log
		WHERE sent_at >= ?
		  AND related_id IN (` + strings.Join(holders, ",") + `)
		ORDER BY sent_at DESC
		LIMIT ?
		SETTINGS max_execution_time = 10`
	args = append(args, limit)
	return q, args
}

// ── Channel health (v0.9.1278) ──────────────────────────────────────────────

// channelHealthWindow / channelHealthCap bound the health cover-fetch.
// 30 days is long enough that a channel which last fired weeks ago still
// reports a verdict instead of "hiç gönderim yok"; the 5000-row cap keeps
// the read O(one page) on a busy install. Both are honest in the output:
// a capped fetch stamps Capped on every row so the UI can say so.
const (
	channelHealthWindow = 30 * 24 * time.Hour
	channelHealthCap    = 5000
)

// ChannelHealthRow is the per-channel verdict derived from the
// notification_log dispatch history (v0.9.1278). Settings → Channels
// showed configuration only — a dead SMTP relay or a rotated webhook
// token was invisible until someone opened /events. In a bank the
// missed page is the most expensive failure class, so the channel list
// itself carries its last-send outcome.
//
// Identity is (ChannelKind, ChannelName): notification_log does NOT
// carry the channel id, so a RENAMED channel starts its history over.
// Accepted deliberately — adding a channel_id column would need a
// distributed-safe ALTER + a backfill for zero read-path benefit, and
// the rename case self-heals on the next send.
type ChannelHealthRow struct {
	ChannelKind string `json:"channelKind"`
	ChannelName string `json:"channelName"`
	LastAt      int64  `json:"lastAt"` // unix ns of the most recent send
	LastOK      bool   `json:"lastOk"`
	LastError   string `json:"lastError,omitempty"`
	// ConsecFails counts failures back from the newest send until the
	// most recent SUCCESS. 0 while healthy; when no success exists in
	// the window it is every failure the window holds.
	ConsecFails int `json:"consecFails"`
	// Capped is true on EVERY row when the cover-fetch hit its row cap
	// — the counts are then a lower bound, not the whole 30-day truth.
	Capped bool `json:"capped"`
}

// channelSend is one slim notification_log row as read by the health
// cover-fetch: only the columns the rollup needs, so the 5000-row page
// stays cheap to decode.
type channelSend struct {
	Kind   string
	Name   string
	SentAt int64 // unix ns
	OK     bool
	Error  string
}

// ChannelHealth reads the recent dispatch history and folds it into one
// verdict per (kind, name). Deliberately a bounded cover-fetch + a PURE
// fold rather than an argMax/SQL aggregate: the "consecutive failures
// since the last success" walk is a sequential predicate that SQL would
// express as a window function over the same rows anyway, and keeping
// the arithmetic in Go makes it table-testable without a live CH.
func (s *Store) ChannelHealth(ctx context.Context) ([]ChannelHealthRow, error) {
	q, args := buildChannelHealthQuery(time.Now().Add(-channelHealthWindow), channelHealthCap)
	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sends []channelSend
	for rows.Next() {
		var c channelSend
		var ok uint8
		if err := rows.Scan(&c.Kind, &c.Name, &c.SentAt, &ok, &c.Error); err != nil {
			return nil, err
		}
		c.OK = ok == 1
		sends = append(sends, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return channelHealthFromRows(sends, channelHealthCap), nil
}

// buildChannelHealthQuery is the pure SQL builder for ChannelHealth —
// extracted like the two builders above so the mandatory read bounds
// (time-bounded WHERE on the sent_at ORDER BY prefix + LIMIT +
// max_execution_time) are table-testable without a live CH.
//
// v0.9.1344 — channel_kind != 'none' ŞART. O kind sentetik
// "kimseye gitmedi" işaretidir (bkz. NotifyUnmatchedChannelKind), bir
// kanal DEĞİL. Elenmezse Settings → Kanallar listesinde ardışık hatası
// sürekli artan hayalet bir "none/unmatched" kanalı belirir; eleme SQL
// düzeyinde, çünkü ChannelHealth'in TEK okuma noktası burası ve
// Go tarafındaki fold'a bırakmak yeni bir çağıranın kaçırabileceği
// bir sözleşme olurdu.
func buildChannelHealthQuery(since time.Time, limit int) (string, []any) {
	if limit <= 0 || limit > channelHealthCap {
		limit = channelHealthCap
	}
	q := `
		SELECT channel_kind, channel_name,
		       toUnixTimestamp64Nano(sent_at),
		       ok, error
		FROM notification_log
		WHERE sent_at >= ?
		  AND channel_kind != ?
		ORDER BY sent_at DESC
		LIMIT ?
		SETTINGS max_execution_time = 10`
	return q, []any{since, NotifyUnmatchedChannelKind, limit}
}

// channelHealthFromRows folds raw sends into one row per (kind, name).
//
// Order-independent by construction: the caller's SQL already returns
// newest-first, but the fold re-sorts each channel's slice descending
// rather than trusting that ORDER BY to survive a future edit. limit is
// the cover-fetch cap — hitting it stamps Capped on every row.
//
// Output is sorted by (kind, name) so the result is deterministic for
// the cache and for tests.
func channelHealthFromRows(sends []channelSend, limit int) []ChannelHealthRow {
	if len(sends) == 0 {
		return nil
	}
	capped := limit > 0 && len(sends) >= limit

	type key struct{ kind, name string }
	groups := map[key][]channelSend{}
	order := make([]key, 0, 8)
	for _, s := range sends {
		k := key{s.Kind, s.Name}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], s)
	}

	out := make([]ChannelHealthRow, 0, len(order))
	for _, k := range order {
		g := groups[k]
		sort.SliceStable(g, func(i, j int) bool { return g[i].SentAt > g[j].SentAt })
		last := g[0]
		// Walk newest→oldest and STOP at the first success: that is what
		// makes the count "consecutive since the last OK" rather than
		// "failures in the window". Dropping this break is the mutation
		// the regression test pins.
		consec := 0
		for _, s := range g {
			if s.OK {
				break
			}
			consec++
		}
		out = append(out, ChannelHealthRow{
			ChannelKind: k.kind,
			ChannelName: k.name,
			LastAt:      last.SentAt,
			LastOK:      last.OK,
			LastError:   last.Error,
			ConsecFails: consec,
			Capped:      capped,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ChannelKind != out[j].ChannelKind {
			return out[i].ChannelKind < out[j].ChannelKind
		}
		return out[i].ChannelName < out[j].ChannelName
	})
	return out
}

func newNotificationLogID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "nl-" + hex.EncodeToString(b)
}

// HasNotification — the "ilk defa" gate for the team-routing mail
// (v0.8.429): has a SUCCESSFUL send for (relatedKind, relatedID,
// channelName) already been logged? Bounded to the table's 90-day
// retention window; the row volume is notification-scale (not span-
// scale) so the related_id predicate without a prefix key is fine
// under the execution cap.
func (s *Store) HasNotification(ctx context.Context, relatedKind, relatedID, channelName string) (bool, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT 1 FROM notification_log
		WHERE sent_at >= now() - INTERVAL 90 DAY
		  AND related_kind = ? AND related_id = ? AND channel_name = ? AND ok = 1
		LIMIT 1
		SETTINGS max_execution_time = 10`,
		relatedKind, relatedID, channelName)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	return rows.Next(), rows.Err()
}
