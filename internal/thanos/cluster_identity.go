package thanos

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/url"
	"regexp"
	"strconv"
)

// cluster_identity.go — REMOTE CLUSTER KAYDI = ENTITY HİYERARŞİSİNİN KÖKÜ
// (v0.10.128, K8s entity katmanı adım 2; design §1.1, keşif engel #2).
//
// Keşif: kayıtta id YOKTU, `Name` join anahtarıydı; ad değişince kimlik
// kırılıyordu; Thanos serisi ↔ kayıt ↔ span cluster değeri için eşleme
// alanı yoktu. Üç karar:
//
//   ID          opak, DEĞİŞMEZ. Kayıt oluşturulurken üretilir; mevcut
//               kayıtlar için boot'ta Name'den türetilir ve bloba BİR KEZ
//               yazılır (BackfillClusterIDs). Name sonradan değişse de id
//               kalır — entity_id'nin birinci bileşeni.
//   Thanos      ThanosLabelName BOŞ = matcher yok = eski davranış (cluster
//   etiketi     başına URL). Dolu ise doQuery her seçiciye
//               <ad>="<değer>" enjekte eder (cluster_matcher.go); değer
//               boşsa Name.
//   Span değeri SpanClusterValue boşsa Name (bugünkü join anahtarı).
//
// Geriye dönük doldurma = türetilmiş varsayılanlar; satır yeniden
// yazılmaz, yalnız boş ID doldurulur.

var promLabelNameRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// derivedClusterID — "c-" + fnv64a(Name) ilk 8 hex. Boş ad → boş id.
func derivedClusterID(name string) string {
	if name == "" {
		return ""
	}
	h := fnv.New64a()
	h.Write([]byte(name))
	return fmt.Sprintf("c-%08x", h.Sum64()&0xffffffff)
}

// EffectiveID — kayıtlı ID ya da Name'den türetilmiş id.
func (c ClusterConfig) EffectiveID() string {
	if c.ID != "" {
		return c.ID
	}
	return derivedClusterID(c.Name)
}

// EffectiveThanosLabel — (etiket adı, değeri). Ad boşsa ("", "") =
// enjeksiyon yok. Değer boşsa Name.
func (c ClusterConfig) EffectiveThanosLabel() (string, string) {
	if c.ThanosLabelName == "" {
		return "", ""
	}
	if c.ThanosLabelValue != "" {
		return c.ThanosLabelName, c.ThanosLabelValue
	}
	return c.ThanosLabelName, c.Name
}

// SpanClusterKey — span `cluster` kolonunda bu cluster'ın değeri.
func (c ClusterConfig) SpanClusterKey() string {
	if c.SpanClusterValue != "" {
		return c.SpanClusterValue
	}
	return c.Name
}

// ValidThanosLabelName — PUT doğrulaması: boş ya da Prometheus etiket adı.
func ValidThanosLabelName(s string) bool {
	return s == "" || promLabelNameRe.MatchString(s)
}

// BackfillClusterIDs — boş ID'leri türetir. Girdi değişmez; changed=true
// ise çağıran tam-blob yazar (SavePersisted). Adsız kayıt dokunulmaz.
func BackfillClusterIDs(cfg Settings) (Settings, bool) {
	out := Settings{Clusters: make([]ClusterConfig, len(cfg.Clusters))}
	copy(out.Clusters, cfg.Clusters)
	changed := false
	for i := range out.Clusters {
		if out.Clusters[i].ID == "" && out.Clusters[i].Name != "" {
			out.Clusters[i].ID = derivedClusterID(out.Clusters[i].Name)
			changed = true
		}
	}
	return out, changed
}

// ClusterByID — etkin kayıt, id ile.
func (s *Service) ClusterByID(id string) (ClusterConfig, bool) {
	if id == "" {
		return ClusterConfig{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.cfg.Clusters {
		if c.Enabled && c.EffectiveID() == id {
			return c, true
		}
	}
	return ClusterConfig{}, false
}

// ClusterByRef — `?cluster=` id YA DA ad taşıyabilir (eski URL'ler ad
// taşır; yeni yüzeyler id). Önce id, sonra ad.
func (s *Service) ClusterByRef(ref string) (ClusterConfig, bool) {
	if c, ok := s.ClusterByID(ref); ok {
		return c, true
	}
	return s.ClusterByName(ref)
}

// ReconcileClusterSettings — PUT'un kimlik/gizli birleştirmesi (saf).
//
// ID sunucu sahipli:
//
//	(a) gelen ID saklı bir kayda aitse → yeniden adlandırma: o id ve
//	    (gelen token boşsa) o kaydın token'ı korunur;
//	(b) aynı AD saklıysa → saklı id + (boş token yerine) saklı token;
//	(c) yeni kayıt → Name'den türetilmiş id; istemcinin gönderdiği
//	    yabancı id alınmaz.
//
// Etiket adı Prometheus etiket sözdizimine uymuyorsa hata (boş serbest).
func ReconcileClusterSettings(in, cur Settings) (Settings, error) {
	byID := map[string]ClusterConfig{}
	byName := map[string]ClusterConfig{}
	for _, c := range cur.Clusters {
		if id := c.EffectiveID(); id != "" {
			byID[id] = c
		}
		byName[c.Name] = c
	}
	out := Settings{Clusters: make([]ClusterConfig, len(in.Clusters))}
	copy(out.Clusters, in.Clusters)
	for i := range out.Clusters {
		c := &out.Clusters[i]
		if !ValidThanosLabelName(c.ThanosLabelName) {
			return Settings{}, fmt.Errorf("thanosLabelName %q geçersiz (Prometheus etiket adı bekleniyor)", c.ThanosLabelName)
		}
		var prev ClusterConfig
		var found bool
		if c.ID != "" {
			prev, found = byID[c.ID]
		}
		if !found {
			prev, found = byName[c.Name]
		}
		switch {
		case found:
			c.ID = prev.EffectiveID()
			if c.Token == "" {
				c.Token = prev.Token
			}
		default:
			c.ID = derivedClusterID(c.Name)
		}
	}
	return out, nil
}

// ProbeCluster — rozet: matcher'lı `count(kube_node_info)` kaç seri
// döndürüyor? (Settings "Test label"; 10 s deadline çağıranda.) Etiket
// adı boşsa matcher yok — yine de URL/token'ı sınar.
func (s *Service) ProbeCluster(ctx context.Context, c ClusterConfig) (int, error) {
	res, err := s.doQuery(ctx, c, "/api/v1/query", url.Values{"query": {"count(kube_node_info)"}})
	if err != nil {
		return 0, err
	}
	if len(res) == 0 || len(res[0].Value) < 2 {
		return 0, nil
	}
	var raw string
	if err := json.Unmarshal(res[0].Value[1], &raw); err != nil {
		return 0, fmt.Errorf("probe değeri çözülemedi: %w", err)
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("probe değeri sayı değil: %q", raw)
	}
	return int(f), nil
}

// Sample — anlık sorgu satırı: etiket seti + değer (entity syncer için
// dışa açık şekil; promSeries paket-içi kalır).
type Sample struct {
	Labels map[string]string
	Value  float64
}

// InstantSamples — matcher'lı anlık sorgu (doQuery enjeksiyonu). Görev
// kısıtı: Thanos'a giden her sorgu seçici taşır — expr'in seçicisi
// çağıranın sorumluluğu (entity.SnapshotQueries); cluster matcher burada.
func (s *Service) InstantSamples(ctx context.Context, c ClusterConfig, expr string) ([]Sample, error) {
	res, err := s.doQuery(ctx, c, "/api/v1/query", url.Values{"query": {expr}})
	if err != nil {
		return nil, err
	}
	out := make([]Sample, 0, len(res))
	for _, r := range res {
		v := 0.0
		if len(r.Value) >= 2 {
			var raw string
			if json.Unmarshal(r.Value[1], &raw) == nil {
				v, _ = strconv.ParseFloat(raw, 64)
			}
		}
		out = append(out, Sample{Labels: r.Metric, Value: v})
	}
	return out, nil
}
