package chstore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ServiceContract is one operator-curated architectural
// assertion (v0.5.191). Two rule types:
//
//   - "must-call":  the contract is violated when `service`
//     fails to call `target_service` over the eval window.
//     Use for "auth must call audit-log" patterns.
//   - "forbidden":  the contract is violated when `service`
//     DOES call `target_service` over the eval window. Use
//     for "billing must NOT call user-profile directly"
//     patterns (force traffic through a gateway/permission
//     layer instead).
//
// Stored in `service_contracts` ReplacingMergeTree. The
// version column lets edits supersede old rows without ALTER
// gymnastics; soft-disable via `enabled=0` leaves the
// definition for audit but stops the evaluator from firing.
type ServiceContract struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Service       string `json:"service"`
	RuleType      string `json:"ruleType"`      // must-call | forbidden
	TargetService string `json:"targetService"`
	Description   string `json:"description"`
	Severity      string `json:"severity"`      // info | warning | critical
	Enabled       bool   `json:"enabled"`
	CreatedBy     string `json:"createdBy"`
	CreatedAt     int64  `json:"createdAt"` // unix ns
}

const (
	ContractMustCall  = "must-call"
	ContractForbidden = "forbidden"
)

// ContractViolation is one evaluation result row: a contract
// whose actual topology didn't match the assertion within the
// evaluation window. Surfaced on /admin/contracts so the
// operator can either fix the topology (the usual response) or
// retire the contract (when the architecture has legitimately
// changed and the old rule is stale).
type ContractViolation struct {
	Contract  ServiceContract `json:"contract"`
	Observed  bool            `json:"observed"`     // did the edge exist in the window
	EdgeCalls uint64          `json:"edgeCalls"`    // calls observed if any
	Since     int64           `json:"since"`        // window start unix ns
	Detected  int64           `json:"detected"`     // unix ns
}

// ListServiceContracts returns every contract — enabled and
// disabled. Filter to enabled-only at the caller when running
// the evaluator.
func (s *Store) ListServiceContracts(ctx context.Context) ([]ServiceContract, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT id, name, service, rule_type, target_service,
		       description, severity, enabled, created_by,
		       toUnixTimestamp64Nano(created_at)
		FROM service_contracts FINAL
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ServiceContract{}
	for rows.Next() {
		var c ServiceContract
		var enabled uint8
		if err := rows.Scan(&c.ID, &c.Name, &c.Service, &c.RuleType, &c.TargetService,
			&c.Description, &c.Severity, &enabled, &c.CreatedBy, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.Enabled = enabled == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpsertServiceContract inserts or updates a contract (the
// ReplacingMergeTree dedupes on id + keeps the highest version).
// Caller supplies the id when editing an existing row; pass ""
// to mint a new one.
func (s *Store) UpsertServiceContract(ctx context.Context, c *ServiceContract) error {
	if c.ID == "" {
		c.ID = randHex(8)
	}
	c.Service = strings.TrimSpace(c.Service)
	c.TargetService = strings.TrimSpace(c.TargetService)
	if c.Service == "" || c.TargetService == "" {
		return fmt.Errorf("service and targetService required")
	}
	if c.RuleType != ContractMustCall && c.RuleType != ContractForbidden {
		return fmt.Errorf("ruleType must be %q or %q", ContractMustCall, ContractForbidden)
	}
	if c.Severity == "" {
		c.Severity = "warning"
	}
	enabled := uint8(1)
	if !c.Enabled {
		enabled = 0
	}
	now := time.Now()
	created := now
	if c.CreatedAt > 0 {
		created = time.Unix(0, c.CreatedAt)
	}
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO service_contracts
		(id, name, service, rule_type, target_service, description,
		 severity, enabled, created_by, created_at, version)`)
	if err != nil {
		return err
	}
	if err := batch.Append(
		c.ID, c.Name, c.Service, c.RuleType, c.TargetService, c.Description,
		c.Severity, enabled, c.CreatedBy, created, uint64(now.UnixNano()),
	); err != nil {
		return err
	}
	return batch.Send()
}

// DeleteServiceContract hard-removes the contract row (ALTER
// DELETE). Matches the delete semantics of alert_rules + the
// other admin-curated tables — operator hitting Delete expects
// the row to GO AWAY, not stay as a tombstone.
func (s *Store) DeleteServiceContract(ctx context.Context, id string) error {
	return s.conn.Exec(ctx, `ALTER TABLE service_contracts DELETE WHERE id = ?`, id)
}

// EvaluateServiceContracts checks every ENABLED contract
// against the topology over the recent window. Returns
// violations only — clean contracts are implicit. The query
// reads topology_edges_5m, which is pre-aggregated by the
// background goroutine, so even at billion-span scale the
// evaluator is sub-second.
//
// windowMinutes is the lookback. Defaults to 30 min — small
// enough to catch fresh violations, large enough not to be
// flaky from a 5-minute aggregation bucket having no data yet.
func (s *Store) EvaluateServiceContracts(
	ctx context.Context, windowMinutes int,
) ([]ContractViolation, error) {
	if windowMinutes <= 0 || windowMinutes > 24*60 {
		windowMinutes = 30
	}
	contracts, err := s.ListServiceContracts(ctx)
	if err != nil {
		return nil, err
	}
	if len(contracts) == 0 {
		return nil, nil
	}
	since := time.Now().Add(-time.Duration(windowMinutes) * time.Minute)

	// One pass to find every (service, target) edge that
	// appeared in the window. We pull DISTINCT pairs because
	// some contracts only care about presence/absence, not call
	// volume — but we sum calls too for the "observed N times"
	// detail line.
	rows, err := s.conn.Query(ctx, `
		SELECT parent_service, child_service, sum(calls) AS total
		FROM topology_edges_5m FINAL
		WHERE time_bucket >= toStartOfFiveMinute(toDateTime(?, 'UTC'))
		GROUP BY parent_service, child_service
		SETTINGS max_execution_time = 10`,
		since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type edgeKey struct{ src, dst string }
	calls := map[edgeKey]uint64{}
	for rows.Next() {
		var src, dst string
		var n uint64
		if err := rows.Scan(&src, &dst, &n); err != nil {
			return nil, err
		}
		calls[edgeKey{src, dst}] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	now := time.Now().UnixNano()
	sinceNs := since.UnixNano()
	out := []ContractViolation{}
	for _, c := range contracts {
		if !c.Enabled {
			continue
		}
		k := edgeKey{c.Service, c.TargetService}
		n, observed := calls[k]
		switch c.RuleType {
		case ContractMustCall:
			if !observed {
				out = append(out, ContractViolation{
					Contract: c, Observed: false,
					EdgeCalls: 0, Since: sinceNs, Detected: now,
				})
			}
		case ContractForbidden:
			if observed && n > 0 {
				out = append(out, ContractViolation{
					Contract: c, Observed: true,
					EdgeCalls: n, Since: sinceNs, Detected: now,
				})
			}
		}
	}
	return out, nil
}
