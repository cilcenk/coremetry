package chstore

import (
	"testing"
	"time"
)

// analyzeRollouts — pod-churn detection (v0.8.x). The WINDOWED baseline
// (compare to ~10m earlier, not the adjacent bucket) is load-bearing:
// an instant cutover whose transition bucket holds a MIX of old+new
// pods splits the "added" and "removed" across two adjacent
// single-bucket diffs, so a naive i-1 diff would catch NEITHER. The
// "mixed transition bucket" case below pins exactly that regression.
func mkBucket(min int, version string, pods ...string) rolloutBucket {
	return rolloutBucket{
		t:       time.Date(2026, 1, 1, 0, min, 0, 0, time.UTC),
		pods:    pods,
		version: version,
	}
}

func TestAnalyzeRollouts(t *testing.T) {
	tests := []struct {
		name        string
		buckets     []rolloutBucket
		wantCount   int
		wantConst   bool
		wantTracked bool
		// wantKinds (v0.8.405) — expected Kind per detected rollout:
		// "deploy" only when the effective version changed across the
		// churn, else "restart". nil = don't care / no rollouts.
		wantKinds []string
	}{
		{
			name: "stable set → no rollout",
			buckets: []rolloutBucket{
				mkBucket(0, "1", "p1", "p2", "p3"),
				mkBucket(5, "1", "p1", "p2", "p3"),
				mkBucket(10, "1", "p1", "p2", "p3"),
				mkBucket(15, "1", "p1", "p2", "p3"),
			},
			wantCount: 0, wantConst: true, wantTracked: true,
		},
		{
			name: "instant cutover, MIXED transition bucket → 1 (naive i-1 misses this)",
			buckets: []rolloutBucket{
				mkBucket(0, "1", "p1", "p2", "p3"),
				mkBucket(5, "1", "p1", "p2", "p3"),
				mkBucket(10, "1", "p1", "p2", "p3", "q1", "q2", "q3"), // mid-bucket cutover
				mkBucket(15, "1", "q1", "q2", "q3"),
				mkBucket(20, "1", "q1", "q2", "q3"),
			},
			wantCount: 1, wantConst: true, wantTracked: true, wantKinds: []string{"restart"},
		},
		{
			name: "clean instant cutover (no mix) → 1",
			buckets: []rolloutBucket{
				mkBucket(0, "1", "p1", "p2", "p3"),
				mkBucket(5, "1", "p1", "p2", "p3"),
				mkBucket(10, "1", "q1", "q2", "q3"),
				mkBucket(15, "1", "q1", "q2", "q3"),
			},
			wantCount: 1, wantConst: true, wantTracked: true, wantKinds: []string{"restart"},
		},
		{
			name: "gradual rollout over several buckets → coalesced to 1",
			buckets: []rolloutBucket{
				mkBucket(0, "1", "p1", "p2", "p3"),
				mkBucket(5, "1", "p1", "p2", "q1"),
				mkBucket(10, "1", "p1", "q1", "q2"),
				mkBucket(15, "1", "q1", "q2", "q3"),
				mkBucket(20, "1", "q1", "q2", "q3"),
			},
			wantCount: 1, wantConst: true, wantTracked: true, wantKinds: []string{"restart"},
		},
		{
			name: "autoscale up (add, no remove) → no rollout",
			buckets: []rolloutBucket{
				mkBucket(0, "1", "p1", "p2"),
				mkBucket(5, "1", "p1", "p2", "p3"),
				mkBucket(10, "1", "p1", "p2", "p3", "p4"),
			},
			wantCount: 0, wantConst: true, wantTracked: true,
		},
		{
			name: "scale down (remove, no add) → no rollout",
			buckets: []rolloutBucket{
				mkBucket(0, "1", "p1", "p2", "p3", "p4"),
				mkBucket(5, "1", "p1", "p2", "p3"),
				mkBucket(10, "1", "p1", "p2"),
			},
			wantCount: 0, wantConst: true, wantTracked: true,
		},
		{
			name: "no pod identity → no rollout, untracked",
			buckets: []rolloutBucket{
				mkBucket(0, "1"),
				mkBucket(5, "1"),
				mkBucket(10, "1"),
			},
			wantCount: 0, wantConst: true, wantTracked: false,
		},
		{
			// v0.8.405 — operator-reported false-deploy class: bucket
			// presence means "emitted spans that 5 minutes", so pods
			// quiet for ONE bucket (low traffic / collector hiccup)
			// read as removed; a simultaneously-appearing pod (HPA
			// scale-up) completed the add+remove pattern → phantom
			// rollout. Presence smoothing must kill it.
			name: "one quiet bucket + HPA add → NO rollout (v0.8.405)",
			buckets: []rolloutBucket{
				mkBucket(0, "1", "p1", "p2", "p3", "p4"),
				mkBucket(5, "1", "p1", "p2", "p3", "p4"),
				mkBucket(10, "1", "p1", "p2", "x1"), // p3/p4 quiet, x1 scaled up
				mkBucket(15, "1", "p1", "p2", "p3", "p4", "x1"),
				mkBucket(20, "1", "p1", "p2", "p3", "p4", "x1"),
			},
			wantCount: 0, wantConst: true, wantTracked: true,
		},
		{
			// v0.8.405 — pods genuinely replaced (≥2 buckets absent)
			// at a CONSTANT version = reschedule/restart, not a deploy.
			name: "real replacement, constant version → kind restart",
			buckets: []rolloutBucket{
				mkBucket(0, "1", "p1", "p2", "p3"),
				mkBucket(5, "1", "p1", "p2", "p3"),
				mkBucket(10, "1", "q1", "q2", "q3"),
				mkBucket(15, "1", "q1", "q2", "q3"),
				mkBucket(20, "1", "q1", "q2", "q3"),
			},
			wantCount: 1, wantConst: true, wantTracked: true, wantKinds: []string{"restart"},
		},
		{
			name: "version changes across the rollout → versionConstant false",
			buckets: []rolloutBucket{
				mkBucket(0, "1.0.0", "p1", "p2", "p3"),
				mkBucket(5, "1.0.0", "p1", "p2", "p3"),
				mkBucket(10, "1.0.1", "q1", "q2", "q3"),
				mkBucket(15, "1.0.1", "q1", "q2", "q3"),
			},
			wantCount: 1, wantConst: false, wantTracked: true, wantKinds: []string{"deploy"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := analyzeRollouts("svc", tt.buckets)
			if len(res.Rollouts) != tt.wantCount {
				t.Errorf("rollouts = %d, want %d", len(res.Rollouts), tt.wantCount)
			}
			if res.VersionConstant != tt.wantConst {
				t.Errorf("versionConstant = %v, want %v", res.VersionConstant, tt.wantConst)
			}
			if res.InstancesTracked != tt.wantTracked {
				t.Errorf("instancesTracked = %v, want %v", res.InstancesTracked, tt.wantTracked)
			}
			if tt.wantKinds != nil {
				for i, want := range tt.wantKinds {
					if i >= len(res.Rollouts) {
						break
					}
					if got := res.Rollouts[i].Kind; got != want {
						t.Errorf("rollout[%d].kind = %q, want %q", i, got, want)
					}
				}
			}
		})
	}
}
