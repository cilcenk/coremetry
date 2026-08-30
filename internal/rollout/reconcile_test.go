package rollout

import (
	"testing"
	"time"
)

// reconcile_test.go — v0.10.199 sözleşmesi (reconcile.go başlığı; audit §2.4-2.5,
// §11, §14.1). Model: satır = gözlenmiş GİRİŞ olayı; giriş histerezisi 2 kova,
// çıkış histerezisi 6 kova (30 dk); giriş için ≥ 6 kova gözlenmiş yokluk.
// Testlerde pencere b(-10)'dan başlar (girişten önce yeterli boş kova).

var t0 = time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)

func b(i int) time.Time { return t0.Add(time.Duration(i) * 5 * time.Minute) }

func act(rev string, i int, spans int64) Activity {
	return Activity{ClusterID: "c1", Namespace: "pay", Workload: "api", Kind: "Deployment", Revision: rev,
		Bucket: b(i), Spans: spans, FirstSeen: b(i).Add(10 * time.Second), LastSeen: b(i).Add(4 * time.Minute), Image: "reg/api", ImageTag: "t-" + rev}
}

func span(rev string, from, to int, spans int64) []Activity {
	var out []Activity
	for i := from; i <= to; i++ {
		out = append(out, act(rev, i, spans))
	}
	return out
}

// recon — pencere b(-10)'dan; FirstSeen: A pencere başından beri bilinir (7 g ufku).
func recon(cfg Config, now time.Time, prev []Rollout, acts []Activity) []Rollout {
	return Reconcile(cfg, Input{Now: now, WindowStart: b(-10), Prev: prev, Acts: acts})
}

func reconSeen(cfg Config, now time.Time, prev []Rollout, acts []Activity, seen map[string]time.Time) []Rollout {
	return Reconcile(cfg, Input{Now: now, WindowStart: b(-10), Prev: prev, Acts: acts, FirstSeen: map[Key]map[string]time.Time{{"c1", "pay", "api"}: seen}})
}

func find(rows []Rollout, rev string) *Rollout {
	for i := range rows {
		if rows[i].Revision == rev {
			return &rows[i]
		}
	}
	return nil
}

func count(rows []Rollout, rev string) int {
	n := 0
	for _, r := range rows {
		if r.Revision == rev {
			n++
		}
	}
	return n
}

func upsertInto(table *[]Rollout, rows []Rollout) {
	for _, r := range rows {
		replaced := false
		for i := range *table {
			if (*table)[i].Revision == r.Revision && (*table)[i].StartedAt.Equal(r.StartedAt) {
				(*table)[i], replaced = r, true
			}
		}
		if !replaced {
			*table = append(*table, r)
		}
	}
}

func TestReconcile_SimpleRollout_AtoB(t *testing.T) {
	cfg := DefaultConfig()
	acts := append(span("api-aaaa", -10, 3, 100), span("api-bbbb", 3, 12, 80)...)
	out := recon(cfg, b(13), nil, acts)
	bb := find(out, "api-bbbb")
	if bb == nil || !bb.StartedAt.Equal(b(3)) || bb.PrevRevision != "api-aaaa" || bb.PrevImageTag != "t-api-aaaa" {
		t.Fatalf("B giriş satırı (b3, prev=A): %+v", out)
	}
	if bb.Status != StatusCompleted || !bb.TrafficConfirmedAt.Equal(b(4)) || !bb.CompletedAt.Equal(b(4)) {
		t.Fatalf("A kova 3'te son → trafik kova 4'te B'de; completed: %+v", bb)
	}
	if bb.SpanCount != 10*80 || bb.ImageTag != "t-api-bbbb" || bb.Kind != "Deployment" {
		t.Fatalf("toplamlar: %+v", bb)
	}
	if find(out, "api-aaaa") != nil || len(out) != 1 {
		t.Fatalf("kenar koşusu satır açmamalı: %+v", out)
	}
	if eb := find(recon(cfg, b(8), nil, acts), "api-bbbb"); eb == nil || eb.Status != StatusInProgress {
		t.Fatalf("çıkış histerezisi dolmadan completed yazılmaz: %+v", eb)
	}
}

func TestReconcile_HysteresisIgnoresBlip(t *testing.T) {
	cfg := DefaultConfig()
	acts := append(span("api-aaaa", -10, 6, 100), act("api-blip", 2, 50))
	if out := recon(cfg, b(7), nil, acts); find(out, "api-blip") != nil {
		t.Fatalf("tek kovalık parıltı rollout sayılmamalı: %+v", out)
	}
	acts = append(acts, act("api-low", 2, 3), act("api-low", 3, 4))
	if out := recon(cfg, b(7), nil, acts); find(out, "api-low") != nil {
		t.Fatalf("eşik altı span aktif kümeye girmemeli: %+v", out)
	}
}

func TestReconcile_DeterministicAndIdempotent(t *testing.T) {
	cfg := DefaultConfig()
	acts := append(span("api-aaaa", -10, 5, 100), span("api-bbbb", 4, 5, 60)...)
	first := recon(cfg, b(6), nil, acts)
	bb := find(first, "api-bbbb")
	if bb == nil || !bb.StartedAt.Equal(b(4)) || bb.Status != StatusInProgress {
		t.Fatalf("B giriş kovası b4, in_progress: %+v", first)
	}
	if second := recon(cfg, b(6), first, acts); len(second) != 0 {
		t.Fatalf("aynı girdiyle ikinci tik satır üretmemeli: %+v", second)
	}
	// iki yazıcı: now 90 s farklı ve pencere başı bir kova farklı → aynı satır
	other := Reconcile(cfg, Input{Now: b(6).Add(90 * time.Second), WindowStart: b(-11), Acts: acts})
	if ob := find(other, "api-bbbb"); ob == nil || !ob.StartedAt.Equal(bb.StartedAt) || ob.PrevRevision != bb.PrevRevision {
		t.Fatalf("started_at/prev yazıcı-bağımsız olmalı: %+v", other)
	}
}

func TestReconcile_RollbackOnWithdrawnRevisionRow(t *testing.T) {
	cfg := DefaultConfig()
	acts := append(span("api-aaaa", -10, 20, 100), span("api-bbbb", 3, 6, 100)...)
	out := recon(cfg, b(21), nil, acts)
	bb := find(out, "api-bbbb")
	if bb == nil || bb.Status != StatusRolledBack || bb.PrevRevision != "api-aaaa" || !bb.CompletedAt.Equal(b(7)) {
		t.Fatalf("B rolled_back (prev A sürdü), completed_at=b7: %+v", out)
	}
	if len(out) != 1 {
		t.Fatalf("A için satır olmamalı: %+v", out)
	}
	if eb := find(recon(cfg, b(9), nil, acts), "api-bbbb"); eb == nil || eb.Status != StatusInProgress || eb.Note == "" {
		t.Fatalf("erken: in_progress + zayıf sinyal notu: %+v", eb)
	}
}

// Rollback çekilişten çok sonra gelirse de yakalanır (tek kova değil ileri yürüyüş).
func TestReconcile_LateTakeoverAfterWithdrawal(t *testing.T) {
	cfg := DefaultConfig()
	// A -10..0; B 1..4; tam sessizlik 5..12; A 13..30 döner
	acts := append(span("api-aaaa", -10, 0, 100), span("api-bbbb", 1, 4, 100)...)
	acts = append(acts, span("api-aaaa", 13, 30, 100)...)
	out := recon(cfg, b(31), nil, acts)
	bb := find(out, "api-bbbb")
	if bb == nil || bb.Status != StatusRolledBack || !bb.CompletedAt.Equal(b(5)) {
		t.Fatalf("B çekildi (b4), A sonra döndü → rolled_back, completed_at=b5: %+v", out)
	}
	// A: bilinen revizyon, yokluğunda B aktifti → yeni giriş (prev B, geri dönüş)
	aa := find(out, "api-aaaa")
	if aa == nil || !aa.StartedAt.Equal(b(13)) || aa.PrevRevision != "api-bbbb" || !containsNote(aa.Note, "geri dönüş") {
		t.Fatalf("A yeniden giriş (b13, prev B): %+v", out)
	}
}

func TestReconcile_RedeployOldRevision(t *testing.T) {
	cfg := DefaultConfig()
	prev := []Rollout{{ClusterID: "c1", Namespace: "pay", Workload: "api", Revision: "api-bbbb", StartedAt: b(-30), Status: StatusCompleted, CompletedAt: b(-28), PrevRevision: "api-aaaa", DetectedBy: "spans"}}
	acts := append(span("api-bbbb", -10, 6, 100), span("api-aaaa", 6, 20, 90)...)
	out := reconSeen(cfg, b(21), prev, acts, map[string]time.Time{"api-aaaa": b(-60)})
	aa := find(out, "api-aaaa")
	if aa == nil || !aa.StartedAt.Equal(b(6)) || aa.PrevRevision != "api-bbbb" || aa.Status != StatusCompleted || !aa.TrafficConfirmedAt.Equal(b(7)) || !containsNote(aa.Note, "geri dönüş") {
		t.Fatalf("A yeni giriş (b6, prev B, geri dönüş) ve completed: %+v", out)
	}
	if count(out, "api-bbbb") != 0 {
		t.Fatalf("B'nin kapalı satırı yeniden yazılmamalı: %+v", out)
	}
}

func TestReconcile_SupersededByThird(t *testing.T) {
	cfg := DefaultConfig()
	acts := append(span("api-aaaa", -10, 3, 100), span("api-bbbb", 3, 5, 100)...)
	acts = append(acts, span("api-cccc", 5, 20, 100)...)
	out := recon(cfg, b(21), nil, acts)
	bb := find(out, "api-bbbb")
	if bb == nil || bb.Status != StatusSuperseded || !containsNote(bb.Note, "api-cccc") || !bb.CompletedAt.Equal(b(6)) {
		t.Fatalf("B devralındı (C): %+v", out)
	}
	if cc := find(out, "api-cccc"); cc == nil || cc.Status != StatusCompleted || cc.PrevRevision != "api-bbbb" {
		t.Fatalf("C completed, prev B: %+v", out)
	}
}

func TestReconcile_CanaryOverlapNote(t *testing.T) {
	cfg := DefaultConfig()
	acts := append(span("api-aaaa", -10, 12, 100), span("api-bbbb", 3, 25, 100)...)
	if bb := find(recon(cfg, b(13), nil, acts), "api-bbbb"); bb == nil || bb.Status != StatusInProgress || !bb.CompletedAt.IsZero() || !containsNote(bb.Note, "durağan") {
		t.Fatalf("çakışma sürerken completed yazılmamalı, durağan not: %+v", bb)
	}
	bb := find(recon(cfg, b(20), nil, acts), "api-bbbb")
	if bb == nil || bb.Status != StatusCompleted || !bb.TrafficConfirmedAt.Equal(b(13)) || !containsNote(bb.Note, "çok-revizyonlu geçiş") {
		t.Fatalf("A kova 12'de son → trafik b13'te B; completed + overlap notu: %+v", bb)
	}
	// stabil (A) 15 dk dalarsa köprülenir; 35 dk dalarsa (arada B) A'nın dönüşü yeni giriş DEĞİL
	// çünkü B A'dan sonra girmişti? — model: bilinen revizyon + yokluğunda başkası aktif → giriş.
	dip := append(span("api-aaaa", -10, 8, 100), span("api-aaaa", 12, 25, 100)...)
	dip = append(dip, span("api-bbbb", 3, 25, 100)...)
	if r := find(recon(cfg, b(26), nil, dip), "api-bbbb"); r == nil || r.Status != StatusInProgress {
		t.Fatalf("stabilin 15 dk dalması B'yi tamamlamaz: %+v", r)
	}
	if count(recon(cfg, b(26), nil, dip), "api-aaaa") != 0 {
		t.Fatal("15 dk dalma A için satır açmaz")
	}
}

func TestReconcile_WeakSignalDoesNotSetStatus(t *testing.T) {
	cfg := DefaultConfig()
	acts := append(span("api-aaaa", -10, 8, 100), act("api-bbbb", 3, 50), act("api-bbbb", 4, 50))
	bb := find(recon(cfg, b(9), nil, acts), "api-bbbb")
	if bb == nil || bb.Status != StatusInProgress || bb.Note == "" {
		t.Fatalf("zayıf sinyal yalnız nota düşer: %+v", bb)
	}
	cfg.WeakSignal = false
	if r := find(recon(cfg, b(9), nil, acts), "api-bbbb"); r == nil || r.Note != "" {
		t.Fatalf("WeakSignal kapalı → not yok: %+v", r)
	}
}

func TestReconcile_UnmappedAndPartialBucketsSkipped(t *testing.T) {
	cfg := DefaultConfig()
	acts := append(span("api-aaaa", -10, 0, 100), Activity{Namespace: "pay", Workload: "api", Revision: "x-1", Bucket: b(1), Spans: 100})
	acts = append(acts, act("api-bbbb", 1, 100), act("api-bbbb", 2, 100), act("api-bbbb", 3, 100))
	out := recon(cfg, b(3).Add(time.Minute), nil, acts)
	if find(out, "x-1") != nil {
		t.Fatal("eşlenmemiş cluster satır üretmemeli")
	}
	if bb := find(out, "api-bbbb"); bb == nil || bb.SpanCount != 200 || !bb.StartedAt.Equal(b(1)) {
		t.Fatalf("yarım kova (b3) sayılmamalı: %+v", out)
	}
}

// Kayan pencere, deploy'suz tek revizyon — profiller: sabit, eşik-altı tek kova,
// 15 dk çukur, 35 dk çukur (gece), 2 saat kesinti, pencere kenarı çukurun içine
// denk gelir → HİÇ satır yok. Yerleşik revizyon 7 g ufkunda bilinir.
func TestReconcile_SlidingWindowSteadyRevision(t *testing.T) {
	cfg := DefaultConfig()
	lookback := 6 * time.Hour
	base := t0.Add(24 * time.Hour)
	gapAt := func(from, to int) func(int) int64 {
		return func(i int) int64 {
			if i >= from && i < to {
				return 0
			}
			return 100
		}
	}
	profiles := map[string]func(i int) int64{
		"sabit": func(int) int64 { return 100 },
		"tek kova eşik altı": func(i int) int64 {
			if i == 97 {
				return 3
			}
			return 100
		},
		"15 dk çukur":        gapAt(95, 98),
		"35 dk çukur (gece)": gapAt(95, 102),
		"2 saat kesinti":     gapAt(90, 114),
	}
	for name, prof := range profiles {
		var table []Rollout
		for tick := 0; tick < 600; tick++ { // 10 saat: pencere her çukuru içine alır ve geçer
			now := base.Add(time.Duration(tick) * time.Minute)
			winStart := AlignBucket(now.Add(-lookback), cfg.Bucket)
			lastFull := AlignBucket(now, cfg.Bucket).Add(-cfg.Bucket)
			var acts []Activity
			for bk := winStart; !bk.After(lastFull); bk = bk.Add(cfg.Bucket) {
				if s := prof(int(bk.Sub(base) / cfg.Bucket)); s > 0 {
					a := act("api-aaaa", 0, s)
					a.Bucket = bk
					acts = append(acts, a)
				}
			}
			seen := map[Key]map[string]time.Time{{"c1", "pay", "api"}: {"api-aaaa": base.Add(-48 * time.Hour)}}
			upsertInto(&table, Reconcile(cfg, Input{Now: now, WindowStart: winStart, Prev: table, Acts: acts, FirstSeen: seen}))
		}
		if len(table) != 0 {
			t.Fatalf("[%s] deploy'suz revizyon olay üretmemeli: %+v", name, table)
		}
	}
}

// Kayan pencere, canary (B %8) ile birlikte A'nın 15 dk çukuru; pencere kenarı
// çukurun içine denk gelince bile satır yok.
func TestReconcile_SlidingWindowCanaryDipAtEdge(t *testing.T) {
	cfg := DefaultConfig()
	lookback := 6 * time.Hour
	base := t0.Add(24 * time.Hour)
	var table []Rollout
	for tick := 0; tick < 600; tick++ {
		now := base.Add(time.Duration(tick) * time.Minute)
		winStart := AlignBucket(now.Add(-lookback), cfg.Bucket)
		lastFull := AlignBucket(now, cfg.Bucket).Add(-cfg.Bucket)
		var acts []Activity
		for bk := winStart; !bk.After(lastFull); bk = bk.Add(cfg.Bucket) {
			i := int(bk.Sub(base) / cfg.Bucket)
			if !(i >= 95 && i < 98) {
				a := act("api-aaaa", 0, 185)
				a.Bucket = bk
				acts = append(acts, a)
			}
			c := act("api-bbbb", 0, 15)
			c.Bucket = bk
			acts = append(acts, c)
		}
		seen := map[Key]map[string]time.Time{{"c1", "pay", "api"}: {"api-aaaa": base.Add(-48 * time.Hour), "api-bbbb": base.Add(-24 * time.Hour)}}
		upsertInto(&table, Reconcile(cfg, Input{Now: now, WindowStart: winStart, Prev: table, Acts: acts, FirstSeen: seen}))
	}
	if len(table) != 0 {
		t.Fatalf("canary + 15 dk çukur olay üretmemeli: %+v", table)
	}
}

func TestReconcile_SlidingWindowRealDeployStaysStable(t *testing.T) {
	cfg := DefaultConfig()
	lookback := 2 * time.Hour
	base := t0.Add(24 * time.Hour)
	deployAt := base.Add(30 * time.Minute)
	var table []Rollout
	for tick := 0; tick < 300; tick++ {
		now := base.Add(time.Duration(tick) * time.Minute)
		winStart := AlignBucket(now.Add(-lookback), cfg.Bucket)
		lastFull := AlignBucket(now, cfg.Bucket).Add(-cfg.Bucket)
		var acts []Activity
		for bk := winStart; !bk.After(lastFull); bk = bk.Add(cfg.Bucket) {
			if bk.Before(deployAt.Add(cfg.Bucket)) {
				a := act("api-aaaa", 0, 100)
				a.Bucket = bk
				acts = append(acts, a)
			}
			if !bk.Before(deployAt) {
				a := act("api-bbbb", 0, 100)
				a.Bucket = bk
				acts = append(acts, a)
			}
		}
		seen := map[Key]map[string]time.Time{{"c1", "pay", "api"}: {"api-aaaa": base.Add(-48 * time.Hour)}}
		upsertInto(&table, Reconcile(cfg, Input{Now: now, WindowStart: winStart, Prev: table, Acts: acts, FirstSeen: seen}))
	}
	if len(table) != 1 || table[0].Revision != "api-bbbb" || !table[0].StartedAt.Equal(deployAt) || table[0].Status != StatusCompleted || table[0].PrevRevision != "api-aaaa" {
		t.Fatalf("B tek completed satır, prev A: %+v", table)
	}
}

// Yeni iş yükünün ilk deploy'u (pencere ortasında beliren yeni revizyon) kaydedilir.
func TestReconcile_FirstDeployOfNewWorkload(t *testing.T) {
	cfg := DefaultConfig()
	out := recon(cfg, b(21), nil, span("api-v1", 4, 20, 100))
	if len(out) != 1 || !out[0].StartedAt.Equal(b(4)) || out[0].Status != StatusCompleted || out[0].PrevRevision != "" {
		t.Fatalf("ilk deploy tek satır (b4, completed, prev yok): %+v", out)
	}
	// aynı revizyon 7 g ufkunda biliniyorsa (gece sonrası scale-up) → satır yok
	if out := reconSeen(cfg, b(21), nil, span("api-v1", 4, 20, 100), map[string]time.Time{"api-v1": b(-200)}); len(out) != 0 {
		t.Fatalf("bilinen revizyonun dönüşü olay değil: %+v", out)
	}
}

func TestReconcile_DipsAndScaleToZeroAreContinuation(t *testing.T) {
	cfg := DefaultConfig()
	acts := append(span("api-aaaa", -10, 4, 100), span("api-bbbb", 3, 5, 80)...)
	acts = append(acts, span("api-bbbb", 9, 15, 80)...)
	out := recon(cfg, b(16), nil, acts)
	if count(out, "api-bbbb") != 1 || find(out, "api-bbbb").Status != StatusCompleted {
		t.Fatalf("çukur koşuyu bölmemeli: %+v", out)
	}
	acts = append(span("api-aaaa", -10, 4, 100), span("api-bbbb", 3, 5, 80)...)
	acts = append(acts, span("api-bbbb", 14, 20, 80)...)
	out = recon(cfg, b(21), nil, acts)
	if count(out, "api-bbbb") != 1 || !find(out, "api-bbbb").StartedAt.Equal(b(3)) {
		t.Fatalf("scale-to-zero dönüşü yeni satır açmamalı: %+v", out)
	}
	if again := recon(cfg, b(21), out, acts); len(again) != 0 {
		t.Fatalf("idempotans: %+v", again)
	}
}

func TestReconcile_RampUpIsNotEntry(t *testing.T) {
	cfg := DefaultConfig()
	if out := recon(cfg, b(16), nil, append(span("api-aaaa", 0, 6, 2), span("api-aaaa", 7, 15, 100)...)); len(out) != 0 {
		t.Fatalf("rampa satır üretmemeli: %+v", out)
	}
	acts := append(span("api-aaaa", -10, 3, 100), act("api-bbbb", 3, 4))
	acts = append(acts, span("api-bbbb", 4, 15, 100)...)
	if bb := find(recon(cfg, b(16), nil, acts), "api-bbbb"); bb == nil || !bb.StartedAt.Equal(b(3)) || bb.PrevRevision != "api-aaaa" {
		t.Fatalf("kısmi ilk kova girişi engellemez, öncü kova başa dahil: %+v", bb)
	}
}

func TestReconcile_InCallABA(t *testing.T) {
	cfg := DefaultConfig()
	acts := append(span("api-aaaa", -10, 3, 100), span("api-bbbb", 3, 10, 100)...)
	acts = append(acts, span("api-aaaa", 11, 30, 100)...)
	out := recon(cfg, b(31), nil, acts)
	bb := find(out, "api-bbbb")
	if bb == nil || bb.Status != StatusRolledBack || bb.PrevRevision != "api-aaaa" || !bb.CompletedAt.Equal(b(11)) {
		t.Fatalf("B çekildi, prev A sürdü → rolled_back: %+v", out)
	}
	aa := find(out, "api-aaaa")
	if aa == nil || !aa.StartedAt.Equal(b(11)) || aa.PrevRevision != "api-bbbb" || aa.Status != StatusCompleted || !containsNote(aa.Note, "geri dönüş") {
		t.Fatalf("A yeniden giriş satırı (b11, prev B, completed, geri dönüş): %+v", out)
	}
	if len(out) != 2 {
		t.Fatalf("iki satır: %+v", out)
	}
	if again := recon(cfg, b(31), out, acts); len(again) != 0 {
		t.Fatalf("idempotans: %+v", again)
	}
}

func TestReconcile_BackwardStartKeepsIdentity(t *testing.T) {
	cfg := DefaultConfig()
	prev := []Rollout{{ClusterID: "c1", Namespace: "pay", Workload: "api", Revision: "api-bbbb", StartedAt: b(4), Status: StatusInProgress, PrevRevision: "api-aaaa", DetectedBy: "spans", SpanCount: 300}}
	acts := append(span("api-aaaa", -10, 3, 100), span("api-bbbb", 3, 12, 100)...)
	out := recon(cfg, b(13), prev, acts)
	if count(out, "api-bbbb") != 1 || !find(out, "api-bbbb").StartedAt.Equal(b(4)) {
		t.Fatalf("started_at geri alınmaz, satır taşınır: %+v", out)
	}
	// ileri kayma: satır b3'te, koşu b5'te başlıyor (eşik ayarı değişti) → yine taşınır
	prev[0].StartedAt = b(3)
	acts = append(span("api-aaaa", -10, 3, 100), span("api-bbbb", 5, 12, 100)...)
	out = recon(cfg, b(13), prev, acts)
	if count(out, "api-bbbb") != 1 || !find(out, "api-bbbb").StartedAt.Equal(b(3)) {
		t.Fatalf("started_at ileri alınmaz: %+v", out)
	}
}

// Pencerede etkinliği olmayan revizyonun açık satırı: kim aktifse ona göre kapanır;
// bitiş pencere başı − 1 kova (uydurma damga yok) ve halefin girişinden önce.
func TestReconcile_StaleOpenRowsClose(t *testing.T) {
	cfg := DefaultConfig()
	prev := []Rollout{
		{ClusterID: "c1", Namespace: "pay", Workload: "api", Revision: "api-old1", StartedAt: b(-100), Status: StatusInProgress, PrevRevision: "api-aaaa", DetectedBy: "spans"},
		{ClusterID: "c1", Namespace: "pay", Workload: "api", Revision: "api-old2", StartedAt: b(-90), Status: StatusInProgress, PrevRevision: "api-zzzz", DetectedBy: "spans"},
		{ClusterID: "c1", Namespace: "pay", Workload: "api", Revision: "api-old3", StartedAt: b(-80), Status: StatusCompleted, CompletedAt: b(-79), DetectedBy: "spans"},
	}
	acts := span("api-aaaa", -10, 12, 100)
	out := recon(cfg, b(13), prev, acts)
	r1 := find(out, "api-old1")
	if r1 == nil || r1.Status != StatusRolledBack || !r1.CompletedAt.Equal(b(-10)) {
		t.Fatalf("prev'i (A) aktif → rolled_back, bitiş pencere başı: %+v", out)
	}
	if r := find(out, "api-old2"); r == nil || r.Status != StatusSuperseded || !containsNote(r.Note, "api-aaaa") {
		t.Fatalf("başkası (A) aktif → superseded: %+v", out)
	}
	if find(out, "api-old3") != nil || len(out) != 2 {
		t.Fatalf("kapalı satır dokunulmaz: %+v", out)
	}
	if again := recon(cfg, b(13), append(prev, out...), acts); len(again) != 0 {
		t.Fatalf("idempotans: %+v", again)
	}
	// etkinliği hiç olmayan iş yükü: açık satıra yalnız not, durum değişmez
	silent := []Rollout{{ClusterID: "c9", Namespace: "x", Workload: "dead", Revision: "d-1", StartedAt: b(-50), Status: StatusInProgress, DetectedBy: "spans"}}
	out = recon(cfg, b(13), silent, nil)
	if len(out) != 1 || out[0].Status != StatusInProgress || out[0].Note == "" {
		t.Fatalf("sessiz iş yükü: yalnız not: %+v", out)
	}
	if again := recon(cfg, b(13), out, nil); len(again) != 0 {
		t.Fatalf("not idempotent: %+v", again)
	}
}

func TestReconcile_SameRevisionSuccessor(t *testing.T) {
	cfg := DefaultConfig()
	prev := []Rollout{
		{ClusterID: "c1", Namespace: "pay", Workload: "api", Revision: "api-aaaa", StartedAt: b(-50), Status: StatusInProgress, DetectedBy: "spans"},
		{ClusterID: "c1", Namespace: "pay", Workload: "api", Revision: "api-aaaa", StartedAt: b(-10), Status: StatusCompleted, CompletedAt: b(-9), DetectedBy: "spans"},
	}
	out := recon(cfg, b(13), prev, span("api-aaaa", -10, 12, 100))
	if len(out) != 1 || out[0].Status != StatusSuperseded || !out[0].StartedAt.Equal(b(-50)) {
		t.Fatalf("eski açık satır devralındı: %+v", out)
	}
}

// Pencere kenarına 30 dk'dan yakın giriş (kesinti sonrası) BİLİNÇLİ olarak kaydedilmez;
// kenardan ≥ 6 kova uzak giriş kaydedilir.
func TestReconcile_EntryNeedsObservedAbsence(t *testing.T) {
	cfg := DefaultConfig()
	acts := append(span("api-aaaa", -10, 20, 100), span("api-bbbb", -8, 20, 100)...) // B kenardan 2 kova sonra
	if out := recon(cfg, b(21), nil, acts); count(out, "api-bbbb") != 0 {
		t.Fatalf("kenara yakın giriş kanıtsız → satır yok: %+v", out)
	}
	acts = append(span("api-aaaa", -10, 20, 100), span("api-bbbb", -4, 20, 100)...) // 6 kova yokluk gözlendi
	if out := recon(cfg, b(21), nil, acts); count(out, "api-bbbb") != 1 {
		t.Fatalf("≥ 6 kova gözlenmiş yokluk → giriş: %+v", out)
	}
}

func TestAlignBucketEpochGrid(t *testing.T) {
	tt := time.Date(2026, 8, 30, 10, 7, 30, 0, time.UTC)
	if got := AlignBucket(tt, 5*time.Minute); !got.Equal(time.Date(2026, 8, 30, 10, 5, 0, 0, time.UTC)) {
		t.Fatalf("5m: %v", got)
	}
	loc := time.FixedZone("x", 3*3600)
	if got := AlignBucket(tt.In(loc), 15*time.Minute); !got.Equal(time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("15m: %v", got)
	}
	if got := AlignBucket(time.Unix(-90, 0), time.Minute); got.Unix() != -120 {
		t.Fatalf("negatif epoch: %v", got.Unix())
	}
	for _, d := range []time.Duration{time.Minute, 10 * time.Minute, 30 * time.Minute} {
		if a := AlignBucket(tt, d); a.Unix()%int64(d/time.Second) != 0 || a.After(tt) {
			t.Fatalf("%v hizası: %v", d, a)
		}
	}
	if got := AlignBucket(tt, 0); !got.Equal(tt) {
		t.Fatal("0 kova → dokunma")
	}
}

// ── 4. tur inceleme regresyonları ──────────────────────────────────────

// Eşik altına inen ama span üreten revizyon ÇEKİLMİŞ sayılmaz (varlık ≠ etkinlik).
func TestReconcile_SubThresholdTroughIsNotWithdrawal(t *testing.T) {
	cfg := DefaultConfig()
	// A yerleşik 1000; canary B 87 (b3'te girdi); b20-b27 çukur: A 120, B 9 (< eşik ama canlı)
	var acts []Activity
	for i := -10; i <= 40; i++ {
		a := act("api-aaaa", i, 1000)
		if i >= 20 && i <= 27 {
			a.Spans = 120
		}
		acts = append(acts, a)
	}
	for i := 3; i <= 40; i++ {
		c := act("api-bbbb", i, 87)
		if i >= 20 && i <= 27 {
			c.Spans = 9
		}
		acts = append(acts, c)
	}
	seen := map[string]time.Time{"api-aaaa": b(-500), "api-bbbb": b(3)}
	var table []Rollout
	for now := 5; now <= 41; now++ {
		upsertInto(&table, reconSeen(cfg, b(now), table, acts, seen))
	}
	if count(table, "api-bbbb") != 1 || table[0].Status != StatusInProgress || table[0].Status == StatusRolledBack {
		t.Fatalf("canlı çukur rollback değil, tek açık satır: %+v", table)
	}
	if count(table, "api-aaaa") != 0 {
		t.Fatalf("A için satır yok: %+v", table)
	}
}

// MV geçmişi pencere içinde başlıyorsa (ilk etkinleştirme) yerleşik revizyonlar olay üretmez.
func TestReconcile_BootstrapDataStartSuppressesEntries(t *testing.T) {
	cfg := DefaultConfig()
	acts := span("api-v1", 4, 30, 900) // veri b4'te başlıyor, pencere b(-10)'dan
	in := Input{Now: b(31), WindowStart: b(-10), Acts: acts,
		FirstSeen: map[Key]map[string]time.Time{{"c1", "pay", "api"}: {"api-v1": b(4)}},
		DataStart: map[string]time.Time{"c1": b(4)}}
	if out := Reconcile(cfg, in); len(out) != 0 {
		t.Fatalf("küme verisi koşuyla başlıyor → yokluk gözlenmedi → satır yok: %+v", out)
	}
	// küme verisi ≥ EH kova önce başladıysa yeni iş yükünün ilk deploy'u kaydedilir
	in.DataStart = map[string]time.Time{"c1": b(-10)}
	if out := Reconcile(cfg, in); len(out) != 1 {
		t.Fatalf("yerleşik kümede yeni iş yükü → giriş: %+v", out)
	}
}

// Canary (%8) yanında yerleşik revizyonun 35 dk span'siz dalması olay değil (devralma testi).
func TestReconcile_IncumbentDipBesideFlatCanaryIsNotEntry(t *testing.T) {
	cfg := DefaultConfig()
	var acts []Activity
	for i := -10; i <= 40; i++ {
		if i >= 20 && i <= 26 { // 7 kova span'siz
			continue
		}
		acts = append(acts, act("api-aaaa", i, 1000))
	}
	acts = append(acts, span("api-bbbb", -10, 40, 87)...)
	seen := map[string]time.Time{"api-aaaa": b(-500), "api-bbbb": b(-300)}
	if out := reconSeen(cfg, b(41), nil, acts, seen); len(out) != 0 {
		t.Fatalf("düz canary yerleşik revizyonu devralamaz → satır yok: %+v", out)
	}
	// ama gerçek devralma (B 1000'e çıktı) → A'nın dönüşü giriş (prev B)
	acts = nil
	for i := -10; i <= 40; i++ {
		if i >= 20 && i <= 26 {
			continue
		}
		acts = append(acts, act("api-aaaa", i, 1000))
	}
	for i := -10; i <= 40; i++ {
		s := int64(87)
		if i >= 20 && i <= 26 {
			s = 1000
		}
		acts = append(acts, act("api-bbbb", i, s))
	}
	if aa := find(reconSeen(cfg, b(41), nil, acts, seen), "api-aaaa"); aa == nil || aa.PrevRevision != "api-bbbb" || !aa.StartedAt.Equal(b(27)) {
		t.Fatalf("gerçek devralma sonrası dönüş giriş: %+v", aa)
	}
}

// Pencere kenarı çukurun içine denk gelince mevcut satır taşınır, yeniden basılmaz.
func TestReconcile_EdgeInsideDipCarriesRow(t *testing.T) {
	cfg := DefaultConfig()
	prev := []Rollout{{ClusterID: "c1", Namespace: "pay", Workload: "api", Revision: "api-aaaa", StartedAt: b(-50), Status: StatusInProgress, DetectedBy: "spans"}}
	acts := append(span("api-aaaa", 2, 20, 185), span("api-bbbb", 0, 20, 15)...)
	seen := map[Key]map[string]time.Time{{"c1", "pay", "api"}: {"api-aaaa": b(0).Add(-48 * time.Hour), "api-bbbb": b(0).Add(-24 * time.Hour)}}
	out := Reconcile(cfg, Input{Now: b(21), WindowStart: b(0), Prev: prev, Acts: acts, FirstSeen: seen})
	if count(out, "api-aaaa") > 1 {
		t.Fatalf("kenar çukuru yeni satır basmamalı: %+v", out)
	}
	if r := find(out, "api-aaaa"); r != nil && (!r.StartedAt.Equal(b(-50)) || r.Status != StatusInProgress) {
		t.Fatalf("mevcut satır taşınır: %+v", r)
	}
}

// Etkinlik okuması kesikse etkinliği görünmeyen iş yüküne "trafik yok" notu düşmez.
func TestReconcile_TruncatedLeavesPrevOnlyKeysAlone(t *testing.T) {
	cfg := DefaultConfig()
	prev := []Rollout{{ClusterID: "c9", Namespace: "x", Workload: "cut", Revision: "d-1", StartedAt: b(-50), Status: StatusInProgress, DetectedBy: "spans"}}
	if out := Reconcile(cfg, Input{Now: b(13), WindowStart: b(-10), Prev: prev, Truncated: true}); len(out) != 0 {
		t.Fatalf("kesik okumada yokluk kanıt değil: %+v", out)
	}
	if out := Reconcile(cfg, Input{Now: b(13), WindowStart: b(-10), Prev: prev}); len(out) != 1 {
		t.Fatalf("tam okumada not düşer: %+v", out)
	}
}

// Önceki revizyon pencere dışında çekildiyse (reconciler kesintisi) overlap uydurulmaz.
func TestReconcile_UnobservedWithdrawalNote(t *testing.T) {
	cfg := DefaultConfig()
	prev := []Rollout{{ClusterID: "c1", Namespace: "pay", Workload: "api", Revision: "api-bbbb", StartedAt: b(-100), Status: StatusInProgress, PrevRevision: "api-aaaa", DetectedBy: "spans"}}
	out := reconSeen(cfg, b(13), prev, span("api-bbbb", -10, 12, 100), map[string]time.Time{"api-bbbb": b(-100)})
	bb := find(out, "api-bbbb")
	if bb == nil || bb.Status != StatusCompleted || !containsNote(bb.Note, "pencere dışında") || containsNote(bb.Note, "çok-revizyonlu") {
		t.Fatalf("completed + üst-sınır notu, overlap notu yok: %+v", out)
	}
}

// "trafik üretmiyor" notu geçicidir: revizyon dönünce kalkar.
func TestReconcile_NoTrafficNoteIsTransient(t *testing.T) {
	cfg := DefaultConfig()
	prev := []Rollout{{ClusterID: "c1", Namespace: "pay", Workload: "api", Revision: "api-bbbb", StartedAt: b(-2), Status: StatusInProgress, PrevRevision: "api-aaaa", DetectedBy: "spans", Note: noteNoTraffic}}
	acts := append(span("api-aaaa", -10, -3, 100), span("api-bbbb", -2, 12, 100)...)
	bb := find(recon(cfg, b(13), prev, acts), "api-bbbb")
	if bb == nil || containsNote(bb.Note, noteNoTraffic) || bb.Status != StatusCompleted {
		t.Fatalf("not kalkmalı, tamamlanmalı: %+v", bb)
	}
	if got := stripNote("a; "+noteNoTraffic+"; b", noteNoTraffic); got != "a; b" {
		t.Fatalf("stripNote orta: %q", got)
	}
	if got := stripNote(noteNoTraffic+"; b", noteNoTraffic); got != "b" {
		t.Fatalf("stripNote baş: %q", got)
	}
}

// ── 5. tur inceleme regresyonları ──────────────────────────────────────

// Bayat-satır kapatıcısı: kurulmuş koşusu olmayan ama span üreten revizyon çekilmiş DEĞİLDİR.
func TestReconcile_StaleCloserRespectsAliveRevision(t *testing.T) {
	cfg := DefaultConfig()
	prev := []Rollout{{ClusterID: "c1", Namespace: "pay", Workload: "api", Revision: "api-bbbb", StartedAt: b(-2), Status: StatusInProgress, PrevRevision: "api-aaaa", DetectedBy: "spans"}}
	// B eşik altında (6 span) ama sürekli canlı; C aktif
	acts := append(span("api-bbbb", -2, 12, 6), span("api-cccc", 0, 12, 100)...)
	out := recon(cfg, b(13), prev, acts)
	if r := find(out, "api-bbbb"); r != nil && r.Status != StatusInProgress {
		t.Fatalf("canlı (eşik altı) revizyon çekilmiş sayılmaz: %+v", out)
	}
	// 1 s saat kayması: B'nin tek kovası henüz tam değil → yine terminal karar yok
	acts = append(span("api-aaaa", -10, -1, 100), span("api-bbbb", 0, 2, 100)...)
	prev = []Rollout{{ClusterID: "c1", Namespace: "pay", Workload: "api", Revision: "api-bbbb", StartedAt: b(0), Status: StatusInProgress, PrevRevision: "api-aaaa", DetectedBy: "spans"}}
	if out := recon(cfg, b(2).Add(-time.Second), prev, acts); find(out, "api-bbbb") != nil && find(out, "api-bbbb").Status == StatusRolledBack {
		t.Fatalf("saat kayması sahte rollback üretmemeli: %+v", out)
	}
}

// Aynı revizyonun ikinci koşusu bayt-aynı satırı yeniden basmaz (her tik upsert/tail yok).
func TestReconcile_NoChurnOnSecondRun(t *testing.T) {
	cfg := DefaultConfig()
	// A yerleşik; B canary b3-b10; 2 saat ortak sessizlik yok — B 24 kova sustu, sonra A ve B döndü
	acts := append(span("api-aaaa", -10, 10, 1000), span("api-bbbb", 3, 10, 87)...)
	acts = append(acts, span("api-aaaa", 35, 50, 1000)...)
	acts = append(acts, span("api-bbbb", 35, 50, 87)...)
	seen := map[string]time.Time{"api-aaaa": b(-500), "api-bbbb": b(3)}
	var table []Rollout
	for now := 11; now <= 51; now++ {
		upsertInto(&table, reconSeen(cfg, b(now), table, acts, seen))
	}
	// aynı kova içindeki tikler (1 dk adım, lastFull sabit): tablo geri beslenince
	// hiçbir tik değişiklik üretmemeli — ikinci koşu çağrı-içi kopyaya karşı
	// diff alıp bayt-aynı satırı yeniden basıyordu
	for k := 1; k <= 4; k++ {
		if out := reconSeen(cfg, b(51).Add(time.Duration(k)*time.Minute), table, acts, seen); len(out) != 0 {
			t.Fatalf("sabit kovada satır yeniden basıldı (+%d dk): %+v", k, out)
		}
	}
	// B için tek satır (iki koşu tek kimlik altında)
	if count(table, "api-bbbb") != 1 {
		t.Fatalf("B tek satır: %+v", table)
	}
}

// Ortak sessizlik + yerleşiğin bir kova erken dönmesi canary'ye sahte rollback yazmaz.
func TestReconcile_SharedSilenceStaggeredReturn(t *testing.T) {
	cfg := DefaultConfig()
	acts := append(span("api-aaaa", -10, 10, 1000), span("api-bbbb", 3, 10, 87)...) // birlikte aktif
	acts = append(acts, span("api-aaaa", 17, 30, 1000)...)                          // 6 kova ortak sessizlik, A bir kova erken
	acts = append(acts, span("api-bbbb", 18, 30, 87)...)
	seen := map[string]time.Time{"api-aaaa": b(-500), "api-bbbb": b(3)}
	var table []Rollout
	for now := 11; now <= 31; now++ {
		upsertInto(&table, reconSeen(cfg, b(now), table, acts, seen))
	}
	for _, r := range table {
		if r.Revision == "api-bbbb" && r.Status == StatusRolledBack {
			t.Fatalf("ortak sessizlik rollback değil: %+v", table)
		}
	}
}

// Eşik-altı öncü EH'den uzun olsa da (blue/green preview) prev varsa giriş kaydedilir.
func TestReconcile_LongSubThresholdLeadWithPrev(t *testing.T) {
	cfg := DefaultConfig()
	for _, hold := range []int{7, 24} {
		acts := append(span("api-aaaa", -10, 30+hold, 150), span("api-bbbb", 20, 20+hold-1, 5)...) // preview
		acts = append(acts, span("api-bbbb", 20+hold, 40+hold, 150)...)                            // cutover
		// A çekilir
		acts2 := acts[:0]
		for _, a := range acts {
			if a.Revision == "api-aaaa" && a.Bucket.After(b(20+hold)) {
				continue
			}
			acts2 = append(acts2, a)
		}
		out := reconSeen(cfg, b(41+hold), nil, acts2, map[string]time.Time{"api-aaaa": b(-500)})
		bb := find(out, "api-bbbb")
		if bb == nil || bb.PrevRevision != "api-aaaa" || !bb.StartedAt.Equal(b(20)) || bb.Status != StatusCompleted {
			t.Fatalf("hold=%d: preview'lı deploy kaydedilmeli (started_at öncünün başı, prev A): %+v", hold, out)
		}
	}
	// prev yok + öncü > H → rampa (satır yok)
	acts := append(span("api-aaaa", 0, 6, 2), span("api-aaaa", 7, 15, 100)...)
	if out := recon(cfg, b(16), nil, acts); len(out) != 0 {
		t.Fatalf("rampa satır üretmemeli: %+v", out)
	}
}

// Zayıf sinyal notu canlı-eşik-altı ile yok'u ayırmaz ama iddiası doğru olmalı.
func TestReconcile_WeakSignalWording(t *testing.T) {
	cfg := DefaultConfig()
	acts := append(span("api-aaaa", -10, 8, 100), span("api-bbbb", 3, 7, 50)...)
	acts = append(acts, act("api-bbbb", 8, 4))
	bb := find(recon(cfg, b(9), nil, acts), "api-bbbb")
	if bb == nil || bb.Status != StatusInProgress || !containsNote(bb.Note, "eşik altında") {
		t.Fatalf("eşik altı canlı → 'eşik altında kaldı' notu: %+v", bb)
	}
}

// Aynı-revizyon geçişi tikin taze kopyasını ezmemeli (known() dedupe).
func TestReconcile_FreshCopyBeatsStaleTableRow(t *testing.T) {
	cfg := DefaultConfig()
	prev := []Rollout{{ClusterID: "c1", Namespace: "pay", Workload: "api", Revision: "api-aaaa", StartedAt: b(0), Status: StatusInProgress, PrevRevision: "api-bbbb", DetectedBy: "spans", SpanCount: 7, Image: "BAYAT"}}
	acts := append(span("api-bbbb", -10, -1, 100), span("api-aaaa", 0, 3, 100)...)
	acts = append(acts, span("api-bbbb", 4, 10, 100)...)
	acts = append(acts, span("api-aaaa", 20, 25, 100)...)
	out := recon(cfg, b(26), prev, acts)
	var first *Rollout
	for i := range out {
		if out[i].Revision == "api-aaaa" && out[i].StartedAt.Equal(b(0)) {
			first = &out[i]
		}
	}
	if first == nil || first.Status != StatusRolledBack || first.Image == "BAYAT" || first.SpanCount != 400 {
		t.Fatalf("b0 satırı tikin taze kararını taşımalı (rolled_back, imaj/spans güncel): %+v", out)
	}
}

// ── 6. tur inceleme regresyonları ──────────────────────────────────────

// Ingest kesintisi (tüm revizyonlar sustu) + bir kova farklı dönüş: giriş satırı yok.
func TestReconcile_IngestOutageStaggeredReturnNoEntry(t *testing.T) {
	cfg := DefaultConfig()
	seen := map[string]time.Time{"api-aaaa": b(-500), "api-bbbb": b(3)}
	for _, order := range []struct{ aBack, bBack int }{{30, 31}, {31, 30}} {
		acts := append(span("api-aaaa", -10, 20, 1000), span("api-bbbb", 3, 20, 87)...)
		acts = append(acts, span("api-aaaa", order.aBack, 45, 1000)...)
		acts = append(acts, span("api-bbbb", order.bBack, 45, 87)...)
		var table []Rollout
		for now := 21; now <= 46; now++ {
			upsertInto(&table, reconSeen(cfg, b(now), table, acts, seen))
		}
		for _, r := range table {
			if r.StartedAt.After(b(21)) || r.Status == StatusSuperseded || r.Status == StatusRolledBack {
				t.Fatalf("kesinti sonrası sahte olay (%+v): %+v", order, table)
			}
		}
		if count(table, "api-bbbb") > 1 || count(table, "api-aaaa") != 0 {
			t.Fatalf("B tek satır, A satırsız (%+v): %+v", order, table)
		}
	}
}

// Rampa-aşağı sonrası 35 dk sessizlik: düz canary devralmış sayılmaz (taban = seviye).
func TestReconcile_DrainThenDipBesideCanaryIsNotEntry(t *testing.T) {
	cfg := DefaultConfig()
	seen := map[string]time.Time{"api-aaaa": b(-500), "api-bbbb": b(-300)}
	for _, canary := range []int64{15, 87, 200} {
		var acts []Activity
		for i := -10; i <= 45; i++ {
			switch {
			case i >= 20 && i <= 26: // 7 kova span'siz
			case i >= 17 && i <= 19: // rampa-aşağı
				acts = append(acts, act("api-aaaa", i, 25))
			default:
				acts = append(acts, act("api-aaaa", i, 1000))
			}
		}
		acts = append(acts, span("api-bbbb", -10, 45, canary)...)
		if out := reconSeen(cfg, b(46), nil, acts, seen); len(out) != 0 {
			t.Fatalf("canary=%d: rampa-aşağı + dalma olay değil: %+v", canary, out)
		}
	}
	// gerçek devralma: B yoklukta 1000'e çıkar → A'nın dönüşü giriş
	var acts []Activity
	for i := -10; i <= 45; i++ {
		if i >= 20 && i <= 26 {
			continue
		}
		s := int64(1000)
		if i >= 17 && i <= 19 {
			s = 25
		}
		acts = append(acts, act("api-aaaa", i, s))
	}
	for i := -10; i <= 45; i++ {
		s := int64(87)
		if i >= 20 && i <= 26 {
			s = 1000
		}
		acts = append(acts, act("api-bbbb", i, s))
	}
	if aa := find(reconSeen(cfg, b(46), nil, acts, seen), "api-aaaa"); aa == nil || aa.PrevRevision != "api-bbbb" {
		t.Fatalf("gerçek devralma sonrası dönüş giriş: %+v", aa)
	}
}

// Ortak sessizlikten yalnız önceki revizyon dönerse rollback bir lookback ertelenmez.
func TestReconcile_SharedSilenceThenOnlyPrevReturns(t *testing.T) {
	cfg := DefaultConfig()
	acts := append(span("api-aaaa", -10, 10, 1000), span("api-bbbb", 3, 10, 900)...)
	acts = append(acts, span("api-aaaa", 18, 40, 1000)...) // 7 kova ortak sessizlik, yalnız A döner
	seen := map[string]time.Time{"api-aaaa": b(-500), "api-bbbb": b(3)}
	var table []Rollout
	for now := 11; now <= 41; now++ {
		upsertInto(&table, reconSeen(cfg, b(now), table, acts, seen))
	}
	bb := find(table, "api-bbbb")
	if bb == nil || bb.Status != StatusRolledBack || !bb.CompletedAt.Equal(b(11)) || containsNote(bb.Note, noteNoTraffic) {
		t.Fatalf("A ≥ EH kova tek başına aktif → B rolled_back, completed_at=b11, bayat not yok: %+v", table)
	}
}

// ── 7. tur inceleme regresyonları ──────────────────────────────────────

// Ortak kesinti sonrası 2..6 kovalık kayma da olay değil; ≥ EH tek başına aktiflik olaydır.
func TestReconcile_OutageStaggerUpToEH(t *testing.T) {
	cfg := DefaultConfig()
	seen := map[string]time.Time{"api-aaaa": b(-500), "api-bbbb": b(3)}
	for stagger := 2; stagger <= 6; stagger++ {
		acts := append(span("api-aaaa", -10, 20, 1000), span("api-bbbb", 3, 20, 500)...)
		acts = append(acts, span("api-aaaa", 29, 60, 1000)...)
		acts = append(acts, span("api-bbbb", 29+stagger, 60, 500)...)
		var table []Rollout
		for now := 21; now <= 61; now++ {
			upsertInto(&table, reconSeen(cfg, b(now), table, acts, seen))
		}
		for _, r := range table {
			if r.StartedAt.After(b(21)) || r.Status == StatusSuperseded || r.Status == StatusRolledBack {
				t.Fatalf("kayma=%d: sahte olay: %+v", stagger, table)
			}
		}
	}
	// kayma 7 (> EH): A ≥ EH kova tek başına → B'nin satırı rolled_back + B'nin dönüşü giriş
	acts := append(span("api-aaaa", -10, 20, 1000), span("api-bbbb", 3, 20, 500)...)
	acts = append(acts, span("api-aaaa", 29, 60, 1000)...)
	acts = append(acts, span("api-bbbb", 36, 60, 500)...)
	var table []Rollout
	for now := 21; now <= 61; now++ {
		upsertInto(&table, reconSeen(cfg, b(now), table, acts, seen))
	}
	if r := find(table, "api-bbbb"); r == nil || r.Status != StatusRolledBack {
		t.Fatalf("kayma 7: B rolled_back bekleniyor: %+v", table)
	}
}

// Ortak kesinti + pencere kayması: B'nin kesinti-öncesi son aktif kovası pencereden çıkınca da olay yok.
func TestReconcile_OutageStaggerSurvivesWindowSlide(t *testing.T) {
	cfg := DefaultConfig()
	lookback := 6 * time.Hour
	base := t0.Add(24 * time.Hour)
	var table []Rollout
	out := func(i int) bool { return i >= 40 && i <= 47 } // 8 kova ortak kesinti
	for tick := 0; tick < 900; tick++ {
		now := base.Add(time.Duration(tick) * time.Minute)
		winStart := AlignBucket(now.Add(-lookback), cfg.Bucket)
		lastFull := AlignBucket(now, cfg.Bucket).Add(-cfg.Bucket)
		var acts []Activity
		for bk := winStart; !bk.After(lastFull); bk = bk.Add(cfg.Bucket) {
			i := int(bk.Sub(base) / cfg.Bucket)
			if !out(i) {
				a := act("api-aaaa", 0, 1000)
				a.Bucket = bk
				acts = append(acts, a)
			}
			if !out(i) && i != 48 { // B bir kova geç döner
				c := act("api-bbbb", 0, 500)
				c.Bucket = bk
				acts = append(acts, c)
			}
		}
		seen := map[Key]map[string]time.Time{{"c1", "pay", "api"}: {"api-aaaa": base.Add(-48 * time.Hour), "api-bbbb": base.Add(-24 * time.Hour)}}
		upsertInto(&table, Reconcile(cfg, Input{Now: now, WindowStart: winStart, Prev: table, Acts: acts, FirstSeen: seen}))
	}
	if len(table) != 0 {
		t.Fatalf("kesinti + kayan pencere olay üretmemeli: %+v", table)
	}
}

// Ertelenen aday aktif kalmazsa kabul edilmez; gerçek halef görülür.
func TestReconcile_DeferredCandidateMustStayActive(t *testing.T) {
	cfg := DefaultConfig()
	seen := map[string]time.Time{"api-aaaa": b(-500), "api-bbbb": b(3)}
	// A ve B birlikte; ortak sessizlik; A tek kova parıldar (b18), sonra sessizlik
	acts := append(span("api-aaaa", -10, 10, 1000), span("api-bbbb", 3, 10, 900)...)
	acts = append(acts, act("api-aaaa", 18, 1000))
	var table []Rollout
	for now := 11; now <= 42; now++ {
		upsertInto(&table, reconSeen(cfg, b(now), table, acts, seen))
	}
	if r := find(table, "api-bbbb"); r == nil || r.Status != StatusInProgress || !containsNote(r.Note, noteNoTraffic) {
		t.Fatalf("parıltı devralma değil: in_progress + trafik-yok notu: %+v", table)
	}
	// aynı ama C gerçek halef olarak b20'de gelir → superseded: C
	acts = append(acts, span("api-cccc", 20, 40, 1000)...)
	table = nil
	for now := 11; now <= 42; now++ {
		upsertInto(&table, reconSeen(cfg, b(now), table, acts, seen))
	}
	if r := find(table, "api-bbbb"); r == nil || r.Status != StatusSuperseded || !containsNote(r.Note, "api-cccc") {
		t.Fatalf("gerçek halef C görülmeli: %+v", table)
	}
}

// Taban gözlenmediğinde (pencere kenarı) dönen koşunun seviyesi kullanılır; rampa ilk kova düz canary'yi devralmış göstermez.
func TestReconcile_UnobservedBaseUsesRunLevel(t *testing.T) {
	cfg := DefaultConfig()
	seen := map[Key]map[string]time.Time{{"c1", "pay", "api"}: {"api-aaaa": b(-500), "api-bbbb": b(-300)}}
	for _, ws := range []int{-40, -4, -3, -2, 1} {
		acts := append(span("api-aaaa", -10, 0, 1000), act("api-aaaa", 7, 30))
		acts = append(acts, span("api-aaaa", 8, 30, 1000)...)
		acts = append(acts, span("api-bbbb", -10, 30, 87)...)
		out := Reconcile(cfg, Input{Now: b(31), WindowStart: b(ws), Acts: acts, FirstSeen: seen})
		if len(out) != 0 {
			t.Fatalf("ws=%d: düz canary devralamaz: %+v", ws, out)
		}
	}
}
