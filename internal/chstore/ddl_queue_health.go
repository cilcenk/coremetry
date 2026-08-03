// v0.9.613 — dağıtık DDL kuyruğu sağlık teşhisi.
//
// Operatör vakası (prod, 2026-08-03, üç gece): system.distributed_ddl_queue
// doldu, girdiler Inactive, hiçbir host görevleri almıyor. Boot buna
// dayanıklı hâle geldi (v0.9.604-608) ama teşhis elle SQL istiyordu.
// Bu dosya teşhisi ürüne koyuyor.
//
// AYIRICI — canlı kümede kalibre edildi; üç tuzak bizzat yenildi:
//
//  1. MaxPushedDDLEntryID BAŞLATICIYA YERELDİR (chc-1'de 0 görüldü).
//     Kuyruk başı = küme genelindeki tepe, host'un kendi işleneni ile
//     kıyaslanır. Metrikler restart'ta sıfırlandığı için tepe ayrıca
//     kuyruk girdi numaralarından da türetilir (query-%010d) — üç
//     gecelik vakada host'lar restart edildiyse metrik tek başına
//     yalan söylerdi.
//
//  2. system.clusters.is_local GÜVENİLİR DEĞİL (sağlıklı lokal kümede
//     bile 0). İsim karşılaştırması verdict'e SINIRLI girer: yalnız
//     adlandırma şemasının ÇALIŞTIĞI kanıtlıyken (en az bir eşleşme
//     varken) bir host "ulaşılamaz" sayılır. Cluster tanımı IP
//     taşıyorsa hiçbir ad eşleşmez — o hâlde ulaşılamazlık iddiası
//     üretilmez, yanlış "host kapalı" teşhisi verilmez.
//
//  3. status kolonu Nullable: bozuk Keeper girdisi NULL status üretir
//     ve `status != 'Finished'` NULL'u da düşürürdü — en patolojik
//     takılma biçimi görünmez olur, verdict "healthy" dönerdi.
//
// Verdict DAVRANIŞTAN çıkar:
//
//	kuyruk boş                        → healthy
//	ilerleme verisi yok/kesikse       → probe_failed (uydurma teşhis YOK)
//	host metrikte yok + şema eşleşir  → unreachable
//	bir host GERİDE                   → worker_stuck  (restart çözer)
//	kimse geride değil                → worker_skipping (ad uyuşmazlığı)
package chstore

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DDLQueueEntry — bekleyen bir kuyruk satırı (girdi × host).
type DDLQueueEntry struct {
	Entry      string `json:"entry"`
	Host       string `json:"host"`
	Status     string `json:"status"`
	AgeSeconds int64  `json:"ageSeconds"`
	Query      string `json:"query"` // kırpılmış
}

// DDLHostProgress — bir host'un DDL worker ilerlemesi.
type DDLHostProgress struct {
	Host      string `json:"host"`
	Processed int64  `json:"processed"`
	// Behind — kuyruk başının ne kadar gerisinde. >0 = worker takılı.
	Behind int64 `json:"behind"`
}

// DDLQueueHealth — teşhisin tamamı.
type DDLQueueHealth struct {
	ClusterMode bool `json:"clusterMode"`

	// Verdict — healthy | worker_stuck | worker_skipping |
	// unreachable | probe_failed | single_node.
	Verdict string `json:"verdict"`
	Detail  string `json:"detail"`

	// StuckCount — bekleyen TEKİL girdi sayısı (uniqExact(entry) —
	// satır sayısı DEĞİL: her girdi host başına satır üretir, canlıda
	// 2388 satır / 1194 girdi ölçüldü — satır sayımı 2× şişiyordu).
	StuckCount uint64 `json:"stuckCount"`
	// StuckCountApprox — sayım probe'u düştü, StuckCount alt sınır.
	// Sessiz kırpma "hepsi bu" diye okunur; yaklaşıklık İTİRAF edilir.
	StuckCountApprox bool              `json:"stuckCountApprox,omitempty"`
	OldestAgeSeconds int64             `json:"oldestAgeSeconds,omitempty"`
	QueueHead        int64             `json:"queueHead,omitempty"`
	Hosts            []DDLHostProgress `json:"hosts,omitempty"`
	UnreachableHosts []string          `json:"unreachableHosts,omitempty"`
	// Entries — kuyruğun BAŞI (≤20 satır): gerisi onu bekliyor olabilir.
	Entries []DDLQueueEntry `json:"entries,omitempty"`
	// QueueHosts / ClusterHosts — destekleyici bilgi.
	QueueHosts   []string `json:"queueHosts,omitempty"`
	ClusterHosts []string `json:"clusterHosts,omitempty"`
	ProbeErrors  []string `json:"probeErrors,omitempty"`

	Generated int64 `json:"generated"`
}

// ddlQueueVerdict — SAF karar. Sıralama önemli ve tablo-testli.
func ddlQueueVerdict(stuck uint64, approx bool, hosts []DDLHostProgress,
	unreachable []string, queueFailed, progressErrored bool) (string, string) {
	if queueFailed {
		return "probe_failed", "Kuyruk okunamadı — teşhis yapılamıyor. Keeper erişimi ya da system.distributed_ddl_queue sorgusu başarısız (ProbeErrors'a bak)."
	}
	if stuck == 0 {
		return "healthy", "Bekleyen dağıtık DDL yok."
	}
	countTxt := fmt.Sprintf("%d girdi bekliyor", stuck)
	if approx {
		countTxt = fmt.Sprintf("en az %d girdi bekliyor (tam sayım okunamadı)", stuck)
	}
	// İlerleme verisi YOKKEN ya da yarıda KESİLMİŞKEN "kimse geride
	// değil" / "şu host kapalı" çıkarımı yapılamaz — uydurma teşhis,
	// operatörü yanlış düzeltmeye gönderir.
	if progressErrored {
		return "probe_failed", countTxt + " ama worker ilerleme probe'u HATAYLA kesildi (ProbeErrors'a bak) — geride kalan mı, atlayan mı ayırt edilemiyor. Probe'u düşüren şey muhtemelen arızanın kendisi."
	}
	if len(hosts) == 0 {
		return "probe_failed", countTxt + " ama worker ilerleme metrikleri HİÇ dönmedi — ProbeErrors boşsa bu, metriklerin bu ClickHouse sürümünde bulunmadığı anlamına gelebilir."
	}
	if len(unreachable) > 0 {
		return "unreachable", fmt.Sprintf("%s ve şu host(lar) metrik probe'una cevap vermiyor: %v. Host kapalı olabilir — önce onu ayağa kaldır; kuyruk Keeper'da, host dönünce kaldığı yerden işler.", countTxt, unreachable)
	}
	var behind []string
	for _, h := range hosts {
		if h.Behind > 0 {
			behind = append(behind, fmt.Sprintf("%s (%d geride)", h.Host, h.Behind))
		}
	}
	if len(behind) > 0 {
		return "worker_stuck", fmt.Sprintf("%s ve şu host(lar)ın DDL worker'ı takılı: %v. O host(lar)da ClickHouse'u TEKER TEKER yeniden başlat — kuyruk Keeper'da yaşar, veri/kuyruk kaybı olmaz, worker kaldığı yerden devam eder.", countTxt, behind)
	}
	return "worker_skipping", countTxt + " ama hiçbir host geride değil: worker'lar CANLI ve girdileri ATLIYOR. Bu, host'un kendini girdideki adla eşleştirememesi demek (hostname/IP uyuşmazlığı) — QueueHosts ile ClusterHosts'u karşılaştır; düzeltme DNS/cluster tanımı tarafında."
}

// ddlEntryNumber — "query-0000647099" → 647099. Tanınmayan biçim -1.
//
// Neden gerekli: Max*DDLEntryID metrikleri BELLEK-İÇİ ve restart'ta
// sıfırlanır. Üç gecelik incident'ta host'lar restart edildiyse metrik
// tepe 0'a döner ve Behind hesabı yalan söylerdi; kuyruk girdi adları
// ise Keeper'da kalıcı. Tepe = max(metrik, girdi adları).
func ddlEntryNumber(entry string) int64 {
	i := strings.LastIndexByte(entry, '-')
	if i < 0 || i == len(entry)-1 {
		return -1
	}
	n, err := strconv.ParseInt(entry[i+1:], 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// GetDDLQueueHealth — üç bağımsız probe + saf verdict.
//
// Her probe soft-fail eder: teşhis aracı, teşhis ettiği arızada (Keeper
// sıkıntısı, host down) tamamen kör kalmamalı — ama kör kaldığı yerde
// UYDURMAZ, probe_failed der.
func (s *Store) GetDDLQueueHealth(ctx context.Context) (DDLQueueHealth, error) {
	out := DDLQueueHealth{Generated: time.Now().UnixNano()}
	if !s.clusterMode() {
		out.Verdict = "single_node"
		out.Detail = "Tek düğümlü kurulum — dağıtık DDL kuyruğu yok."
		return out, nil
	}
	out.ClusterMode = true
	// Ad boot'ta system.clusters'a karşı doğrulanıyor ama o doğrulama
	// probe hatasında soft-pass geçebiliyor — backtick'i burada da süz
	// ki quoting hiçbir yolda kırılmasın.
	cl := "`" + strings.ReplaceAll(s.cfg.ClusterName, "`", "") + "`"

	// ── Probe 1: bekleyen kuyruk ─────────────────────────────────────
	queueFailed := false
	var entryHead int64 = -1
	rows, err := s.conn.Query(ctx, `
		SELECT entry,
		       coalesce(host, '(NULL)')                        AS h,
		       coalesce(toString(status), '(bozuk girdi)')     AS st,
		       greatest(0, toInt64(now() - query_create_time)) AS age_s,
		       substring(query, 1, 200)                        AS q
		FROM system.distributed_ddl_queue
		WHERE status IS NULL OR status != 'Finished'
		ORDER BY query_create_time ASC
		LIMIT 20
		SETTINGS max_execution_time = 3`)
	if err != nil {
		queueFailed = true
		out.ProbeErrors = append(out.ProbeErrors, "kuyruk: "+err.Error())
	} else {
		seenQH := map[string]bool{}
		for rows.Next() {
			var e DDLQueueEntry
			if serr := rows.Scan(&e.Entry, &e.Host, &e.Status, &e.AgeSeconds, &e.Query); serr != nil {
				queueFailed = true
				out.ProbeErrors = append(out.ProbeErrors, "kuyruk scan: "+serr.Error())
				break
			}
			out.Entries = append(out.Entries, e)
			if !seenQH[e.Host] {
				seenQH[e.Host] = true
				out.QueueHosts = append(out.QueueHosts, e.Host)
			}
			if e.AgeSeconds > out.OldestAgeSeconds {
				out.OldestAgeSeconds = e.AgeSeconds
			}
			if n := ddlEntryNumber(e.Entry); n > entryHead {
				entryHead = n
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			queueFailed = true
			out.ProbeErrors = append(out.ProbeErrors, "kuyruk rows: "+err.Error())
		}
		if !queueFailed {
			// TEKİL girdi sayısı — satır değil (satır = girdi × host;
			// canlıda 2388 satır / 1194 girdi — satır sayımı 2× şişer).
			if err := s.conn.QueryRow(ctx, `
				SELECT toUInt64(uniqExact(entry)) FROM system.distributed_ddl_queue
				WHERE status IS NULL OR status != 'Finished'
				SETTINGS max_execution_time = 3`).Scan(&out.StuckCount); err != nil {
				out.ProbeErrors = append(out.ProbeErrors, "kuyruk sayısı: "+err.Error())
				uniq := map[string]bool{}
				for _, e := range out.Entries {
					uniq[e.Entry] = true
				}
				out.StuckCount = uint64(len(uniq))
				out.StuckCountApprox = true
			}
		}
	}

	// ── Probe 2: host başına worker ilerlemesi ───────────────────────
	progressErrored := false
	prows, err := s.conn.Query(ctx, fmt.Sprintf(`
		SELECT a.host, a.islenen, b.kuyruk_basi
		FROM (
			SELECT hostName() AS host, maxIf(value, metric = 'MaxDDLEntryID') AS islenen
			FROM clusterAllReplicas(%s, system.metrics)
			WHERE metric = 'MaxDDLEntryID'
			GROUP BY host
		) AS a CROSS JOIN (
			SELECT max(value) AS kuyruk_basi
			FROM clusterAllReplicas(%s, system.metrics)
			WHERE metric IN ('MaxDDLEntryID', 'MaxPushedDDLEntryID')
		) AS b
		SETTINGS skip_unavailable_shards = 1, max_execution_time = 3`, cl, cl))
	respondingHosts := map[string]bool{}
	var metricHead int64
	if err != nil {
		progressErrored = true
		out.ProbeErrors = append(out.ProbeErrors, "worker ilerlemesi: "+err.Error())
	} else {
		for prows.Next() {
			var h DDLHostProgress
			var head int64
			if serr := prows.Scan(&h.Host, &h.Processed, &head); serr != nil {
				progressErrored = true
				out.ProbeErrors = append(out.ProbeErrors, "ilerleme scan: "+serr.Error())
				break
			}
			metricHead = head
			out.Hosts = append(out.Hosts, h)
			respondingHosts[h.Host] = true
		}
		prows.Close()
		if err := prows.Err(); err != nil {
			progressErrored = true
			out.ProbeErrors = append(out.ProbeErrors, "ilerleme rows: "+err.Error())
		}
	}
	// Tepe: metrik ∨ girdi adları (metrik restart'ta sıfırlanır).
	out.QueueHead = metricHead
	if entryHead > out.QueueHead {
		out.QueueHead = entryHead
	}
	for i := range out.Hosts {
		b := out.QueueHead - out.Hosts[i].Processed
		if b < 0 {
			b = 0
		}
		out.Hosts[i].Behind = b
	}

	// ── Probe 3: cluster tanımı ──────────────────────────────────────
	crows, err := s.conn.Query(ctx, `
		SELECT DISTINCT host_name FROM system.clusters
		WHERE cluster = ?
		SETTINGS max_execution_time = 3`, s.cfg.ClusterName)
	if err != nil {
		out.ProbeErrors = append(out.ProbeErrors, "cluster tanımı: "+err.Error())
	} else {
		var candidates []string
		matched := 0
		for crows.Next() {
			var hn string
			if serr := crows.Scan(&hn); serr != nil {
				out.ProbeErrors = append(out.ProbeErrors, "cluster scan: "+serr.Error())
				break
			}
			out.ClusterHosts = append(out.ClusterHosts, hn)
			if hostRespondedTo(respondingHosts, hn) {
				matched++
			} else {
				candidates = append(candidates, hn)
			}
		}
		crows.Close()
		if err := crows.Err(); err != nil {
			out.ProbeErrors = append(out.ProbeErrors, "cluster rows: "+err.Error())
		}
		// Ulaşılamazlık iddiası ÜÇ şart birden ister:
		//  - ilerleme probe'u TEMİZ koştu (kesik veride eksik host,
		//    probe kesintisinin eseri olabilir — arızanın değil)
		//  - en az BİR cluster adı eşleşti (adlandırma şeması çalışıyor;
		//    hiçbiri eşleşmiyorsa tanım IP taşıyor olabilir ve "hepsi
		//    kapalı" iddiası, probe'a N host cevap vermişken saçmalaşır)
		//  - aday listesi boş değil
		if !progressErrored && matched > 0 {
			out.UnreachableHosts = candidates
		}
	}

	out.Verdict, out.Detail = ddlQueueVerdict(out.StuckCount, out.StuckCountApprox,
		out.Hosts, out.UnreachableHosts, queueFailed, progressErrored)
	return out, nil
}

// hostRespondedTo — hostName() çıktısı (kısa ad / pod adı) cluster
// tanımındaki adla (çoğu zaman FQDN) eşleşiyor mu? Çift yönlü önek. SAF.
func hostRespondedTo(responding map[string]bool, clusterHost string) bool {
	if responding[clusterHost] {
		return true
	}
	for r := range responding {
		if len(r) > 0 && len(clusterHost) > 0 &&
			(hasHostPrefix(clusterHost, r) || hasHostPrefix(r, clusterHost)) {
			return true
		}
	}
	return false
}

// hasHostPrefix — önekten sonra nokta ya da dize sonu ŞART: yalın
// HasPrefix "chc-1"i "chc-10"a eşlerdi.
func hasHostPrefix(full, prefix string) bool {
	if len(prefix) > len(full) {
		return false
	}
	if full[:len(prefix)] != prefix {
		return false
	}
	return len(full) == len(prefix) || full[len(prefix)] == '.'
}
