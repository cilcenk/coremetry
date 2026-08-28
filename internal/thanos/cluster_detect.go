package thanos

// cluster_detect.go — v0.10.140 (cluster değeri otomatik eşleme brief'i).
//
// Remote Cluster kaydı Thanos external label'ını (AD + DEĞER) kendisi
// keşfeder; elle giriş son çare. Tek querier'ın önünde N cluster varken
// hangi etiketin cluster'ı ayırdığı kurulumdan kuruluma değişir (cluster,
// cluster_id, prometheus, tenant…) — tek ada sabitlenmez.
//
// Yöntem: ENJEKSİYONSUZ tek sorgu `count by (<adaylar>) (kube_node_info)`
// (olmayan etiketler boş gelir; kube_node_info cluster başına node sayısı
// kadar seri = ucuz). Saf seçici PickClusterLabel: tercih sırasıyla ilk dolu
// etiket; değer = kayıt adına eşit/içeren değer > tek değer > BELİRSİZ
// (adaylar döner, hiçbir şey yazılmaz). Algılanan değer "auto" olarak
// işaretlenir, UI'da görünür, elle geçersiz kılınabilir.
//
// Periyodik doğrulama (LabelCheckTick): auto etiketli her kayıt için
// matcher'lı count(kube_node_info) — 0 seri = uyarı (kayıt SESSİZCE
// bozulmaz; Snapshot.LabelCheck ile ilan edilir).

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// clusterLabelCandidates — tercih sırası. Prometheus/Thanos external label
// yazımları; OpenShift CMO `cluster` + `prometheus` + `prometheus_replica`
// basar, çoklu-kiracı Thanos Receive `tenant_id`.
var clusterLabelCandidates = []string{"cluster", "cluster_id", "cluster_name", "k8s_cluster", "openshift_cluster", "prometheus", "tenant", "tenant_id"}

// strongClusterLabels — adı gereği cluster'ı ayıran etiketler: TEK değer
// görülmesi "querier'ın önünde tek cluster var" demektir. Zayıf etiketler
// (prometheus, tenant…) tüm cluster'larda AYNI değeri taşıyabilir
// (openshift-monitoring/k8s) — tek değer orada hiçbir şeyi ayırmaz; yalnız
// kayıt adıyla eşleşirse kabul (inceleme).
var strongClusterLabels = map[string]bool{"cluster": true, "cluster_id": true, "cluster_name": true, "k8s_cluster": true, "openshift_cluster": true}

type Detection struct {
	Label      string              `json:"label"`
	Value      string              `json:"value"`
	Ambiguous  bool                `json:"ambiguous"`
	Candidates map[string][]string `json:"candidates,omitempty"` // etiket → değerler (belirsizlikte ya da bilgi için)
	Series     int                 `json:"series"`
}

type LabelCheck struct {
	OK        bool      `json:"ok"`
	Series    int       `json:"series"`
	CheckedAt time.Time `json:"checkedAt"`
	Error     string    `json:"error,omitempty"`
}

func detectQuery() string {
	return "count by (" + strings.Join(clusterLabelCandidates, ", ") + ") (kube_node_info)"
}

// PickClusterLabel — saf; tablo-testli. rows: her seri için etiket seti.
func PickClusterLabel(rows []map[string]string, name string) Detection {
	cands := map[string][]string{}
	present := map[string]int{} // etiketi taşıyan seri sayısı (kapsama)
	for _, cand := range clusterLabelCandidates {
		seen := map[string]bool{}
		for _, r := range rows {
			v := strings.TrimSpace(r[cand])
			if v == "" {
				continue
			}
			present[cand]++
			if !seen[v] {
				seen[v] = true
				cands[cand] = append(cands[cand], v)
			}
		}
		sort.Strings(cands[cand])
	}
	det := Detection{Series: len(rows)}
	for _, cand := range clusterLabelCandidates {
		vals := cands[cand]
		if len(vals) == 0 {
			continue
		}
		det.Candidates = cands
		det.Label = cand
		lname := strings.ToLower(strings.TrimSpace(name))
		// 1) kayıt adına eşit değer, 2) kayıt adını içeren / içerilen TEK değer
		for _, v := range vals {
			if strings.EqualFold(v, lname) {
				det.Value = v
				return det
			}
		}
		// Kısmi eşleşme TEK yönlü: kayıt adı DEĞERİN içinde ("prod-eu" ⊂
		// "ocp-prod-eu-west-1"). Ters yön (değer adın içinde) "prod-eu-west"
		// kaydına "prod-eu" değerini yakıştırırdı — başka cluster (inceleme).
		var partial []string
		if lname != "" {
			for _, v := range vals {
				if strings.Contains(strings.ToLower(v), lname) {
					partial = append(partial, v)
				}
			}
		}
		if len(partial) == 1 {
			det.Value = partial[0]
			return det
		}
		// 3) tek değer — yalnız GÜÇLÜ etiketlerde VE etiket HER seride varsa
		// (kapsama): `count by` etiketi taşımayan serileri de satır yapar;
		// "tek değer" yalnız o etiketi taşıyan seriler arasında tek demektir.
		// Bir yönetim cluster'ı cluster="mgmt", iş yükü cluster'ı yalnız
		// cluster_id taşıyorsa "mgmt" bu kayda ait değildir (inceleme).
		if len(vals) == 1 && strongClusterLabels[cand] && present[cand] == len(rows) {
			det.Value = vals[0]
			return det
		}
		det.Ambiguous = true
		return det
	}
	// hiçbir aday etiket yok: matcher gerekmiyor (cluster başına URL modeli)
	return det
}

// DetectClusterLabel — ENJEKSİYONSUZ sorgu + saf seçim.
func (s *Service) DetectClusterLabel(ctx context.Context, c ClusterConfig) (Detection, error) {
	res, err := s.doQueryRaw(ctx, c, "/api/v1/query", url.Values{"query": {detectQuery()}})
	if err != nil {
		return Detection{}, err
	}
	rows := make([]map[string]string, 0, len(res))
	for _, r := range res {
		rows = append(rows, r.Metric)
	}
	return PickClusterLabel(rows, c.Name), nil
}

// ApplyDetection — saf: algılamayı kayda yazar (auto + zaman damgası).
func ApplyDetection(c ClusterConfig, d Detection, now time.Time) (ClusterConfig, error) {
	if d.Ambiguous {
		return c, fmt.Errorf("etiket belirsiz: %s için birden çok değer (%v) — kayıt adı eşleşmiyor; adaylardan seçin", d.Label, d.Candidates[d.Label])
	}
	if d.Label == "" {
		// Aday etiket yok. Mevcut bir matcher varsa SİLİNMEZ (inceleme: boş
		// sonuç — KSM yok, geçici hata — elle girilmiş matcher'ı yok
		// edemez); yalnız etiketi olmayan kayıt "algılandı: etiket yok" olur.
		if c.ThanosLabelName != "" {
			return c, fmt.Errorf("aday etiket bulunamadı (kube_node_info serisi yok); mevcut etiket %s korunuyor", c.ThanosLabelName)
		}
	} else {
		c.ThanosLabelName, c.ThanosLabelValue = d.Label, d.Value
	}
	c.ThanosLabelSource, c.ThanosLabelDetectedAt = "auto", now.UnixMilli()
	return c, nil
}

// ── periyodik doğrulama ──
//
// Sonuçlar bellekte + system_settings["thanos_label_checks"] (inceleme: lider
// worker pod'unun belleği api pod'larından görünmez; blob 30 s'de bir her
// pod'a yüklenir). Ayar kaydedilince bellek + blob sıfırlanır, taze denetim
// tetiklenir.

const labelChecksKey = "thanos_label_checks"

type labelCheckStore interface {
	GetSetting(ctx context.Context, key string) ([]byte, error)
	PutSetting(ctx context.Context, key string, value []byte) error
}

func (s *Service) labelCheckFor(id string) *LabelCheck {
	if s == nil {
		return nil
	}
	s.checkMu.RLock()
	defer s.checkMu.RUnlock()
	if lc, ok := s.labelChecks[id]; ok {
		cp := lc
		return &cp
	}
	return nil
}

// LabelCheckTick — auto etiketli (ya da etiketli) her etkin kayıt için
// matcher'lı probe; sonuç bellekte, Snapshot ile ilan edilir. Worker
// rolünde lider ticker'dan çağrılır (main.go).
// ResetLabelChecks — ayar kaydedilince bayat uyarılar düşer (inceleme);
// bir sonraki denetim (kaydın hemen ardından tetiklenir) taze sonucu yazar.
// store verilirse blob da boşaltılır (diğer pod'lar 30 s içinde görür).
func (s *Service) ResetLabelChecks(ctx context.Context, store labelCheckStore) {
	if s == nil {
		return
	}
	s.checkMu.Lock()
	s.labelChecks = nil
	s.checkMu.Unlock()
	if store != nil {
		_ = store.PutSetting(ctx, labelChecksKey, []byte("{}"))
	}
}

// LabelCheckTickPersist — denetim + blob'a yaz (lider ticker'ı).
func (s *Service) LabelCheckTickPersist(ctx context.Context, store labelCheckStore) {
	s.LabelCheckTick(ctx)
	if s == nil || store == nil {
		return
	}
	s.checkMu.RLock()
	raw, err := json.Marshal(s.labelChecks)
	s.checkMu.RUnlock()
	if err == nil {
		if perr := store.PutSetting(ctx, labelChecksKey, raw); perr != nil {
			log.Printf("[thanos] label check blob yazılamadı: %v", perr)
		}
	}
}

// LoadLabelChecks — blob'dan belleğe (her pod; api rolü lider değildir).
func (s *Service) LoadLabelChecks(ctx context.Context, store labelCheckStore) error {
	if s == nil || store == nil {
		return nil
	}
	raw, err := store.GetSetting(ctx, labelChecksKey)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	var m map[string]LabelCheck
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	s.checkMu.Lock()
	s.labelChecks = m
	s.checkMu.Unlock()
	return nil
}

// StartLabelCheckRefresh — 30 s'de bir blob'u yükle (StartConfigRefresh emsali).
func (s *Service) StartLabelCheckRefresh(ctx context.Context, store labelCheckStore, interval time.Duration) {
	if s == nil || store == nil {
		return
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.LoadLabelChecks(ctx, store); err != nil {
				log.Printf("[thanos] label check refresh: %v", err)
			}
		}
	}
}

func (s *Service) LabelCheckTick(ctx context.Context) {
	if s == nil {
		return
	}
	cfg := s.CurrentSettings()
	fresh := map[string]LabelCheck{} // her tick sıfırdan: silinen/kapatılan kayıt bayat uyarı bırakmaz
	for _, c := range cfg.Clusters {
		if !c.Enabled || c.URL == "" || c.ThanosLabelName == "" {
			continue
		}
		qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		n, err := s.ProbeCluster(qctx, c)
		cancel()
		lc := LabelCheck{OK: err == nil && n > 0, Series: n, CheckedAt: time.Now()}
		if err != nil {
			lc.Error = err.Error()
		} else if n == 0 {
			l, v := c.EffectiveThanosLabel()
			lc.Error = fmt.Sprintf("etiket artık eşleşmiyor: %s=%q ile kube_node_info serisi yok", l, v)
		}
		fresh[c.EffectiveID()] = lc
	}
	s.checkMu.Lock()
	s.labelChecks = fresh
	s.checkMu.Unlock()
}

// labelCheckState — Service'e gömülü durum (client.go struct alanları).
type labelCheckState struct {
	checkMu     sync.RWMutex
	labelChecks map[string]LabelCheck
}
