package chstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"
)

// Public status page data layer — operator-curated components, email
// subscribers, and the published-incidents marker. Read by the unauth
// /api/public-status endpoint and managed by the operator's admin UI.

type StatusPageConfig struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	SupportURL  string `json:"supportUrl"`
}

type StatusComponent struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	MonitorID    string `json:"monitorId,omitempty"`
	ServiceName  string `json:"serviceName,omitempty"`
	DisplayOrder int32  `json:"displayOrder"`
	CreatedAt    int64  `json:"createdAt"`
}

type StatusSubscriber struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	Verified      bool   `json:"verified"`
	// Token is only populated on the read path internally —
	// never round-trips to the admin UI. The public confirm
	// link is the only consumer.
	Token         string `json:"-"`
	ConfirmSentAt int64  `json:"confirmSentAt,omitempty"`
	CreatedAt     int64  `json:"createdAt"`
}

type PublishedIncident struct {
	IncidentID  string `json:"incidentId"`
	Published   bool   `json:"published"`
	PublicTitle string `json:"publicTitle,omitempty"`
	PublicBody  string `json:"publicBody,omitempty"`
	UpdatedAt   int64  `json:"updatedAt"`
}

func (s *Store) GetStatusPageConfig(ctx context.Context) (StatusPageConfig, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT title, description, support_url
		FROM status_page_config FINAL WHERE id = 'default' LIMIT 1`)
	var c StatusPageConfig
	if err := row.Scan(&c.Title, &c.Description, &c.SupportURL); err != nil {
		// Row absent → return zero-value defaults so first-load works
		// without seeding.
		if strings.Contains(err.Error(), "no rows") {
			return StatusPageConfig{Title: "Service Status"}, nil
		}
		return StatusPageConfig{}, err
	}
	return c, nil
}

func (s *Store) UpsertStatusPageConfig(ctx context.Context, c StatusPageConfig) error {
	if c.Title == "" {
		c.Title = "Service Status"
	}
	now := time.Now().UTC()
	batch, err := s.conn.PrepareBatch(ctx,
		`INSERT INTO status_page_config (id, title, description, support_url, updated_at, version)`)
	if err != nil {
		return err
	}
	if err := batch.Append("default", c.Title, c.Description, c.SupportURL,
		now, uint64(now.UnixNano())); err != nil {
		return err
	}
	return batch.Send()
}

func (s *Store) ListStatusComponents(ctx context.Context) ([]StatusComponent, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT id, name, description, monitor_id, service_name, display_order,
		       toUnixTimestamp64Nano(created_at)
		FROM status_page_components FINAL
		ORDER BY display_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StatusComponent{}
	for rows.Next() {
		var c StatusComponent
		if err := rows.Scan(&c.ID, &c.Name, &c.Description, &c.MonitorID,
			&c.ServiceName, &c.DisplayOrder, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) UpsertStatusComponent(ctx context.Context, c *StatusComponent) error {
	if c.ID == "" {
		c.ID = randHex(8)
	}
	if c.Name == "" {
		c.Name = "Unnamed component"
	}
	now := time.Now().UTC()
	createdAt := now
	if c.CreatedAt > 0 {
		createdAt = time.Unix(0, c.CreatedAt).UTC()
	}
	batch, err := s.conn.PrepareBatch(ctx,
		`INSERT INTO status_page_components (id, name, description, monitor_id,
		 service_name, display_order, created_at, version)`)
	if err != nil {
		return err
	}
	if err := batch.Append(c.ID, c.Name, c.Description, c.MonitorID,
		c.ServiceName, c.DisplayOrder, createdAt, uint64(now.UnixNano())); err != nil {
		return err
	}
	if err := batch.Send(); err != nil {
		return err
	}
	c.CreatedAt = createdAt.UnixNano()
	return nil
}

func (s *Store) DeleteStatusComponent(ctx context.Context, id string) error {
	return s.conn.Exec(ctx, `ALTER TABLE status_page_components DELETE WHERE id = ?`, id)
}

func (s *Store) ListStatusSubscribers(ctx context.Context) ([]StatusSubscriber, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT id, email, verified,
		       toUnixTimestamp64Nano(confirm_sent_at),
		       toUnixTimestamp64Nano(created_at)
		FROM status_page_subscribers FINAL
		ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StatusSubscriber{}
	for rows.Next() {
		var s StatusSubscriber
		var v uint8
		if err := rows.Scan(&s.ID, &s.Email, &v, &s.ConfirmSentAt, &s.CreatedAt); err != nil {
			return nil, err
		}
		s.Verified = v == 1
		out = append(out, s)
	}
	return out, rows.Err()
}

// AddStatusSubscriber inserts an unverified subscriber row + a
// freshly-minted confirm token. The token is returned so the
// caller (api layer) can deliver it via SMTP without us having
// to know the public URL here. If the row already exists and is
// verified, we treat the call as a no-op and return empty token
// — re-confirming a verified email is just noise. If the row
// exists but is unverified, we mint a new token (covers
// "operator lost the confirmation mail" without leaking signal
// to a third party).
func (s *Store) AddStatusSubscriber(ctx context.Context, email string) (string, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return "", nil
	}
	// Check existing state — verified ones don't get a re-send;
	// unverified ones get a fresh token (overwrites previous).
	existing := s.conn.QueryRow(ctx,
		`SELECT verified FROM status_page_subscribers FINAL WHERE email = ? LIMIT 1`, email)
	var v uint8
	if err := existing.Scan(&v); err == nil && v == 1 {
		return "", nil
	}
	token := randToken(16)
	now := time.Now().UTC()
	batch, err := s.conn.PrepareBatch(ctx,
		`INSERT INTO status_page_subscribers
		 (id, email, verified, confirm_token, confirm_sent_at, created_at, version)`)
	if err != nil {
		return "", err
	}
	id := randHex(8)
	if err := batch.Append(id, email, uint8(0), token, now, now, uint64(now.UnixNano())); err != nil {
		return "", err
	}
	if err := batch.Send(); err != nil {
		return "", err
	}
	return token, nil
}

// AddVerifiedSubscriber is the admin-curated path. An operator
// invites a teammate from the admin UI; that subscriber is
// trusted to already exist and skips the email confirmation
// dance entirely.
func (s *Store) AddVerifiedSubscriber(ctx context.Context, email string) error {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return nil
	}
	now := time.Now().UTC()
	batch, err := s.conn.PrepareBatch(ctx,
		`INSERT INTO status_page_subscribers
		 (id, email, verified, confirm_token, confirm_sent_at, created_at, version)`)
	if err != nil {
		return err
	}
	id := randHex(8)
	if err := batch.Append(id, email, uint8(1), "", time.Unix(0, 0).UTC(), now, uint64(now.UnixNano())); err != nil {
		return err
	}
	return batch.Send()
}

// ConfirmStatusSubscriber flips verified=1 and clears the
// confirm token when the operator clicks the link in the
// confirmation email. Returns the email so the caller can
// render a thank-you page. Empty email + nil error = token
// not found / already consumed.
func (s *Store) ConfirmStatusSubscriber(ctx context.Context, token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", nil
	}
	row := s.conn.QueryRow(ctx, `
		SELECT email, verified
		FROM status_page_subscribers FINAL
		WHERE confirm_token = ? LIMIT 1`, token)
	var email string
	var v uint8
	if err := row.Scan(&email, &v); err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return "", nil
		}
		return "", err
	}
	if v == 1 {
		// Already verified — treat as success so a refresh of
		// the confirm page after first click doesn't error.
		return email, nil
	}
	now := time.Now().UTC()
	batch, err := s.conn.PrepareBatch(ctx,
		`INSERT INTO status_page_subscribers
		 (id, email, verified, confirm_token, confirm_sent_at, created_at, version)`)
	if err != nil {
		return "", err
	}
	id := randHex(8)
	if err := batch.Append(id, email, uint8(1), "", time.Unix(0, 0).UTC(), now, uint64(now.UnixNano())); err != nil {
		return "", err
	}
	if err := batch.Send(); err != nil {
		return "", err
	}
	return email, nil
}

// randToken — URL-safe hex string for the confirmation link.
// 32 hex chars (16 bytes) is well past the brute-force threshold
// for a non-time-sensitive secret.
func randToken(nbytes int) string {
	b := make([]byte, nbytes)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

func (s *Store) RemoveStatusSubscriber(ctx context.Context, email string) error {
	return s.conn.Exec(ctx, `ALTER TABLE status_page_subscribers DELETE WHERE email = ?`,
		strings.ToLower(strings.TrimSpace(email)))
}

func (s *Store) IsIncidentPublished(ctx context.Context, incidentID string) (PublishedIncident, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT incident_id, published, public_title, public_body,
		       toUnixTimestamp64Nano(updated_at)
		FROM status_page_published FINAL WHERE incident_id = ? LIMIT 1`, incidentID)
	var p PublishedIncident
	var pub uint8
	if err := row.Scan(&p.IncidentID, &pub, &p.PublicTitle, &p.PublicBody, &p.UpdatedAt); err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return PublishedIncident{IncidentID: incidentID}, nil
		}
		return PublishedIncident{}, err
	}
	p.Published = pub == 1
	return p, nil
}

func (s *Store) SetIncidentPublished(ctx context.Context, p PublishedIncident) error {
	now := time.Now().UTC()
	pub := uint8(0)
	if p.Published {
		pub = 1
	}
	batch, err := s.conn.PrepareBatch(ctx,
		`INSERT INTO status_page_published (incident_id, published, public_title, public_body, updated_at, version)`)
	if err != nil {
		return err
	}
	if err := batch.Append(p.IncidentID, pub, p.PublicTitle, p.PublicBody,
		now, uint64(now.UnixNano())); err != nil {
		return err
	}
	return batch.Send()
}

// ListPublishedIncidents returns incidents marked public with an
// optional status filter. Used by the public page to render recent +
// active incidents.
func (s *Store) ListPublishedIncidents(ctx context.Context, limit int) ([]Incident, map[string]PublishedIncident, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	rows, err := s.conn.Query(ctx, `
		SELECT i.id, i.title, i.severity, i.status, i.service, i.summary,
		       toUnixTimestamp64Nano(i.started_at),
		       toUnixTimestamp64Nano(i.resolved_at),
		       p.public_title, p.public_body
		FROM status_page_published p FINAL
		INNER JOIN incidents i FINAL ON i.id = p.incident_id
		WHERE p.published = 1
		ORDER BY i.started_at DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	incidents := []Incident{}
	pubmap := map[string]PublishedIncident{}
	for rows.Next() {
		var i Incident
		var resolved *int64
		var pubTitle, pubBody string
		if err := rows.Scan(&i.ID, &i.Title, &i.Severity, &i.Status, &i.Service,
			&i.Summary, &i.StartedAt, &resolved, &pubTitle, &pubBody); err != nil {
			return nil, nil, err
		}
		if resolved != nil && *resolved > 0 {
			i.ResolvedAt = resolved
		}
		incidents = append(incidents, i)
		pubmap[i.ID] = PublishedIncident{
			IncidentID:  i.ID,
			Published:   true,
			PublicTitle: pubTitle,
			PublicBody:  pubBody,
		}
	}
	return incidents, pubmap, rows.Err()
}

// ComponentUptime computes uptime% for a monitor over the last N days
// from monitor_results. Returns one ratio per day (0.0..1.0) where the
// most recent day is last. Days with no probe data return -1 to signal
// "no data" so the UI can render those bars in grey rather than green.
func (s *Store) ComponentUptime(ctx context.Context, monitorID string, days int) ([]float64, error) {
	if monitorID == "" || days <= 0 {
		return nil, nil
	}
	if days > 90 {
		days = 90
	}
	rows, err := s.conn.Query(ctx, `
		SELECT toDate(time)                                  AS day,
		       countIf(status = 'up')                        AS up_count,
		       count()                                       AS total_count
		FROM monitor_results
		WHERE monitor_id = ?
		  AND time >= toStartOfDay(now() - INTERVAL ? DAY)
		GROUP BY day
		ORDER BY day`, monitorID, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byDay := map[string]float64{}
	for rows.Next() {
		var day time.Time
		var up, total uint64
		if err := rows.Scan(&day, &up, &total); err != nil {
			return nil, err
		}
		ratio := -1.0
		if total > 0 {
			ratio = float64(up) / float64(total)
		}
		byDay[day.Format("2006-01-02")] = ratio
	}
	out := make([]float64, days)
	now := time.Now().UTC()
	for i := 0; i < days; i++ {
		d := now.AddDate(0, 0, -(days - 1 - i)).Format("2006-01-02")
		if v, ok := byDay[d]; ok {
			out[i] = v
		} else {
			out[i] = -1
		}
	}
	return out, nil
}
