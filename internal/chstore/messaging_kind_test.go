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
				applyMsgKindSplit(&r, f.kind, f.calls, f.errs, 0)
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

// TestApplyMsgKindSplitP95 — v0.9.816. Gecikme ayrışması: üretim p95
// (publish) ile işleme p95 (process) AYRI kovalara iner.
//
// SAYILAR TOPLANIR, QUANTİLE TOPLANMAZ. Sorgu (system, cluster,
// destination, kind) ile grupladığı için satır başına tek p95 gelir;
// fold yine de MAX alır, çünkü o değişmez bir gün bozulursa (GROUP BY
// genişler) keyfi bir "son yazan" değil GÖRÜLEN EN KÖTÜSÜ raporlanmalı:
// gecikme kolonunun sessizce iyimser olması, kötümser olmasından
// tehlikelidir.
func TestApplyMsgKindSplitP95(t *testing.T) {
	t.Run("üretim ve işleme AYRI kovalara iner", func(t *testing.T) {
		var r MessagingInstance
		applyMsgKindSplit(&r, "producer", 100, 0, 12.5)
		applyMsgKindSplit(&r, "consumer", 100, 0, 480)
		if r.ProduceP95Ms != 12.5 {
			t.Errorf("ProduceP95Ms = %v, beklenen 12.5", r.ProduceP95Ms)
		}
		if r.ConsumeP95Ms != 480 {
			t.Errorf("ConsumeP95Ms = %v, beklenen 480", r.ConsumeP95Ms)
		}
		// Sızma olmamalı: hızlı üretici yavaş tüketiciyi maskelememeli.
		if r.ProduceP95Ms > r.ConsumeP95Ms {
			t.Error("kovalar karıştı — ayrışmanın tüm sebebi bu ayrım")
		}
	})

	t.Run("tekrarlı fold EN KÖTÜYÜ tutar (quantile ortalanamaz)", func(t *testing.T) {
		var r MessagingInstance
		applyMsgKindSplit(&r, "consumer", 10, 0, 900)
		applyMsgKindSplit(&r, "consumer", 10, 0, 300)
		if r.ConsumeP95Ms != 900 {
			t.Errorf("ConsumeP95Ms = %v, beklenen 900 — sonraki düşük değer YÜKSEĞİ EZMEMELİ", r.ConsumeP95Ms)
		}
	})

	t.Run("ayrım dışı kind p95 taşımaz", func(t *testing.T) {
		var r MessagingInstance
		applyMsgKindSplit(&r, "client", 10, 0, 5000)
		applyMsgKindSplit(&r, "", 10, 0, 5000)
		if r.ProduceP95Ms != 0 || r.ConsumeP95Ms != 0 {
			t.Errorf("broker chatter'ı p95 kovalarına sızdı: produce=%v consume=%v",
				r.ProduceP95Ms, r.ConsumeP95Ms)
		}
	})

	t.Run("ölçüm yok → 0 kalır (omitempty ile alan düşer, FE '—' basar)", func(t *testing.T) {
		var r MessagingInstance
		applyMsgKindSplit(&r, "producer", 100, 0, 0)
		if r.ProduceP95Ms != 0 {
			t.Errorf("ProduceP95Ms = %v, beklenen 0", r.ProduceP95Ms)
		}
	})
}
