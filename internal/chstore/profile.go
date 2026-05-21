package chstore

import (
	"context"
	"fmt"
	"time"
)

// InsertProfile stores a single pprof profile.
func (s *Store) InsertProfile(ctx context.Context, p *Profile) error {
	batch, err := s.conn.PrepareBatch(ctx, "INSERT INTO profiles")
	if err != nil {
		return fmt.Errorf("prepare profiles: %w", err)
	}
	if err := batch.Append(
		p.ProfileID, p.ServiceName, p.HostName, p.ProfileType,
		p.StartTime, p.DurationNs, string(p.PprofData), p.SampleCount,
		p.LabelsKeys, p.LabelsValues,
	); err != nil {
		return fmt.Errorf("append profile: %w", err)
	}
	return batch.Send()
}

type ProfileFilter struct {
	Service     string
	ProfileType string
	From, To    time.Time
	Limit       int
}

// ListProfiles returns recent profiles matching the filter (without payload).
func (s *Store) ListProfiles(ctx context.Context, f ProfileFilter) ([]ProfileRow, error) {
	var wc whereClause
	if !f.From.IsZero() {
		wc.add("start_time >= ?", f.From)
	}
	if !f.To.IsZero() {
		wc.add("start_time <= ?", f.To)
	}
	if f.Service != "" {
		wc.add("service_name = ?", f.Service)
	}
	if f.ProfileType != "" {
		wc.add("profile_type = ?", f.ProfileType)
	}
	if f.Limit == 0 {
		f.Limit = 100
	}
	rows, err := s.conn.Query(ctx, `
		SELECT profile_id, service_name, host_name, profile_type,
		       start_time, duration_ns, sample_count
		FROM profiles `+wc.sql()+`
		ORDER BY start_time DESC
		LIMIT ?`, append(wc.args, f.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProfileRow
	for rows.Next() {
		var p ProfileRow
		var t time.Time
		var durNs int64
		if err := rows.Scan(&p.ProfileID, &p.ServiceName, &p.HostName, &p.ProfileType,
			&t, &durNs, &p.SampleCount); err != nil {
			return nil, err
		}
		p.StartTime = t.UnixNano()
		p.DurationMs = durNs / 1_000_000
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetProfileBytes returns the raw pprof payload for a profile id.
func (s *Store) GetProfileBytes(ctx context.Context, id string) ([]byte, *ProfileRow, error) {
	// ClickHouse driver scans `String` columns into Go strings; convert after.
	var dataStr string
	var t time.Time
	var meta ProfileRow
	var durNs int64
	err := s.conn.QueryRow(ctx, `
		SELECT profile_id, service_name, host_name, profile_type,
		       start_time, duration_ns, sample_count, pprof_data
		FROM profiles WHERE profile_id = ? LIMIT 1`, id).
		Scan(&meta.ProfileID, &meta.ServiceName, &meta.HostName, &meta.ProfileType,
			&t, &durNs, &meta.SampleCount, &dataStr)
	if err != nil {
		return nil, nil, err
	}
	meta.StartTime = t.UnixNano()
	meta.DurationMs = durNs / 1_000_000
	return []byte(dataStr), &meta, nil
}

// ProfilePayload is a profile's raw bytes alongside the metadata
// needed to attribute hotspots back to a host / window.
type ProfilePayload struct {
	ProfileID   string
	ProfileType string
	StartTime   time.Time
	DurationNs  int64
	HostName    string
	Bytes       []byte
}

// IterateProfilePayloads scans matching profiles row-by-row,
// handing each to fn for in-place parsing. The CH driver is
// already streaming (rows.Next() pulls one block at a time);
// this just inverts the call so the caller never holds more
// than one pprof in RAM. Used by the service-level hotspot
// aggregator — a 1h window can match hundreds of MB of raw
// pprof, and the prior ListProfilePayloads variant collected
// them all into a slice before parsing. Returning fn's error
// halts the scan; nil from fn continues.
func (s *Store) IterateProfilePayloads(ctx context.Context, f ProfileFilter, fn func(ProfilePayload) error) error {
	var wc whereClause
	if !f.From.IsZero() {
		wc.add("start_time >= ?", f.From)
	}
	if !f.To.IsZero() {
		wc.add("start_time <= ?", f.To)
	}
	if f.Service != "" {
		wc.add("service_name = ?", f.Service)
	}
	if f.ProfileType != "" {
		wc.add("profile_type = ?", f.ProfileType)
	}
	if f.Limit == 0 {
		f.Limit = 100
	}
	rows, err := s.conn.Query(ctx, `
		SELECT profile_id, profile_type, start_time, duration_ns,
		       host_name, pprof_data
		FROM profiles `+wc.sql()+`
		ORDER BY start_time DESC
		LIMIT ?
		SETTINGS max_execution_time = 5`, append(wc.args, f.Limit)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var p ProfilePayload
		var dataStr string
		if err := rows.Scan(&p.ProfileID, &p.ProfileType, &p.StartTime,
			&p.DurationNs, &p.HostName, &dataStr); err != nil {
			return err
		}
		p.Bytes = []byte(dataStr)
		if err := fn(p); err != nil {
			return err
		}
		// Drop the pprof bytes promptly — without this the
		// loop's local var keeps the previous payload's
		// backing array reachable until the next assignment,
		// which double-peaks RAM under fast scans.
		p.Bytes = nil
	}
	return rows.Err()
}

// ListProfilePayloads returns raw pprof bytes for every profile
// matching the filter. Used by the service-level hotspot
// aggregator — one CH round-trip pulls the full window so the
// API handler can merge in-process. The pprof column is large
// (per-snapshot kilobytes to ~MB), so the caller MUST pass a
// sensible Limit; the query also caps execution at 5s to bound
// blast radius on a wide window.
//
// Deprecated for hotspot aggregation: prefer
// IterateProfilePayloads which keeps RAM proportional to one
// payload rather than the full result set. Retained for any
// caller that needs the whole window materialised.
func (s *Store) ListProfilePayloads(ctx context.Context, f ProfileFilter) ([]ProfilePayload, error) {
	var wc whereClause
	if !f.From.IsZero() {
		wc.add("start_time >= ?", f.From)
	}
	if !f.To.IsZero() {
		wc.add("start_time <= ?", f.To)
	}
	if f.Service != "" {
		wc.add("service_name = ?", f.Service)
	}
	if f.ProfileType != "" {
		wc.add("profile_type = ?", f.ProfileType)
	}
	if f.Limit == 0 {
		f.Limit = 100
	}
	rows, err := s.conn.Query(ctx, `
		SELECT profile_id, profile_type, start_time, duration_ns,
		       host_name, pprof_data
		FROM profiles `+wc.sql()+`
		ORDER BY start_time DESC
		LIMIT ?
		SETTINGS max_execution_time = 5`, append(wc.args, f.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProfilePayload
	for rows.Next() {
		var p ProfilePayload
		var dataStr string
		if err := rows.Scan(&p.ProfileID, &p.ProfileType, &p.StartTime,
			&p.DurationNs, &p.HostName, &dataStr); err != nil {
			return nil, err
		}
		p.Bytes = []byte(dataStr)
		out = append(out, p)
	}
	return out, rows.Err()
}

// IterateProfilesForSpan streams every profile whose sample
// window overlaps a span's window (same overlap rules as
// FindProfilesForSpan), handing the pprof bytes inline so the
// caller doesn't need a second GetProfileBytes per row.
// Removes the N+1 round-trip pattern in
// profileHotspotsForSpan (v0.5.340).
func (s *Store) IterateProfilesForSpan(ctx context.Context, service string, spanStart, spanEnd time.Time, fn func(ProfilePayload) error) error {
	tolStart := spanStart.Add(-profileSnapshotTolerance)
	tolEnd := spanEnd.Add(profileSnapshotTolerance)

	rows, err := s.conn.Query(ctx, `
		SELECT profile_id, profile_type, start_time, duration_ns,
		       host_name, pprof_data
		FROM profiles
		WHERE service_name = ?
		  AND (
		    (duration_ns >  0 AND start_time <= ? AND addNanoseconds(start_time, duration_ns) >= ?)
		    OR
		    (duration_ns =  0 AND start_time >= ? AND start_time <= ?)
		  )
		ORDER BY start_time DESC
		LIMIT 20
		SETTINGS max_execution_time = 5`,
		service, spanEnd, spanStart, tolStart, tolEnd)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var p ProfilePayload
		var dataStr string
		if err := rows.Scan(&p.ProfileID, &p.ProfileType, &p.StartTime,
			&p.DurationNs, &p.HostName, &dataStr); err != nil {
			return err
		}
		p.Bytes = []byte(dataStr)
		if err := fn(p); err != nil {
			return err
		}
		p.Bytes = nil
	}
	return rows.Err()
}

// FindProfilesForSpan returns profiles related to a span's time window.
//
// Two cases:
//   - Ranged profiles (cpu, etc., duration_ns > 0): true overlap test
//     between [profile.start, profile.start+duration_ns] and [spanStart, spanEnd].
//   - Instantaneous profiles (heap, goroutine, alloc, duration_ns = 0):
//     the snapshot has no inherent window, so we pick those captured
//     within ±tolerance of the span — most recent first.
const profileSnapshotTolerance = 30 * time.Second

func (s *Store) FindProfilesForSpan(ctx context.Context, service string, spanStart, spanEnd time.Time) ([]ProfileRow, error) {
	tolStart := spanStart.Add(-profileSnapshotTolerance)
	tolEnd := spanEnd.Add(profileSnapshotTolerance)

	rows, err := s.conn.Query(ctx, `
		SELECT profile_id, service_name, host_name, profile_type,
		       start_time, duration_ns, sample_count
		FROM profiles
		WHERE service_name = ?
		  AND (
		    (duration_ns >  0 AND start_time <= ? AND addNanoseconds(start_time, duration_ns) >= ?)
		    OR
		    (duration_ns =  0 AND start_time >= ? AND start_time <= ?)
		  )
		ORDER BY start_time DESC
		LIMIT 20`, service, spanEnd, spanStart, tolStart, tolEnd)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProfileRow
	for rows.Next() {
		var p ProfileRow
		var t time.Time
		var durNs int64
		if err := rows.Scan(&p.ProfileID, &p.ServiceName, &p.HostName, &p.ProfileType,
			&t, &durNs, &p.SampleCount); err != nil {
			return nil, err
		}
		p.StartTime = t.UnixNano()
		p.DurationMs = durNs / 1_000_000
		out = append(out, p)
	}
	return out, rows.Err()
}
