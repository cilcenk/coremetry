package chstore

// messaging_kind_test.go — v0.8.364 (Stage-2 slice M1). Pins the
// producer/consumer fold that turns messaging_caller_summary_5m
// kind-rollup rows into the overview's split counters: producer /
// consumer accumulate into their buckets, every other OTel span
// kind (client / server / internal / empty) touches neither.

import "testing"

func TestApplyMsgKindSplit(t *testing.T) {
	cases := []struct {
		name string
		// sequence of (kind, calls, errs) folds onto one row
		folds []struct {
			kind        string
			calls, errs uint64
		}
		wantProduce, wantProduceErrs uint64
		wantConsume, wantConsumeErrs uint64
	}{
		{
			name: "producer lands in the produce bucket",
			folds: []struct {
				kind        string
				calls, errs uint64
			}{{"producer", 100, 3}},
			wantProduce: 100, wantProduceErrs: 3,
		},
		{
			name: "consumer lands in the consume bucket",
			folds: []struct {
				kind        string
				calls, errs uint64
			}{{"consumer", 40, 1}},
			wantConsume: 40, wantConsumeErrs: 1,
		},
		{
			name: "repeated folds accumulate (multiple MV rollup rows per destination)",
			folds: []struct {
				kind        string
				calls, errs uint64
			}{
				{"producer", 100, 3},
				{"producer", 50, 0},
				{"consumer", 40, 1},
				{"consumer", 10, 2},
			},
			wantProduce: 150, wantProduceErrs: 3,
			wantConsume: 50, wantConsumeErrs: 3,
		},
		{
			name: "client/server/internal/empty kinds touch neither bucket",
			folds: []struct {
				kind        string
				calls, errs uint64
			}{
				{"client", 7, 7},
				{"server", 8, 8},
				{"internal", 9, 9},
				{"", 10, 10},
			},
		},
		{
			name: "kind match is exact — no case folding, no prefixes",
			folds: []struct {
				kind        string
				calls, errs uint64
			}{
				{"Producer", 5, 0},
				{"PRODUCER", 5, 0},
				{"producers", 5, 0},
				{"consumer ", 5, 0},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r MessagingInstance
			for _, f := range tc.folds {
				applyMsgKindSplit(&r, f.kind, f.calls, f.errs)
			}
			if r.ProduceCount != tc.wantProduce || r.ProduceErrors != tc.wantProduceErrs ||
				r.ConsumeCount != tc.wantConsume || r.ConsumeErrors != tc.wantConsumeErrs {
				t.Errorf("got produce=%d/%derr consume=%d/%derr, want produce=%d/%derr consume=%d/%derr",
					r.ProduceCount, r.ProduceErrors, r.ConsumeCount, r.ConsumeErrors,
					tc.wantProduce, tc.wantProduceErrs, tc.wantConsume, tc.wantConsumeErrs)
			}
		})
	}
}
