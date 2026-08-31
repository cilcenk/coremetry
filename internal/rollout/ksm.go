package rollout

import (
	"context"
	"fmt"
	"time"
)

// ksm.go — Faz 5 dilim 1 (v0.10.212, audit §7 + §16): kube-state-metrics
// RS ikinci kanıdı. Operatör (2026-08-31): "Thanos replicaset'lere de
// ikinci kontrol için bakabilirsin."
//
//   • Kaynak Thanos anlık sorguları (InstantSamples); zaman serisi YOK —
//     "ready < istenen N dk" dayanıklı damgayla tutulur (ksm_not_ready_since
//     tabloda; audit §16.4 önerisi, lider değişimine dayanıklı).
//   • Kabul kapısı (audit §7): kube_replicaset_owner VE (spec|ready) seri
//     veriyorsa açılır; vermiyorsa nil döner — spans tek kaynak, stalled
//     ÜRETİLMEZ. Tavan: owner > 5000 seri = hata (parametreli vida ayrı iş).
//   • RS-only (owner_kind="Deployment"): STS/DS readiness ayrı dilim —
//     onların KSM metrikleri revizyona değil iş yüküne bağlanır.
//   • stalled YALNIZ buradan çıkar; span sessizliği asla (audit madde 7).

// KSMRev — bir ReplicaSet'in KSM anlık görüntüsü.
type KSMRev struct {
	Spec, Ready float64
	CreatedAt   time.Time // kube_replicaset_created (0 = seri yok)
}

// KSMSample — thanos.Sample'ın paket-içi aynası (entity.Sample emsali).
type KSMSample struct {
	Labels map[string]string
	Value  float64
}

// KSMSource — Thanos adaptörü (main.go rolloutKSMSource; testte sahte).
type KSMSource interface {
	FetchKSM(ctx context.Context, ref ClusterRef) (map[Key]map[string]KSMRev, error)
}

// KSMQueries — sorgu haritası; adlar audit §7 doğrulama listesiyle birebir.
// Seçiciler cluster matcher TAŞIMAZ — thanos.doQuery enjekte eder.
func KSMQueries() map[string]string {
	return map[string]string{
		"rs_owner":   `kube_replicaset_owner{replicaset!="",owner_kind="Deployment"}`,
		"rs_spec":    `kube_replicaset_spec_replicas{replicaset!=""}`,
		"rs_ready":   `kube_replicaset_status_ready_replicas{replicaset!=""}`,
		"rs_created": `kube_replicaset_created{replicaset!=""}`,
	}
}

// ksmMaxSeries — audit §7: >1000 tavan ister; 5× pay ile sabit. Aşan cluster
// bu tikte KSM'siz kalır ve koşu satırında görünür (parametreli vida ayrı iş).
const ksmMaxSeries = 5000

// JoinKSM — SAF: örnek kümeleri → Key(cluster,ns,workload) → RS → KSMRev.
// Sahipsiz RS (owner eşleşmesi yok) atlanır: iş yüküne bağlanamaz.
func JoinKSM(clusterID string, sets map[string][]KSMSample) (map[Key]map[string]KSMRev, error) {
	owner := sets["rs_owner"]
	if len(owner) == 0 {
		return nil, nil // aile yok / allowlist kapalı → spans tek kaynak (hata değil)
	}
	if len(owner) > ksmMaxSeries {
		return nil, fmt.Errorf("kube_replicaset_owner %d seri > %d tavanı", len(owner), ksmMaxSeries)
	}
	if len(sets["rs_spec"]) == 0 && len(sets["rs_ready"]) == 0 {
		return nil, nil // audit §7 kabulü: owner VE (spec|ready) şart
	}
	type rsID struct{ ns, rs string }
	wl := map[rsID]string{}
	for _, s := range owner {
		if ns, rs, d := s.Labels["namespace"], s.Labels["replicaset"], s.Labels["owner_name"]; ns != "" && rs != "" && d != "" {
			wl[rsID{ns, rs}] = d
		}
	}
	out := map[Key]map[string]KSMRev{}
	upsert := func(ns, rs string, f func(*KSMRev)) {
		w, ok := wl[rsID{ns, rs}]
		if !ok {
			return
		}
		k := Key{ClusterID: clusterID, Namespace: ns, Workload: w}
		if out[k] == nil {
			out[k] = map[string]KSMRev{}
		}
		kr := out[k][rs]
		f(&kr)
		out[k][rs] = kr
	}
	for _, s := range sets["rs_spec"] {
		v := s.Value
		upsert(s.Labels["namespace"], s.Labels["replicaset"], func(k *KSMRev) { k.Spec = v })
	}
	for _, s := range sets["rs_ready"] {
		v := s.Value
		upsert(s.Labels["namespace"], s.Labels["replicaset"], func(k *KSMRev) { k.Ready = v })
	}
	for _, s := range sets["rs_created"] {
		v := s.Value
		upsert(s.Labels["namespace"], s.Labels["replicaset"], func(k *KSMRev) {
			if v > 0 {
				k.CreatedAt = time.Unix(int64(v), 0).UTC()
			}
		})
	}
	return out, nil
}

// applyKSM — açık satıra KSM damgaları (SAF; at = karar anı, lastFull+B).
// Kanıt disiplini: yalnız GÖZLENEN değer damgalanır. ready==spec>0 → hazır
// (bir kez damgalanır, sayaç sıfırlanır, stalled iyileşir); spec>0 &&
// ready<spec → dayanıklı sayaç + StalledMin sonrası stalled; spec==0
// (scale-down) sayaç BAŞLATMAZ — çekilme kararı span tarafının işi.
func applyKSM(cfg Config, at time.Time, row *Rollout, kr KSMRev) {
	if row.DetectedBy == "spans" || row.DetectedBy == "" {
		row.DetectedBy = "spans+ksm"
	}
	if row.KSMStartedAt.IsZero() && !kr.CreatedAt.IsZero() {
		row.KSMStartedAt = kr.CreatedAt
	}
	switch {
	case kr.Spec > 0 && kr.Ready >= kr.Spec:
		if row.PodsReadyAt.IsZero() {
			row.PodsReadyAt = at
		}
		row.KSMNotReadySince = time.Time{}
		if row.Status == StatusStalled {
			row.Status = StatusInProgress // iyileşme; not zaten her tikte silinir
		}
	case kr.Spec > 0 && kr.Ready < kr.Spec:
		if row.KSMNotReadySince.IsZero() {
			row.KSMNotReadySince = at
		}
		if cfg.StalledMin > 0 && row.Status == StatusInProgress && at.Sub(row.KSMNotReadySince) >= cfg.StalledMin {
			row.Status = StatusStalled
		}
		// noteStalled burada DEĞİL: karar ağacı (completed/withdrawn) durumu
		// ezebilir; not, nihai durum stalled İSE çağıran tarafından eklenir.
	}
}
