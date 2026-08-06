package chstore

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// CustomLink — one operator-bolted-on link per service. The
// catalog renders these as additional chips next to the
// built-in oncall / runbook / repo entries, so a team can
// surface "Grafana board" / "Kibana saved search" /
// "internal SRE dashboard" in one click without us baking
// each surface in as a column.
type CustomLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// ServiceMetadata is operator-curated context for a single
// service: owner team, oncall channel, runbook URL, repo, and
// a free-text description. Joins to the spans table by
// service_name. Lives in a tiny ReplacingMergeTree separate
// from the spans hot path because the data doesn't fit a
// span resource attribute (it's per-team-decided, not per-
// span-emitted) and the row count is bounded by service
// count not span count.
//
// All fields except `service` are optional; an empty row
// surfaces as "no metadata yet" on the frontend with an edit
// CTA so the catalog grows organically.
type ServiceMetadata struct {
	Service   string `json:"service"`
	OwnerTeam string `json:"ownerTeam,omitempty"`
	// SRETeam is the platform / reliability team that owns
	// the operational health of the service — typically
	// distinct from the product owner team. Surfaces as a
	// second chip on the catalog pill so the oncall who
	// inherits the service knows who to escalate to for
	// infra issues vs feature regressions.
	SRETeam     string `json:"sreTeam,omitempty"`
	Description string `json:"description,omitempty"`
	Repository  string `json:"repository,omitempty"`
	RunbookURL  string `json:"runbookUrl,omitempty"`
	OncallURL   string `json:"oncallUrl,omitempty"`
	// ChatChannel — Zoom Chat / Mattermost / Slack channel
	// for the team. Renamed from slack_channel because the
	// catalog target cluster runs on Zoom Chat; the legacy
	// slack_channel column still backfills here on read so
	// pre-rename rows keep showing.
	ChatChannel string `json:"chatChannel,omitempty"`
	// CustomLinks — operator-bolted-on links per service
	// (Grafana / Kibana / Sensei / status page / etc.).
	// Stored as a JSON-encoded array in custom_links column.
	CustomLinks []CustomLink `json:"customLinks,omitempty"`
	UpdatedAt   int64        `json:"updatedAt"` // unix nanoseconds
	// OwnerTeamAuto / SRETeamAuto (v0.8.100) — the last value the span-attr
	// team-deriver auto-wrote for each field. Deriver-managed, NOT human-edited
	// (excluded from JSON). The deriver owns owner_team/sre_team while they
	// equal these (or are empty); a human edit (value != auto) pins the field.
	OwnerTeamAuto string `json:"-"`
	SRETeamAuto   string `json:"-"`
	// Namespace (v0.8.436) — the service's logical namespace, derived
	// from service.namespace / k8s.namespace.name span resource attrs
	// (deriver tick shared with teams; NamespaceAuto is the provenance
	// pin — a manual edit where value != auto stops the deriver).
	// Powers the flow-graph namespace grouping.
	Namespace     string `json:"namespace,omitempty"`
	NamespaceAuto string `json:"-"`
	// Deployment (v0.9.25) — k8s.deployment.name'den türetilen iş
	// yükü adı; Servis→Cluster pivotunun &deployment= hassasiyeti.
	Deployment     string `json:"deployment,omitempty"`
	DeploymentAuto string `json:"-"`
}

// GetServiceMetadata returns the catalog row for one service.
// Missing rows return nil, nil — the page handles the empty
// state inline (no special "404" UI needed).
//
// Read-time fallback: chat_channel is the new column; if a
// pre-rename row only populated slack_channel we surface that
// value so legacy curation doesn't disappear from the UI.
func (s *Store) GetServiceMetadata(ctx context.Context, service string) (*ServiceMetadata, error) {
	if service == "" {
		return nil, nil
	}
	row := s.conn.QueryRow(ctx, `
		SELECT service, owner_team, sre_team, description, repository,
		       runbook_url, oncall_url, chat_channel, slack_channel,
		       custom_links, owner_team_auto, sre_team_auto,
		       namespace, namespace_auto,
		       deployment, deployment_auto,
		       toUnixTimestamp64Nano(updated_at)
		FROM service_metadata FINAL
		WHERE service = ?
		LIMIT 1`, service)
	var m ServiceMetadata
	var legacySlack, customLinks string
	if err := row.Scan(&m.Service, &m.OwnerTeam, &m.SRETeam, &m.Description, &m.Repository,
		&m.RunbookURL, &m.OncallURL, &m.ChatChannel, &legacySlack,
		&customLinks, &m.OwnerTeamAuto, &m.SRETeamAuto,
		&m.Namespace, &m.NamespaceAuto,
		&m.Deployment, &m.DeploymentAuto, &m.UpdatedAt); err != nil {
		// "no rows" → not yet curated; same handling pattern
		// the rest of chstore uses.
		return nil, nil
	}
	if m.ChatChannel == "" && legacySlack != "" {
		m.ChatChannel = legacySlack
	}
	// Malformed JSON in the column collapses to an empty
	// list rather than failing the read — operator just sees
	// no extra links until they re-save the row.
	if customLinks != "" && customLinks != "[]" {
		_ = json.Unmarshal([]byte(customLinks), &m.CustomLinks)
	}
	return &m, nil
}

// ListServiceMetadata returns every catalog row in one shot —
// used by the /services list to render the owner-team chip on
// every row without N round-trips. Cheap because the table is
// at most a few thousand rows.
//
// Cached 30s with write-side invalidation (v0.8.359, perf P2-C):
// the problems enrich chain reads it twice per recompute
// (runbooks + teams), the inbox and /services once more — a
// FINAL scan each time. A local Upsert invalidates immediately,
// so "a catalog edit reflects on the next refresh" still holds;
// a peer pod's edit lands within the TTL (same tolerance as the
// alertRules cache). Each call returns a fresh top-level COPY so
// a caller mutating its map cannot poison the shared snapshot.
func (s *Store) ListServiceMetadata(ctx context.Context) (map[string]ServiceMetadata, error) {
	s.svcMetaMu.RLock()
	if s.svcMetaVal != nil && time.Since(s.svcMetaAt) < svcMetaCacheTTL {
		snap := s.svcMetaVal
		s.svcMetaMu.RUnlock()
		out := make(map[string]ServiceMetadata, len(snap))
		for k, v := range snap {
			out[k] = v
		}
		return out, nil
	}
	s.svcMetaMu.RUnlock()
	rows, err := s.conn.Query(ctx, `
		SELECT service, owner_team, sre_team, description, repository,
		       runbook_url, oncall_url, chat_channel, slack_channel,
		       custom_links, owner_team_auto, sre_team_auto,
		       namespace, namespace_auto,
		       deployment, deployment_auto,
		       toUnixTimestamp64Nano(updated_at)
		FROM service_metadata FINAL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]ServiceMetadata, 64)
	for rows.Next() {
		var m ServiceMetadata
		var legacySlack, customLinks string
		if err := rows.Scan(&m.Service, &m.OwnerTeam, &m.SRETeam, &m.Description, &m.Repository,
			&m.RunbookURL, &m.OncallURL, &m.ChatChannel, &legacySlack,
			&customLinks, &m.OwnerTeamAuto, &m.SRETeamAuto,
			&m.Namespace, &m.NamespaceAuto,
			&m.Deployment, &m.DeploymentAuto, &m.UpdatedAt); err != nil {
			return nil, err
		}
		if m.ChatChannel == "" && legacySlack != "" {
			m.ChatChannel = legacySlack
		}
		if customLinks != "" && customLinks != "[]" {
			_ = json.Unmarshal([]byte(customLinks), &m.CustomLinks)
		}
		out[m.Service] = m
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	s.svcMetaMu.Lock()
	s.svcMetaAt = time.Now()
	s.svcMetaVal = out
	s.svcMetaMu.Unlock()
	// Return a copy — `out` just became the shared snapshot.
	cp := make(map[string]ServiceMetadata, len(out))
	for k, v := range out {
		cp[k] = v
	}
	return cp, nil
}

// invalidateServiceMetadata clears the catalog cache so the next
// ListServiceMetadata re-reads (v0.8.359) — called from every
// catalog write path.
func (s *Store) invalidateServiceMetadata() {
	s.svcMetaMu.Lock()
	s.svcMetaVal = nil
	s.svcMetaMu.Unlock()
}

// UpsertServiceMetadata writes a catalog row. Last-write-wins
// via the ReplacingMergeTree's version column; UpdatedAt is
// always stamped to now() so the operator sees fresh edit
// times in the list. Empty `service` is a no-op (you can't
// curate "no service").
//
// Writes only the new chat_channel column. The legacy
// slack_channel column is left as-is so an upgrade-then-
// downgrade still surfaces the original value; the next edit
// after upgrade migrates the value into chat_channel via the
// read-time fallback.
func (s *Store) UpsertServiceMetadata(ctx context.Context, m ServiceMetadata) error {
	m.Service = strings.TrimSpace(m.Service)
	if m.Service == "" {
		return nil
	}
	// Always serialise CustomLinks — even an empty slice
	// produces "[]" which keeps the column shape stable
	// (read path's `customLinks != ""` guard treats "[]" as
	// no-op anyway). Drop entries with empty url/label so
	// the operator's accidental blank rows don't pollute
	// the chip strip.
	clean := make([]CustomLink, 0, len(m.CustomLinks))
	for _, l := range m.CustomLinks {
		if strings.TrimSpace(l.URL) == "" || strings.TrimSpace(l.Label) == "" {
			continue
		}
		clean = append(clean, CustomLink{
			Label: strings.TrimSpace(l.Label),
			URL:   strings.TrimSpace(l.URL),
		})
	}
	clBytes, err := json.Marshal(clean)
	if err != nil {
		return err
	}
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO service_metadata
		(service, owner_team, sre_team, owner_team_auto, sre_team_auto,
		 namespace, namespace_auto,
		 deployment, deployment_auto,
		 description, repository,
		 runbook_url, oncall_url, chat_channel, custom_links,
		 updated_at, version)`)
	if err != nil {
		return err
	}
	now := time.Now()
	if err := batch.Append(m.Service, m.OwnerTeam, m.SRETeam, m.OwnerTeamAuto, m.SRETeamAuto,
		m.Namespace, m.NamespaceAuto,
		m.Deployment, m.DeploymentAuto,
		m.Description, m.Repository,
		m.RunbookURL, m.OncallURL, m.ChatChannel, string(clBytes),
		now.UTC(), uint64(now.UnixNano())); err != nil {
		return err
	}
	if err := batch.Send(); err != nil {
		return err
	}
	s.invalidateServiceMetadata()
	return nil
}

// ── Auto-derive owner/sre team from span attributes (v0.8.95) ────────────────

// ServiceTeams is the dominant owner/sre team pair derived for one service from
// its span attributes.
type ServiceTeams struct {
	OwnerTeam string
	SRETeam   string
}



// mergeTeams applies derived teams. The deriver OWNS a field while it's empty
// OR still equals the value it last auto-wrote (owner_team_auto / sre_team_auto);
// a human edit (value != auto) pins the field so the deriver leaves it. When
// owned, the field is updated to the derived value AND the *_auto provenance is
// re-stamped — so a team RENAME in the span attrs propagates (v0.8.100; the
// previous fill-when-empty never updated). Returns the row + a changed flag.
// Pure — unit-tested.
func mergeTeams(md ServiceMetadata, t ServiceTeams) (ServiceMetadata, bool) {
	changed := false
	if t.OwnerTeam != "" && (md.OwnerTeam == "" || md.OwnerTeam == md.OwnerTeamAuto) {
		if md.OwnerTeam != t.OwnerTeam || md.OwnerTeamAuto != t.OwnerTeam {
			md.OwnerTeam, md.OwnerTeamAuto = t.OwnerTeam, t.OwnerTeam
			changed = true
		}
	}
	if t.SRETeam != "" && (md.SRETeam == "" || md.SRETeam == md.SRETeamAuto) {
		if md.SRETeam != t.SRETeam || md.SRETeamAuto != t.SRETeam {
			md.SRETeam, md.SRETeamAuto = t.SRETeam, t.SRETeam
			changed = true
		}
	}
	return md, changed
}

// PopulateServiceTeamsFromSpans derives teams from span attributes and fills
// the empty owner_team / sre_team catalog fields (manual values are preserved,
// as are all other metadata fields — UpsertServiceMetadata is a full-row
// replace, so we read-merge-write). Best-effort: a single failed upsert doesn't
// abort the rest. Returns the number of services updated.
func (s *Store) populateTeams(ctx context.Context, derived map[string]ServiceTeams) (int, error) {
	if len(derived) == 0 {
		return 0, nil
	}
	existing, err := s.ListServiceMetadata(ctx)
	if err != nil {
		return 0, err
	}
	updated := 0
	for svc, t := range derived {
		md, ok := existing[svc]
		if !ok {
			md = ServiceMetadata{Service: svc}
		}
		merged, changed := mergeTeams(md, t)
		if !changed {
			continue
		}
		// v0.8.439 (review-confirmed TOCTOU) — the snapshot above may be
		// stale (30s List cache; loop spans many round-trips) and Upsert
		// is a whole-row replace: writing the snapshot row would silently
		// revert a concurrent operator edit AND un-pin a mid-loop manual
		// value. The snapshot only NOMINATES candidates; the write is
		// re-merged onto a FRESH uncached point read.
		if fresh, err := s.GetServiceMetadata(ctx, svc); err != nil {
			continue
		} else if fresh != nil {
			m2, ok2 := mergeTeams(*fresh, t)
			if !ok2 {
				continue
			}
			merged = m2
		}
		if err := s.UpsertServiceMetadata(ctx, merged); err != nil {
			continue // best-effort; the next tick retries
		}
		updated++
	}
	return updated, nil
}




// derivePodIdentitySQL — v0.9.531 pod-adı yedeği için ham malzeme:
// servis başına gözlemlenen pod kimlikleri + satır sayıları. Kimlik
// zinciri deploys.go'daki instanceIdExpr ile aynı (k8s.pod.name →
// service.instance.id → host_name kolonu). Pencere + LIMIT +
// max_execution_time deriveDeploymentSQL ile aynı bütçe.
const derivePodIdentitySQL = `
SELECT service_name, pod, count() AS c
FROM (
  SELECT service_name,
    multiIf(
      res_values[indexOf(res_keys, 'k8s.pod.name')] != '',        res_values[indexOf(res_keys, 'k8s.pod.name')],
      res_values[indexOf(res_keys, 'service.instance.id')] != '', res_values[indexOf(res_keys, 'service.instance.id')],
      host_name != '',                                            host_name,
      '') AS pod
  FROM spans
  WHERE time >= ? AND time <= ?
  LIMIT 2000000
)
WHERE pod != ''
GROUP BY service_name, pod
LIMIT 100000
SETTINGS max_execution_time = 25`

// deploymentFromPodName — v0.9.531. Pod adından deployment adı, YALNIZ
// emin olunan şekillerde; emin değilse "".
//
// Operatör-bildirimli (prod): BFF servislerinde k8s deployment adı
// servis adındaki env ekini TAŞIMIYOR — servis mobile-loans-bff-prod,
// pod mobile-loans-bff-6b8f49b9d5-8hrtj. Katalog deriver'ı yalnız
// deployment.name attr'larını okuyordu; BFF o attr'ı basmadığı için
// deployment_auto boş kalıyor, istemcinin son çaresi (soyulmuş pod adı
// == servis adı TAM eşitliği) env eki yüzünden asla tutmuyor ve Pods /
// Infrastructure sekmeleri "No pods matched" gösteriyordu — CPU/Memory
// grafikleri dahil, çünkü PromQL seçicisi de servis adına düşüyordu.
//
// thanos/promql.go'daki stripPodSuffixes'ten BİLİNÇLİ olarak daha
// muhafazakâr: o bir görüntüleme sezgiseli, bu bir KATALOG ALANI yazar.
//
//	<ad>-<rs-hash 8-10 hex>-<rand5>  → ad   (Deployment — asıl hedef)
//	<ad>-<N, ≤3 hane>                → ad   (StatefulSet ordinali)
//	<ad>-<rand5> (rs-hash'siz)       → ""   (DaemonSet şekli hostname'le
//	                                         ayırt edilemez: vm-app01)
//	UUID service.instance.id         → ""   (son parça 12 hane → ordinal
//	                                         sınırına takılır)
func deploymentFromPodName(pod string) string {
	segs := strings.Split(pod, "-")
	if len(segs) < 2 {
		return ""
	}
	last := segs[len(segs)-1]
	// StatefulSet: kısa sayısal ordinal. 12 haneli UUID kuyruğu ya da
	// tarih damgası gibi uzun sayılar deployment kanıtı DEĞİL.
	if isOrdinalSuffix(last) {
		return strings.Join(segs[:len(segs)-1], "-")
	}
	// Deployment: rand5 + hemen önünde rs-hash. İkisi birden şart —
	// tek başına rand5, "vm-app01" gibi hostname'lerle çakışır.
	if len(segs) >= 3 && isPodRandom5(last) && isRSHash(segs[len(segs)-2]) {
		return strings.Join(segs[:len(segs)-2], "-")
	}
	return ""
}

func isOrdinalSuffix(s string) bool {
	if len(s) == 0 || len(s) > 3 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// isPodRandom5 — k8s'in 5'lik rand alfabesi (rakam + sessiz harfler),
// thanos/promql.go isPodRandomSuffix ile aynı küme.
func isPodRandom5(s string) bool {
	if len(s) != 5 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789bcdfghjklmnpqrstvwxz", c) {
			return false
		}
	}
	return true
}

// isRSHash — thanos/promql.go isReplicaSetHash ile aynı küme.
func isRSHash(s string) bool {
	if len(s) < 8 || len(s) > 10 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// deploymentsFromPodCounts — (servis → pod → satır sayısı) yığınından
// servis başına baskın deployment adayı. Saf — tablo-testli.
func deploymentsFromPodCounts(pods map[string]map[string]uint64) map[string]string {
	out := make(map[string]string, len(pods))
	for svc, byPod := range pods {
		counts := map[string]uint64{}
		for pod, c := range byPod {
			if dep := deploymentFromPodName(pod); dep != "" {
				counts[dep] += c
			}
		}
		var best string
		var bestC uint64
		for dep, c := range counts {
			// Eşitlikte deterministik seçim (map sırası rastgele).
			if c > bestC || (c == bestC && (best == "" || dep < best)) {
				best, bestC = dep, c
			}
		}
		if best != "" {
			out[svc] = best
		}
	}
	return out
}


// mergeDeployment — mergeNamespace'in birebir sahiplik sözleşmesi.
func mergeDeployment(md ServiceMetadata, dep string) (ServiceMetadata, bool) {
	if dep == "" || (md.Deployment != "" && md.Deployment != md.DeploymentAuto) {
		return md, false
	}
	if md.Deployment == dep && md.DeploymentAuto == dep {
		return md, false
	}
	md.Deployment, md.DeploymentAuto = dep, dep
	return md, true
}

// PopulateServiceDeploymentsFromSpans — namespace populate'inin aynası.
func (s *Store) populateDeployments(ctx context.Context, derived map[string]string) (int, error) {
	if len(derived) == 0 {
		return 0, nil
	}
	existing, err := s.ListServiceMetadata(ctx)
	if err != nil {
		return 0, err
	}
	updated := 0
	for svc, dep := range derived {
		md, ok := existing[svc]
		if !ok {
			md = ServiceMetadata{Service: svc}
		}
		merged, changed := mergeDeployment(md, dep)
		if !changed {
			continue
		}
		if fresh, err := s.GetServiceMetadata(ctx, svc); err != nil {
			continue
		} else if fresh != nil {
			refreshed, stillChanged := mergeDeployment(*fresh, dep)
			if !stillChanged {
				continue
			}
			merged = refreshed
		}
		if err := s.UpsertServiceMetadata(ctx, merged); err != nil {
			continue
		}
		updated++
	}
	return updated, nil
}

// mergeNamespace — the pure ownership half, byte-identical semantics to
// mergeTeams: the deriver owns the field while it's empty or still
// equals its own last write; a manual edit (value != auto) pins it.
func mergeNamespace(md ServiceMetadata, ns string) (ServiceMetadata, bool) {
	if ns == "" || (md.Namespace != "" && md.Namespace != md.NamespaceAuto) {
		return md, false
	}
	if md.Namespace == ns && md.NamespaceAuto == ns {
		return md, false
	}
	md.Namespace, md.NamespaceAuto = ns, ns
	return md, true
}

// PopulateServiceNamespacesFromSpans mirrors PopulateServiceTeamsFromSpans
// for the namespace field — read-merge-write per service, best-effort.
func (s *Store) populateNamespaces(ctx context.Context, derived map[string]string) (int, error) {
	if len(derived) == 0 {
		return 0, nil
	}
	existing, err := s.ListServiceMetadata(ctx)
	if err != nil {
		return 0, err
	}
	updated := 0
	for svc, ns := range derived {
		md, ok := existing[svc]
		if !ok {
			md = ServiceMetadata{Service: svc}
		}
		merged, changed := mergeNamespace(md, ns)
		if !changed {
			continue
		}
		// Fresh re-read before the whole-row write — see the identical
		// v0.8.439 TOCTOU note in PopulateServiceTeamsFromSpans.
		if fresh, err := s.GetServiceMetadata(ctx, svc); err != nil {
			continue
		} else if fresh != nil {
			m2, ok2 := mergeNamespace(*fresh, ns)
			if !ok2 {
				continue
			}
			merged = m2
		}
		if err := s.UpsertServiceMetadata(ctx, merged); err != nil {
			continue // best-effort; the next tick retries
		}
		updated++
	}
	return updated, nil
}

// ── v0.9.715 (perf taraması #5) — TEK taramalı birleşik türetme ──────────────
//
// Üç deriver (teams/namespaces/deployments) AYNI 3 saatlik spans
// penceresini ÜÇ KEZ tarıyordu — tarama raporunda SELECT baytının %9.3'ü.
// Şimdi tek tarama: iç SELECT dört değeri birden çıkarır (multiIf sırası
// AYNEN korunur: resource önce, sonra span scope; anahtar sıraları eski
// sorgularla birebir), orta katman (service, ug, sy, ns, dep) kombo
// sayımı üretir. Kombo tablosu KÜÇÜKTÜR; dört marjinal mod Go'da EXACT
// hesaplanır: c'ler (service, değer) başına TOPLANIR, argmax alınır —
// kombo üzerinden argMax marjinali bozardı (testli), o yüzden mod
// SQL'den Go'ya taşındı (topK'ya sessiz düşüş yok; rapor kısıtı).
//
// Eşitlik kırıcı DETERMİNİSTİK (eşit c → sözlükçe küçük): eski
// argMax(val, c)'nin belirsiz tie-break'i tikler arasında değeri
// sallandırabiliyordu — flapping kaynaklarından biri. Pencere
// DARALTILMADI (rapor: seyrek servisler geç keşfedilir).
const deriveMetadataAllSQL = `
SELECT service_name, ug_val, sy_val, ns_val, dep_val, count() AS c
FROM (
  SELECT service_name,
    multiIf(
      has(res_keys, 'ug-team'),  res_values[indexOf(res_keys, 'ug-team')],
      has(res_keys, 'ug_team'),  res_values[indexOf(res_keys, 'ug_team')],
      has(attr_keys, 'ug-team'), attr_values[indexOf(attr_keys, 'ug-team')],
      has(attr_keys, 'ug_team'), attr_values[indexOf(attr_keys, 'ug_team')],
      '') AS ug_val,
    multiIf(
      has(res_keys, 'sy-team'),  res_values[indexOf(res_keys, 'sy-team')],
      has(res_keys, 'sy_team'),  res_values[indexOf(res_keys, 'sy_team')],
      has(attr_keys, 'sy-team'), attr_values[indexOf(attr_keys, 'sy-team')],
      has(attr_keys, 'sy_team'), attr_values[indexOf(attr_keys, 'sy_team')],
      '') AS sy_val,
    multiIf(
      has(res_keys, 'service.namespace'),  res_values[indexOf(res_keys, 'service.namespace')],
      has(res_keys, 'k8s.namespace.name'), res_values[indexOf(res_keys, 'k8s.namespace.name')],
      has(res_keys, 'kubernetes.namespace.name'), res_values[indexOf(res_keys, 'kubernetes.namespace.name')],
      has(res_keys, 'kubernetes.namespace_name'), res_values[indexOf(res_keys, 'kubernetes.namespace_name')],
      has(attr_keys, 'service.namespace'),  attr_values[indexOf(attr_keys, 'service.namespace')],
      has(attr_keys, 'k8s.namespace.name'), attr_values[indexOf(attr_keys, 'k8s.namespace.name')],
      has(attr_keys, 'kubernetes.namespace.name'), attr_values[indexOf(attr_keys, 'kubernetes.namespace.name')],
      has(attr_keys, 'kubernetes.namespace_name'), attr_values[indexOf(attr_keys, 'kubernetes.namespace_name')],
      '') AS ns_val,
    multiIf(
      has(res_keys, 'k8s.deployment.name'),  res_values[indexOf(res_keys, 'k8s.deployment.name')],
      has(res_keys, 'kubernetes.deployment.name'), res_values[indexOf(res_keys, 'kubernetes.deployment.name')],
      has(res_keys, 'kubernetes.deployment_name'), res_values[indexOf(res_keys, 'kubernetes.deployment_name')],
      has(res_keys, 'openshift.deployment.name'), res_values[indexOf(res_keys, 'openshift.deployment.name')],
      has(attr_keys, 'k8s.deployment.name'), attr_values[indexOf(attr_keys, 'k8s.deployment.name')],
      has(attr_keys, 'kubernetes.deployment.name'), attr_values[indexOf(attr_keys, 'kubernetes.deployment.name')],
      has(attr_keys, 'kubernetes.deployment_name'), attr_values[indexOf(attr_keys, 'kubernetes.deployment_name')],
      has(attr_keys, 'openshift.deployment.name'), attr_values[indexOf(attr_keys, 'openshift.deployment.name')],
      '') AS dep_val
  FROM spans
  WHERE time >= ? AND time <= ?
    AND ( has(res_keys, 'ug-team')  OR has(res_keys, 'ug_team')
       OR has(res_keys, 'sy-team')  OR has(res_keys, 'sy_team')
       OR has(attr_keys, 'ug-team') OR has(attr_keys, 'ug_team')
       OR has(attr_keys, 'sy-team') OR has(attr_keys, 'sy_team')
       OR has(res_keys, 'service.namespace')  OR has(res_keys, 'k8s.namespace.name')
       OR has(res_keys, 'kubernetes.namespace.name') OR has(res_keys, 'kubernetes.namespace_name')
       OR has(attr_keys, 'service.namespace') OR has(attr_keys, 'k8s.namespace.name')
       OR has(attr_keys, 'kubernetes.namespace.name') OR has(attr_keys, 'kubernetes.namespace_name')
       OR has(res_keys, 'k8s.deployment.name') OR has(attr_keys, 'k8s.deployment.name')
       OR has(res_keys, 'kubernetes.deployment.name') OR has(res_keys, 'kubernetes.deployment_name')
       OR has(res_keys, 'openshift.deployment.name')
       OR has(attr_keys, 'kubernetes.deployment.name') OR has(attr_keys, 'kubernetes.deployment_name')
       OR has(attr_keys, 'openshift.deployment.name') )
  LIMIT 2000000
)
GROUP BY service_name, ug_val, sy_val, ns_val, dep_val
LIMIT 50000
SETTINGS max_execution_time = 25`

// metaComboRow — birleşik taramanın bir kombosu (SAF marjinal girdisi).
type metaComboRow struct {
	Svc, UG, SY, NS, Dep string
	C                    uint64
}

// marginalModes — SAF: kombo satırlarından dört EXACT marjinal mod.
// Eşitlikte sözlükçe küçük değer kazanır — tikler arası KARARLI.
func marginalModes(rows []metaComboRow) (teams map[string]ServiceTeams, ns, dep map[string]string) {
	type acc map[string]map[string]uint64
	sum := func(a acc, svc, val string, c uint64) {
		if val == "" {
			return
		}
		if a[svc] == nil {
			a[svc] = map[string]uint64{}
		}
		a[svc][val] += c
	}
	ug, sy, nsA, depA := acc{}, acc{}, acc{}, acc{}
	for _, r := range rows {
		sum(ug, r.Svc, strings.TrimSpace(r.UG), r.C)
		sum(sy, r.Svc, strings.TrimSpace(r.SY), r.C)
		sum(nsA, r.Svc, strings.TrimSpace(r.NS), r.C)
		sum(depA, r.Svc, strings.TrimSpace(r.Dep), r.C)
	}
	mode := func(m map[string]uint64) string {
		best, bestC := "", uint64(0)
		for v, c := range m {
			if c > bestC || (c == bestC && (best == "" || v < best)) {
				best, bestC = v, c
			}
		}
		return best
	}
	ns, dep = map[string]string{}, map[string]string{}
	teams = map[string]ServiceTeams{}
	svcs := map[string]bool{}
	for _, a := range []acc{ug, sy, nsA, depA} {
		for svc := range a {
			svcs[svc] = true
		}
	}
	for svc := range svcs {
		if o, sr := mode(ug[svc]), mode(sy[svc]); o != "" || sr != "" {
			teams[svc] = ServiceTeams{OwnerTeam: o, SRETeam: sr}
		}
		if v := mode(nsA[svc]); v != "" {
			ns[svc] = v
		}
		if v := mode(depA[svc]); v != "" {
			dep[svc] = v
		}
	}
	return teams, ns, dep
}

// DeriveServiceMetadataAll — TEK taramada üç türetmenin tamamı.
func (s *Store) DeriveServiceMetadataAll(ctx context.Context, since time.Duration) (map[string]ServiceTeams, map[string]string, map[string]string, error) {
	to := time.Now()
	from := to.Add(-since)
	rows, err := s.conn.Query(ctx, deriveMetadataAllSQL, from, to)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	var combos []metaComboRow
	for rows.Next() {
		var r metaComboRow
		if err := rows.Scan(&r.Svc, &r.UG, &r.SY, &r.NS, &r.Dep, &r.C); err != nil {
			return nil, nil, nil, err
		}
		combos = append(combos, r)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}
	teams, ns, dep := marginalModes(combos)
	return teams, ns, dep, nil
}

// PopulateAllServiceMetadataFromSpans — tikin TEK giriş noktası
// (v0.9.715): bir tarama, üç uygulama. Sıra eski tikle aynı
// (teams → deployments → namespaces); merge/pin/TOCTOU yarıları
// birebir korunmuş populate gövdeleri.
func (s *Store) PopulateAllServiceMetadataFromSpans(ctx context.Context, since time.Duration) (teams, deps, ns int, err error) {
	dt, dns, ddep, derr := s.DeriveServiceMetadataAll(ctx, since)
	if derr != nil {
		return 0, 0, 0, derr
	}
	teams, _ = s.populateTeams(ctx, dt)
	deps, _ = s.populateDeployments(ctx, ddep)
	ns, _ = s.populateNamespaces(ctx, dns)
	return teams, deps, ns, nil
}
