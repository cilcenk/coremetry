package chstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ── system_settings ─────────────────────────────────────────────────────────
//
// Key/value store for global configuration that has to outlive a process.
// SMTP credentials live here today; future global toggles (signup
// allowed?, default retention overrides…) can reuse it.

// GetSetting returns the JSON-encoded value for key, or nil if missing.
func (s *Store) GetSetting(ctx context.Context, key string) ([]byte, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT value FROM system_settings FINAL WHERE key = ? LIMIT 1`, key)
	var v string
	if err := row.Scan(&v); err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	return []byte(v), nil
}

// PutSetting upserts the JSON-encoded value at key.
func (s *Store) PutSetting(ctx context.Context, key string, value []byte) error {
	batch, err := s.conn.PrepareBatch(ctx, "INSERT INTO system_settings (key, value)")
	if err != nil {
		return fmt.Errorf("prepare settings: %w", err)
	}
	if err := batch.Append(key, string(value)); err != nil {
		return fmt.Errorf("append setting: %w", err)
	}
	return batch.Send()
}

// ── notification_channels ───────────────────────────────────────────────────

type NotificationChannel struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Type        string          `json:"type"`        // email | slack | webhook
	Config      json.RawMessage `json:"config"`      // type-specific
	Enabled     bool            `json:"enabled"`
	MinSeverity string          `json:"minSeverity"` // info | warning | critical
	// MatchRules — routing predicates. Empty / zero-value
	// fields mean "match anything" so the default channel
	// stays a catch-all. Populated arrays AND together: a
	// channel only fires when its services / sreTeams /
	// ownerTeams ALL match the problem's service catalog.
	MatchRules  ChannelMatchRules `json:"matchRules,omitempty"`
	CreatedAt   int64           `json:"createdAt"`   // unix ns
}

// ChannelMatchRules — small predicate set that gates
// delivery per channel. Each list is "OR within, AND between
// lists":
//   • services    = []string of literal service names
//   • sreTeams    = []string of catalog SRE team names
//   • ownerTeams  = []string of catalog product owner team names
//   • clusters    = []string of k8s/openshift cluster names —
//     matches against the problem's enriched cluster list
//     (typically populated by EnrichProblemsWithClusters
//     before the channel fan-out)
//   • quietHours  = "HH:MM-HH:MM" window during which the
//     channel does NOT fire. Empty = always-on. The window
//     may cross midnight (e.g. "22:00-07:00"); evaluated in
//     QuietHoursTz which defaults to UTC.
//   • quietHoursTz = IANA timezone for quietHours (e.g.
//     "Europe/Istanbul"). Empty = UTC.
//
// Common operator patterns this supports:
//   • "Pager rota only for prod-eu-west during business hrs":
//     clusters=[prod-eu-west], quietHours="00:00-08:00",
//     quietHoursTz="Europe/Istanbul"
//   • "Staging channel — staging cluster only":
//     clusters=[prod-staging]
//   • "Weekend on-call inbox":
//     ownerTeams=[payments], quietHours empty
type ChannelMatchRules struct {
	Services     []string `json:"services,omitempty"`
	SRETeams     []string `json:"sreTeams,omitempty"`
	OwnerTeams   []string `json:"ownerTeams,omitempty"`
	Clusters     []string `json:"clusters,omitempty"`
	QuietHours   string   `json:"quietHours,omitempty"`
	QuietHoursTz string   `json:"quietHoursTz,omitempty"`
}

// MatchInput bundles the runtime signals Matches needs. We
// pass a struct instead of growing the arg list every time we
// add a predicate; existing call sites switched to use it via
// MatchesProblem below.
type MatchInput struct {
	Service  string
	Metadata *ServiceMetadata
	Clusters []string  // problem.Clusters after enrichment
	Now      time.Time // override for tests; zero = time.Now()
}

// MatchesProblem evaluates every predicate against a Problem's
// runtime signals. Empty / zero-value rules mean catch-all
// (always true); the predicate's job is to PROVE the channel
// should be silenced, otherwise we fire.
func (m ChannelMatchRules) MatchesProblem(in MatchInput) bool {
	if !m.Matches(in.Service, in.Metadata) {
		return false
	}
	if len(m.Clusters) > 0 {
		if len(in.Clusters) == 0 {
			return false
		}
		hit := false
		for _, c := range m.Clusters {
			for _, pc := range in.Clusters {
				if c == pc {
					hit = true
					break
				}
			}
			if hit {
				break
			}
		}
		if !hit {
			return false
		}
	}
	if m.QuietHours != "" {
		now := in.Now
		if now.IsZero() {
			now = time.Now()
		}
		if isInQuietWindow(now, m.QuietHours, m.QuietHoursTz) {
			return false
		}
	}
	return true
}

// Matches retains the pre-v0.5.63 signature so existing
// callers that only need service / catalog matching keep
// compiling. New code paths thread MatchInput through
// MatchesProblem.
func (m ChannelMatchRules) Matches(service string, md *ServiceMetadata) bool {
	if len(m.Services) > 0 {
		hit := false
		for _, s := range m.Services {
			if s == service {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	if len(m.SRETeams) > 0 {
		if md == nil {
			return false
		}
		hit := false
		for _, t := range m.SRETeams {
			if t == md.SRETeam {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	if len(m.OwnerTeams) > 0 {
		if md == nil {
			return false
		}
		hit := false
		for _, t := range m.OwnerTeams {
			if t == md.OwnerTeam {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// isInQuietWindow parses "HH:MM-HH:MM" and returns true when
// `now` (in the configured timezone) falls inside the
// window. Windows that span midnight (start > end) are
// supported — operators with after-hours rotas need them.
// Malformed input returns false so a typo doesn't silence
// the channel forever.
func isInQuietWindow(now time.Time, window, tz string) bool {
	loc := time.UTC
	if tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	now = now.In(loc)
	parts := strings.SplitN(window, "-", 2)
	if len(parts) != 2 {
		return false
	}
	startH, startM, ok1 := parseHHMM(parts[0])
	endH, endM, ok2 := parseHHMM(parts[1])
	if !ok1 || !ok2 {
		return false
	}
	nowMin := now.Hour()*60 + now.Minute()
	startMin := startH*60 + startM
	endMin := endH*60 + endM
	if startMin <= endMin {
		return nowMin >= startMin && nowMin < endMin
	}
	// Crosses midnight: in-window if now ≥ start OR now < end.
	return nowMin >= startMin || nowMin < endMin
}

func parseHHMM(s string) (int, int, bool) {
	s = strings.TrimSpace(s)
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return 0, 0, false
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

func (s *Store) ListChannels(ctx context.Context) ([]NotificationChannel, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT id, name, type, config, enabled, min_severity,
		       match_rules,
		       toUnixTimestamp64Nano(created_at)
		FROM notification_channels FINAL
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NotificationChannel
	for rows.Next() {
		var c NotificationChannel
		var config, matchRules string
		var enabled uint8
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &config, &enabled, &c.MinSeverity,
			&matchRules, &c.CreatedAt); err != nil {
			return nil, err
		}
		if config == "" {
			config = "{}"
		}
		c.Config = json.RawMessage(config)
		c.Enabled = enabled != 0
		// Match rules are stored as a JSON blob in the column;
		// errors (malformed legacy data) collapse to the
		// empty / catch-all value rather than dropping the
		// whole channel.
		if matchRules != "" {
			_ = json.Unmarshal([]byte(matchRules), &c.MatchRules)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// EnabledChannelsForSeverity is what the notifier calls when a Problem
// opens. Returns only enabled channels whose min_severity ≤ the problem's
// severity (so a "critical" problem fires every channel; "info" fires
// only the ones explicitly subscribed at info level).
func (s *Store) EnabledChannelsForSeverity(ctx context.Context, severity string) ([]NotificationChannel, error) {
	all, err := s.ListChannels(ctx)
	if err != nil {
		return nil, err
	}
	threshold := severityRank(severity)
	out := make([]NotificationChannel, 0, len(all))
	for _, c := range all {
		if !c.Enabled {
			continue
		}
		if severityRank(c.MinSeverity) > threshold {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func (s *Store) GetChannel(ctx context.Context, id string) (*NotificationChannel, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT id, name, type, config, enabled, min_severity,
		       match_rules,
		       toUnixTimestamp64Nano(created_at)
		FROM notification_channels FINAL
		WHERE id = ? LIMIT 1`, id)
	var c NotificationChannel
	var config, matchRules string
	var enabled uint8
	if err := row.Scan(&c.ID, &c.Name, &c.Type, &config, &enabled, &c.MinSeverity,
		&matchRules, &c.CreatedAt); err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	if config == "" {
		config = "{}"
	}
	c.Config = json.RawMessage(config)
	c.Enabled = enabled != 0
	if matchRules != "" {
		_ = json.Unmarshal([]byte(matchRules), &c.MatchRules)
	}
	return &c, nil
}

func (s *Store) UpsertChannel(ctx context.Context, c NotificationChannel) error {
	if c.ID == "" {
		return fmt.Errorf("channel id required")
	}
	if c.MinSeverity == "" {
		c.MinSeverity = "warning"
	}
	if len(c.Config) == 0 {
		c.Config = json.RawMessage("{}")
	}
	if c.CreatedAt == 0 {
		c.CreatedAt = time.Now().UnixNano()
	}
	// Marshal match rules into the column. Always populate
	// the column so the read path doesn't have to handle a
	// "missing argument" form for the legacy rows that pre-
	// date the column — the migration ALTER + the always-
	// populated insert keep the shape stable.
	mr, err := json.Marshal(c.MatchRules)
	if err != nil {
		return fmt.Errorf("marshal match rules: %w", err)
	}
	batch, err := s.conn.PrepareBatch(ctx,
		"INSERT INTO notification_channels (id, name, type, config, enabled, min_severity, match_rules)")
	if err != nil {
		return fmt.Errorf("prepare channels: %w", err)
	}
	var en uint8
	if c.Enabled {
		en = 1
	}
	if err := batch.Append(c.ID, c.Name, c.Type, string(c.Config), en, c.MinSeverity, string(mr)); err != nil {
		return fmt.Errorf("append channel: %w", err)
	}
	return batch.Send()
}

func (s *Store) DeleteChannel(ctx context.Context, id string) error {
	return s.conn.Exec(ctx, `ALTER TABLE notification_channels DELETE WHERE id = ?`, id)
}

func severityRank(s string) int {
	switch s {
	case "critical":
		return 3
	case "warning":
		return 2
	case "info":
		return 1
	}
	return 2 // unknown → treat as warning
}

// ── Team routing (v0.8.429) ─────────────────────────────────────────────────
//
// Operator ask: "yeni bir problem ilk defa geldiğinde ilgili sy ve ug
// team'e bildirim gönderilsin — mailleri katalogdan alsın." The catalog
// carries team NAMES (service_metadata owner_team / sre_team); this
// system_settings blob maps those names to e-mail addresses and gates
// the automatic problem-open → team-mail path in internal/notify.
// One settings key, no new table (invariant #6).

const TeamContactsKey = "team_contacts"

// TeamContacts is the "team_contacts" system_settings value.
type TeamContacts struct {
	Enabled bool `json:"enabled"`
	// MinSeverity — info | warning | critical; "" defaults to warning
	// so a fresh install doesn't mail every info-level blip.
	MinSeverity string `json:"minSeverity,omitempty"`
	// Contacts maps a catalog team name → e-mail address(es). Values
	// may be comma-separated for multi-recipient teams. Lookup is
	// case-insensitive (mixed-casing team attrs, v0.8.330 lesson).
	Contacts map[string]string `json:"contacts"`
}

// SeverityAllows reports whether a problem of severity sev clears the
// blob's MinSeverity floor (default warning).
func (tc TeamContacts) SeverityAllows(sev string) bool {
	min := tc.MinSeverity
	if min == "" {
		min = "warning"
	}
	return severityRank(sev) >= severityRank(min)
}

// EmailsForTeam resolves one catalog team name to its configured
// addresses — case-insensitive key match, comma-split, trimmed.
// Missing / empty team or contact yields nil (callers skip silently;
// the Settings UI surfaces which catalog teams lack an address).
func (tc TeamContacts) EmailsForTeam(team string) []string {
	team = strings.TrimSpace(team)
	if team == "" {
		return nil
	}
	var raw string
	for k, v := range tc.Contacts {
		if strings.EqualFold(strings.TrimSpace(k), team) {
			raw = v
			break
		}
	}
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// GetTeamContacts loads the blob; a missing key returns the zero value
// (disabled, empty map) — never an error the caller must special-case.
func (s *Store) GetTeamContacts(ctx context.Context) (TeamContacts, error) {
	var tc TeamContacts
	raw, err := s.GetSetting(ctx, TeamContactsKey)
	if err != nil || len(raw) == 0 {
		return tc, err
	}
	if err := json.Unmarshal(raw, &tc); err != nil {
		return TeamContacts{}, err
	}
	return tc, nil
}

// PutTeamContacts persists the blob (admin PUT path; audited at the
// API layer like every settings write).
func (s *Store) PutTeamContacts(ctx context.Context, tc TeamContacts) error {
	raw, err := json.Marshal(tc)
	if err != nil {
		return err
	}
	return s.PutSetting(ctx, TeamContactsKey, raw)
}
