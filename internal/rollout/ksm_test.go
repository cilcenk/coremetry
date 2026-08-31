package rollout

import (
	"strings"
	"testing"
	"time"
)

// v0.10.212 — Faz 5 dilim 1 sözleşmeleri (audit §7 + §16):
//
//	JoinKSM: owner yoksa ya da (spec|ready) yoksa nil (aile kapalı = hata
//	değil); tavan aşımı hata; sahipsiz RS bağlanmaz. applyKSM: hazır damgası
//	bir kez; ready<istenen dayanıklı sayaç + StalledMin sonrası stalled;
//	iyileşme sayaç+durumu geri alır; spec==0 sayaç başlatmaz; stalled yalnız
//	KSM'den (span sessizliği asla — audit madde 7).
func ksmS(ns, rs string, v float64) KSMSample {
	return KSMSample{Labels: map[string]string{"namespace": ns, "replicaset": rs}, Value: v}
}

func TestJoinKSM(t *testing.T) {
	owner := KSMSample{Labels: map[string]string{"namespace": "demo", "replicaset": "api-abc", "owner_name": "api"}, Value: 1}
	if m, err := JoinKSM("c1", map[string][]KSMSample{"rs_spec": {ksmS("demo", "api-abc", 3)}}); m != nil || err != nil {
		t.Fatalf("owner yokken nil/nil beklenir: %v %v", m, err)
	}
	if m, err := JoinKSM("c1", map[string][]KSMSample{"rs_owner": {owner}}); m != nil || err != nil {
		t.Fatalf("spec+ready yokken nil/nil beklenir: %v %v", m, err)
	}
	big := make([]KSMSample, ksmMaxSeries+1)
	for i := range big {
		big[i] = owner
	}
	if _, err := JoinKSM("c1", map[string][]KSMSample{"rs_owner": big, "rs_spec": {ksmS("demo", "api-abc", 3)}}); err == nil {
		t.Fatal("tavan aşımı hata olmalı")
	}
	m, err := JoinKSM("c1", map[string][]KSMSample{
		"rs_owner":   {owner},
		"rs_spec":    {ksmS("demo", "api-abc", 3), ksmS("demo", "orphan-xyz", 2)},
		"rs_ready":   {ksmS("demo", "api-abc", 2)},
		"rs_created": {ksmS("demo", "api-abc", 1756600000)},
	})
	if err != nil {
		t.Fatal(err)
	}
	k := Key{ClusterID: "c1", Namespace: "demo", Workload: "api"}
	kr, ok := m[k]["api-abc"]
	if !ok || kr.Spec != 3 || kr.Ready != 2 || kr.CreatedAt.IsZero() {
		t.Fatalf("join eksik: %+v", m)
	}
	for key := range m {
		if key.Workload == "orphan-xyz" || key.Workload == "" {
			t.Fatalf("sahipsiz RS bağlanmamalı: %+v", m)
		}
	}
}

func TestApplyKSM(t *testing.T) {
	cfg := DefaultConfig()
	cfg.StalledMin = 10 * time.Minute
	at := b(10)
	row := Rollout{Status: StatusInProgress, DetectedBy: "spans"}
	// hazır: damga + kaynak yükseltmesi
	applyKSM(cfg, at, &row, KSMRev{Spec: 3, Ready: 3, CreatedAt: b(1)})
	if row.PodsReadyAt != at || row.DetectedBy != "spans+ksm" || row.KSMStartedAt != b(1) {
		t.Fatalf("hazır damgası: %+v", row)
	}
	// ikinci hazır tik damgayı OYNATMAZ
	applyKSM(cfg, b(11), &row, KSMRev{Spec: 3, Ready: 3})
	if row.PodsReadyAt != at {
		t.Fatalf("pods_ready_at monoton kalmalı: %+v", row)
	}
	// ready<istenen: sayaç başlar, StalledMin dolmadan stalled YOK
	row2 := Rollout{Status: StatusInProgress}
	applyKSM(cfg, b(10), &row2, KSMRev{Spec: 3, Ready: 1})
	if row2.KSMNotReadySince != b(10) || row2.Status != StatusInProgress {
		t.Fatalf("sayaç başlamalı, stalled erken gelmemeli: %+v", row2)
	}
	// StalledMin (10m = 2 kova) dolunca stalled + not
	applyKSM(cfg, b(12), &row2, KSMRev{Spec: 3, Ready: 1})
	if row2.Status != StatusStalled {
		t.Fatalf("stalled bekleniyordu: %+v", row2)
	}
	// iyileşme: durum geri, sayaç sıfır, hazır damgası
	applyKSM(cfg, b(13), &row2, KSMRev{Spec: 3, Ready: 3})
	if row2.Status != StatusInProgress || !row2.KSMNotReadySince.IsZero() || row2.PodsReadyAt.IsZero() {
		t.Fatalf("iyileşme: %+v", row2)
	}
	// spec==0 (scale-down): sayaç BAŞLAMAZ
	row3 := Rollout{Status: StatusInProgress}
	applyKSM(cfg, b(10), &row3, KSMRev{Spec: 0, Ready: 0})
	if !row3.KSMNotReadySince.IsZero() || row3.Status != StatusInProgress {
		t.Fatalf("spec=0 sayaç başlatmamalı: %+v", row3)
	}
	// StalledMin=0: stalled kapalı
	row4 := Rollout{Status: StatusInProgress}
	applyKSM(Config{Bucket: 5 * time.Minute}, b(12), &row4, KSMRev{Spec: 3, Ready: 1})
	if row4.Status != StatusInProgress {
		t.Fatalf("StalledMin=0 iken stalled yazılmamalı: %+v", row4)
	}
}

func TestKSMQueriesPins(t *testing.T) {
	q := KSMQueries()
	if !strings.Contains(q["rs_owner"], `owner_kind="Deployment"`) {
		t.Fatal("owner sorgusu Deployment'a kısıtlı olmalı (STS/DS ayrı dilim)")
	}
	for _, name := range []string{"rs_owner", "rs_spec", "rs_ready", "rs_created"} {
		if q[name] == "" || !strings.Contains(q[name], `replicaset!=""`) {
			t.Fatalf("%s sorgusu eksik/seçicisiz", name)
		}
	}
}

// Uçtan uca: Input.KSM ile stalled ve iyileşme; stalled satır AÇIK kalır
// (isOpen) ve span kararları üstünde işlemeye devam eder.
func TestReconcile_KSMStalledLifecycle(t *testing.T) {
	cfg := DefaultConfig()
	cfg.StalledMin = 10 * time.Minute
	k := Key{ClusterID: "c1", Namespace: "pay", Workload: "api"}
	// A canlı kalır: tamamlanma kararı stalled yaşam döngüsüyle yarışmasın
	acts := append(span("api-aaaa", -10, 20, 100), span("api-bbbb", 3, 20, 80)...)
	ksmBad := map[Key]map[string]KSMRev{k: {"api-bbbb": {Spec: 3, Ready: 1}}}
	// tik 1 (b8): sayaç başlar (karar anı = lastFull+B = b8)
	out1 := Reconcile(cfg, Input{Now: b(8), Prev: nil, Acts: acts, KSM: ksmBad})
	r1 := find(out1, "api-bbbb")
	if r1 == nil || r1.Status != StatusInProgress || r1.KSMNotReadySince.IsZero() || r1.DetectedBy != "spans+ksm" {
		t.Fatalf("tik1: %+v", r1)
	}
	// tik 2 (b10): 10 dk doldu → stalled (satır zinciri prev'den)
	prev := []Rollout{*r1}
	out2 := Reconcile(cfg, Input{Now: b(10), Prev: prev, Acts: acts, KSM: ksmBad})
	r2 := find(out2, "api-bbbb")
	if r2 == nil || r2.Status != StatusStalled || !strings.Contains(r2.Note, noteStalled) {
		t.Fatalf("tik2 stalled bekleniyordu: %+v", r2)
	}
	// tik 3 (b12): iyileşme → in_progress, not silinir, pods_ready damgası
	out3 := Reconcile(cfg, Input{Now: b(12), Prev: []Rollout{*r2}, Acts: acts, KSM: map[Key]map[string]KSMRev{k: {"api-bbbb": {Spec: 3, Ready: 3}}}})
	r3 := find(out3, "api-bbbb")
	if r3 == nil || r3.Status != StatusInProgress || strings.Contains(r3.Note, noteStalled) || r3.PodsReadyAt.IsZero() {
		t.Fatalf("tik3 iyileşme: %+v", r3)
	}
	// KSM'siz aynı girdi: stalled ASLA (span sessizliği yeterli değil)
	out4 := Reconcile(cfg, Input{Now: b(10), Prev: nil, Acts: acts})
	if r4 := find(out4, "api-bbbb"); r4 == nil || r4.Status == StatusStalled {
		t.Fatalf("KSM'siz stalled üretilmemeli: %+v", r4)
	}
}
