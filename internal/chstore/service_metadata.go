package chstore

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// CustomLink — one operator-bolted-on link per service. The
// catalog renders these as additional chips next to the
// built-in oncall / runbook / repo entries, so a team can
// surface "Grafana board" / "Kibana saved search" /
// "internal SRE dashboard" in one click without us baking
// each surface in as a column.
type CustomLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// ServiceMetadata is operator-curated context for a single
// service: owner team, oncall channel, runbook URL, repo, and
// a free-text description. Joins to the spans table by
// service_name. Lives in a tiny ReplacingMergeTree separate
// from the spans hot path because the data doesn't fit a
// span resource attribute (it's per-team-decided, not per-
// span-emitted) and the row count is bounded by service
// count not span count.
//
// All fields except `service` are optional; an empty row
// surfaces as "no metadata yet" on the frontend with an edit
// CTA so the catalog grows organically.
type ServiceMetadata struct {
	Service      string `json:"service"`
	OwnerTeam    string `json:"ownerTeam,omitempty"`
	// SRETeam is the platform / reliability team that owns
	// the operational health of the service — typically
	// distinct from the product owner team. Surfaces as a
	// second chip on the catalog pill so the oncall who
	// inherits the service knows who to escalate to for
	// infra issues vs feature regressions.
	SRETeam      string `json:"sreTeam,omitempty"`
	Description  string `json:"description,omitempty"`
	Repository   string `json:"repository,omitempty"`
	RunbookURL   string `json:"runbookUrl,omitempty"`
	OncallURL    string `json:"oncallUrl,omitempty"`
	// ChatChannel — Zoom Chat / Mattermost / Slack channel
	// for the team. Renamed from slack_channel because the
	// catalog target cluster runs on Zoom Chat; the legacy
	// slack_channel column still backfills here on read so
	// pre-rename rows keep showing.
	ChatChannel  string       `json:"chatChannel,omitempty"`
	// CustomLinks — operator-bolted-on links per service
	// (Grafana / Kibana / Sensei / status page / etc.).
	// Stored as a JSON-encoded array in custom_links column.
	CustomLinks  []CustomLink `json:"customLinks,omitempty"`
	UpdatedAt    int64        `json:"updatedAt"` // unix nanoseconds
}

// GetServiceMetadata returns the catalog row for one service.
// Missing rows return nil, nil — the page handles the empty
// state inline (no special "404" UI needed).
//
// Read-time fallback: chat_channel is the new column; if a
// pre-rename row only populated slack_channel we surface that
// value so legacy curation doesn't disappear from the UI.
func (s *Store) GetServiceMetadata(ctx context.Context, service string) (*ServiceMetadata, error) {
	if service == "" {
		return nil, nil
	}
	row := s.conn.QueryRow(ctx, `
		SELECT service, owner_team, sre_team, description, repository,
		       runbook_url, oncall_url, chat_channel, slack_channel,
		       custom_links,
		       toUnixTimestamp64Nano(updated_at)
		FROM service_metadata FINAL
		WHERE service = ?
		LIMIT 1`, service)
	var m ServiceMetadata
	var legacySlack, customLinks string
	if err := row.Scan(&m.Service, &m.OwnerTeam, &m.SRETeam, &m.Description, &m.Repository,
		&m.RunbookURL, &m.OncallURL, &m.ChatChannel, &legacySlack,
		&customLinks, &m.UpdatedAt); err != nil {
		// "no rows" → not yet curated; same handling pattern
		// the rest of chstore uses.
		return nil, nil
	}
	if m.ChatChannel == "" && legacySlack != "" {
		m.ChatChannel = legacySlack
	}
	// Malformed JSON in the column collapses to an empty
	// list rather than failing the read — operator just sees
	// no extra links until they re-save the row.
	if customLinks != "" && customLinks != "[]" {
		_ = json.Unmarshal([]byte(customLinks), &m.CustomLinks)
	}
	return &m, nil
}

// ListServiceMetadata returns every catalog row in one shot —
// used by the /services list to render the owner-team chip on
// every row without N round-trips. Cheap because the table is
// at most a few thousand rows.
func (s *Store) ListServiceMetadata(ctx context.Context) (map[string]ServiceMetadata, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT service, owner_team, sre_team, description, repository,
		       runbook_url, oncall_url, chat_channel, slack_channel,
		       custom_links,
		       toUnixTimestamp64Nano(updated_at)
		FROM service_metadata FINAL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]ServiceMetadata, 64)
	for rows.Next() {
		var m ServiceMetadata
		var legacySlack, customLinks string
		if err := rows.Scan(&m.Service, &m.OwnerTeam, &m.SRETeam, &m.Description, &m.Repository,
			&m.RunbookURL, &m.OncallURL, &m.ChatChannel, &legacySlack,
			&customLinks, &m.UpdatedAt); err != nil {
			return nil, err
		}
		if m.ChatChannel == "" && legacySlack != "" {
			m.ChatChannel = legacySlack
		}
		if customLinks != "" && customLinks != "[]" {
			_ = json.Unmarshal([]byte(customLinks), &m.CustomLinks)
		}
		out[m.Service] = m
	}
	return out, rows.Err()
}

// UpsertServiceMetadata writes a catalog row. Last-write-wins
// via the ReplacingMergeTree's version column; UpdatedAt is
// always stamped to now() so the operator sees fresh edit
// times in the list. Empty `service` is a no-op (you can't
// curate "no service").
//
// Writes only the new chat_channel column. The legacy
// slack_channel column is left as-is so an upgrade-then-
// downgrade still surfaces the original value; the next edit
// after upgrade migrates the value into chat_channel via the
// read-time fallback.
func (s *Store) UpsertServiceMetadata(ctx context.Context, m ServiceMetadata) error {
	m.Service = strings.TrimSpace(m.Service)
	if m.Service == "" {
		return nil
	}
	// Always serialise CustomLinks — even an empty slice
	// produces "[]" which keeps the column shape stable
	// (read path's `customLinks != ""` guard treats "[]" as
	// no-op anyway). Drop entries with empty url/label so
	// the operator's accidental blank rows don't pollute
	// the chip strip.
	clean := make([]CustomLink, 0, len(m.CustomLinks))
	for _, l := range m.CustomLinks {
		if strings.TrimSpace(l.URL) == "" || strings.TrimSpace(l.Label) == "" {
			continue
		}
		clean = append(clean, CustomLink{
			Label: strings.TrimSpace(l.Label),
			URL:   strings.TrimSpace(l.URL),
		})
	}
	clBytes, err := json.Marshal(clean)
	if err != nil {
		return err
	}
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO service_metadata
		(service, owner_team, sre_team, description, repository,
		 runbook_url, oncall_url, chat_channel, custom_links,
		 updated_at, version)`)
	if err != nil {
		return err
	}
	now := time.Now()
	if err := batch.Append(m.Service, m.OwnerTeam, m.SRETeam, m.Description, m.Repository,
		m.RunbookURL, m.OncallURL, m.ChatChannel, string(clBytes),
		now.UTC(), uint64(now.UnixNano())); err != nil {
		return err
	}
	return batch.Send()
}

// ── Auto-derive owner/sre team from span attributes (v0.8.95) ────────────────

// ServiceTeams is the dominant owner/sre team pair derived for one service from
// its span attributes.
type ServiceTeams struct {
	OwnerTeam string
	SRETeam   string
}

// deriveTeamsSQL extracts, per service, the dominant owner-team (ug-team /
// ug_team) and sre-team (sy-team / sy_team) attribute value. Team ownership is a
// stable signal, so the most-frequent value IS the team. Both the resource scope
// (res_keys/res_values — preferred) AND the span scope (attr_keys/attr_values)
// are checked, in that order, and both the hyphen + underscore key spellings.
const deriveTeamsSQL = `
SELECT service_name,
       argMaxIf(ug_val, c, ug_val != '') AS owner,
       argMaxIf(sy_val, c, sy_val != '') AS sre
FROM (
  SELECT service_name, ug_val, sy_val, count() AS c
  FROM (
    SELECT service_name,
      multiIf(
        has(res_keys, 'ug-team'),  res_values[indexOf(res_keys, 'ug-team')],
        has(res_keys, 'ug_team'),  res_values[indexOf(res_keys, 'ug_team')],
        has(attr_keys, 'ug-team'), attr_values[indexOf(attr_keys, 'ug-team')],
        has(attr_keys, 'ug_team'), attr_values[indexOf(attr_keys, 'ug_team')],
        '') AS ug_val,
      multiIf(
        has(res_keys, 'sy-team'),  res_values[indexOf(res_keys, 'sy-team')],
        has(res_keys, 'sy_team'),  res_values[indexOf(res_keys, 'sy_team')],
        has(attr_keys, 'sy-team'), attr_values[indexOf(attr_keys, 'sy-team')],
        has(attr_keys, 'sy_team'), attr_values[indexOf(attr_keys, 'sy_team')],
        '') AS sy_val
    FROM spans
    WHERE time >= ? AND time <= ?
      AND ( has(res_keys, 'ug-team')  OR has(res_keys, 'ug_team')
         OR has(res_keys, 'sy-team')  OR has(res_keys, 'sy_team')
         OR has(attr_keys, 'ug-team') OR has(attr_keys, 'ug_team')
         OR has(attr_keys, 'sy-team') OR has(attr_keys, 'sy_team') )
    LIMIT 2000000
  )
  GROUP BY service_name, ug_val, sy_val
)
GROUP BY service_name
ORDER BY service_name
LIMIT 10000
SETTINGS max_execution_time = 30`

// DeriveServiceTeams returns service → dominant {owner, sre} team derived from
// span/resource attributes over the window. Services emitting none of the four
// keys are omitted.
func (s *Store) DeriveServiceTeams(ctx context.Context, since time.Duration) (map[string]ServiceTeams, error) {
	to := time.Now()
	from := to.Add(-since)
	rows, err := s.conn.Query(ctx, deriveTeamsSQL, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]ServiceTeams, 64)
	for rows.Next() {
		var svc, owner, sre string
		if err := rows.Scan(&svc, &owner, &sre); err != nil {
			return nil, err
		}
		owner, sre = strings.TrimSpace(owner), strings.TrimSpace(sre)
		if owner == "" && sre == "" {
			continue
		}
		out[svc] = ServiceTeams{OwnerTeam: owner, SRETeam: sre}
	}
	return out, rows.Err()
}

// mergeTeams fills owner_team / sre_team ONLY when the catalog field is empty,
// so a manual catalog edit always wins. Returns the (possibly) updated row + a
// changed flag. Pure — unit-tested.
func mergeTeams(md ServiceMetadata, t ServiceTeams) (ServiceMetadata, bool) {
	changed := false
	if md.OwnerTeam == "" && t.OwnerTeam != "" {
		md.OwnerTeam = t.OwnerTeam
		changed = true
	}
	if md.SRETeam == "" && t.SRETeam != "" {
		md.SRETeam = t.SRETeam
		changed = true
	}
	return md, changed
}

// PopulateServiceTeamsFromSpans derives teams from span attributes and fills
// the empty owner_team / sre_team catalog fields (manual values are preserved,
// as are all other metadata fields — UpsertServiceMetadata is a full-row
// replace, so we read-merge-write). Best-effort: a single failed upsert doesn't
// abort the rest. Returns the number of services updated.
func (s *Store) PopulateServiceTeamsFromSpans(ctx context.Context, since time.Duration) (int, error) {
	derived, err := s.DeriveServiceTeams(ctx, since)
	if err != nil {
		return 0, err
	}
	if len(derived) == 0 {
		return 0, nil
	}
	existing, err := s.ListServiceMetadata(ctx)
	if err != nil {
		return 0, err
	}
	updated := 0
	for svc, t := range derived {
		md, ok := existing[svc]
		if !ok {
			md = ServiceMetadata{Service: svc}
		}
		merged, changed := mergeTeams(md, t)
		if !changed {
			continue
		}
		if err := s.UpsertServiceMetadata(ctx, merged); err != nil {
			continue // best-effort; the next tick retries
		}
		updated++
	}
	return updated, nil
}
