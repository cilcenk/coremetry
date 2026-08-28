package chstore

// exception_pods.go — v0.10.138 (DETAY SAYFALARI adım 4: exceptions dağılımı).
// Bir hata grubunun (fingerprint) oluşumlarının pod/node dağılımı.
//
// ÜYELİK SQL'DE İFADE EDİLEMEZ: fingerprint = (tip, servis, stack'in ilk 5
// çerçevesi | normalize mesaj) Go tarafında hash'lenir (FingerprintException);
// aynı (servis, tip) altında başka gruplar da yaşar. Samples ucuyla aynı
// yol: grubun tarama penceresinde (exceptionScanWindow), aynı eşleşme
// yüklemiyle (exFragments) satırlar okunur, üyelik satır satır AYNI
// fonksiyonla süzülür, kırılım Go'da toplanır. Tarama en yeni N satırla
// sınırlı (exceptionPodsScanLimit) — dolunca Sampled=true, ilan edilir.
// k8s bağlamı olmayan oluşumlar AYRI (NoContext); host.name yedeğiyle dolan
// pod adı (namespace boş) HostOnly işaretlenir — pod DEĞİL, link yok.
// Ham spans: zaman sınırlı + LIMIT + max_execution_time. exception_inbox.go
// emsaliyle ana bağlantı (tek grup, küçük pencere).

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ExceptionPodRow struct {
	Cluster     string    `json:"cluster"`
	Namespace   string    `json:"namespace"`
	Pod         string    `json:"pod"`
	Node        string    `json:"node,omitempty"`
	Occurrences int64     `json:"occurrences"`
	LastSeen    time.Time `json:"lastSeen"`
	// HostOnly — k8s.namespace.name yok: pod adı host.name yedeğinden geldi
	// (promoted_attr fallback). Kubernetes pod'u DEĞİL; link kurulmaz.
	HostOnly bool `json:"hostOnly,omitempty"`
}

type ExceptionPods struct {
	Rows      []ExceptionPodRow `json:"rows"`
	NoContext int64             `json:"noContext"` // gruba ait, k8s.pod.name taşımayan oluşumlar (tarama içinde)
	Total     int64             `json:"total"`     // gruba ait taranan oluşum (Rows + NoContext + tavan dışı)
	Scanned   int64             `json:"scanned"`   // taranan satır (aynı servis+tip; başka gruplar dahil)
	Sampled   bool              `json:"sampled"`   // tarama tavanı doldu — sayılar en yeni N satır üzerinden
	Truncated bool              `json:"truncated"` // pod tavanı doldu (ExceptionPodsLimit)
	// SchemaMissing — k8s_* kolonları yok (bayrak açık, 0011 uygulanmamış):
	// hata değil, ilan.
	SchemaMissing bool      `json:"schemaMissing,omitempty"`
	From          time.Time `json:"from"`
	To            time.Time `json:"to"`
}

// ExceptionPodsLimit — satır tavanı (pod sayısı).
const ExceptionPodsLimit = 50

// exceptionPodsScanLimit — taranan en yeni satır tavanı; +1 okunur ki
// "daha var" bilinsin. 3000 × (mesaj + stack) — samples ucunun sayfa
// tavanıyla aynı mertebe.
const exceptionPodsScanLimit = 3000

// exceptionPodsSQL — saf; tablo-testli. Bind sırası: service, from, to, type.
func exceptionPodsSQL(f exFrag) string {
	return `SELECT cluster, k8s_namespace, k8s_pod, k8s_node, time,
		       ` + f.Msg + ` AS message,
		       ` + f.Stack + ` AS stacktrace
		FROM spans
		WHERE service_name = ?
		  AND time >= ? AND time <= ?
		  AND ` + f.Match + `
		  AND ` + f.Type + ` = ?
		ORDER BY time DESC
		LIMIT ` + strconv.Itoa(exceptionPodsScanLimit+1) + `
		SETTINGS max_execution_time = 10`
}

type exceptionPodScanRow struct {
	Cluster, Namespace, Pod, Node string
	Time                          time.Time
	Message, Stack                string
}

// aggregateExceptionPods — saf; tablo-testli. member: satır bu gruba mı
// (samples ile aynı fingerprint fonksiyonu). Kırılım (cluster, namespace,
// pod); node ilk görülen; son görülme en yeni; bağlamsız ve host-only ayrı.
func aggregateExceptionPods(rows []exceptionPodScanRow, member func(msg, stack string) bool) ExceptionPods {
	type key struct{ c, ns, pod string }
	agg := map[key]*ExceptionPodRow{}
	out := ExceptionPods{Rows: []ExceptionPodRow{}}
	for _, r := range rows {
		if !member(r.Message, r.Stack) {
			continue
		}
		out.Total++
		if r.Pod == "" {
			out.NoContext++
			continue
		}
		k := key{r.Cluster, r.Namespace, r.Pod}
		a := agg[k]
		if a == nil {
			a = &ExceptionPodRow{Cluster: r.Cluster, Namespace: r.Namespace, Pod: r.Pod, HostOnly: r.Namespace == ""}
			agg[k] = a
		}
		a.Occurrences++
		if a.Node == "" && r.Node != "" {
			a.Node = r.Node
		}
		if r.Time.After(a.LastSeen) {
			a.LastSeen = r.Time
		}
	}
	list := make([]ExceptionPodRow, 0, len(agg))
	for _, a := range agg {
		list = append(list, *a)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Occurrences != list[j].Occurrences {
			return list[i].Occurrences > list[j].Occurrences
		}
		return list[i].Pod < list[j].Pod
	})
	if len(list) > ExceptionPodsLimit {
		list = list[:ExceptionPodsLimit]
		out.Truncated = true
	}
	out.Rows = list
	return out
}

// isUnknownIdentifier — CH 47 UNKNOWN_IDENTIFIER (kolon yok). clickhouse-go
// hatayı "code: 47, message: …" biçiminde düzleştirir.
func isUnknownIdentifier(err error) bool {
	return err != nil && strings.Contains(err.Error(), "code: 47")
}

func (s *Store) GetExceptionGroupPods(ctx context.Context, fingerprint string) (ExceptionPods, error) {
	g, err := s.GetExceptionGroup(ctx, fingerprint)
	if err != nil {
		return ExceptionPods{}, err
	}
	if g == nil {
		return ExceptionPods{Rows: []ExceptionPodRow{}}, nil
	}
	from, to := exceptionScanWindow(g.FirstSeen, g.LastSeen)
	rows, err := s.conn.Query(ctx, exceptionPodsSQL(exFragments(s.hasExCols)), g.Service, from, to, g.Type)
	if err != nil {
		if isUnknownIdentifier(err) {
			return ExceptionPods{Rows: []ExceptionPodRow{}, SchemaMissing: true, From: from, To: to}, nil
		}
		return ExceptionPods{}, fmt.Errorf("exception pods: %w", err)
	}
	defer rows.Close()
	scan := make([]exceptionPodScanRow, 0, 256)
	for rows.Next() {
		var r exceptionPodScanRow
		if err := rows.Scan(&r.Cluster, &r.Namespace, &r.Pod, &r.Node, &r.Time, &r.Message, &r.Stack); err != nil {
			return ExceptionPods{}, err
		}
		scan = append(scan, r)
	}
	if err := rows.Err(); err != nil {
		return ExceptionPods{}, err
	}
	sampled := false
	if len(scan) > exceptionPodsScanLimit {
		scan, sampled = scan[:exceptionPodsScanLimit], true
	}
	out := aggregateExceptionPods(scan, func(msg, stack string) bool {
		return FingerprintException(g.Type, msg, g.Service, stack) == fingerprint
	})
	out.Scanned, out.Sampled, out.From, out.To = int64(len(scan)), sampled, from, to
	return out, nil
}
