package api

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// InboxItem is the unified shape every triage-worthy thing
// (Problem / Exception group / Anomaly event) collapses into.
// Kind discriminates which source it came from; the kind-
// specific blob carries the bits needed to drill-down.
//
// Designed so a single table on /inbox can show "everything
// needing a human" without operators tab-hopping between
// Problems / Exceptions / Anomalies pages — same priority
// blend, same age column, same assignee column. The per-source
// pages still exist as drill-down targets.
type InboxItem struct {
	ID             string `json:"id"`       // composite: "<kind>:<nativeId>"
	Kind           string `json:"kind"`     // problem | exception | anomaly
	Source         string `json:"source"`   // human label: "Alert rule" / "Exception" / "Anomaly"
	Priority       string `json:"priority"` // P1 | P2 | P3
	PriorityReason string `json:"priorityReason"`
	Severity       string `json:"severity"` // critical | warning | info
	Service        string `json:"service"`
	// SubjectKind (v0.9.1339) — Service alanının NE OLDUĞU: service | db.
	// ⚠️ Yukarıdaki `Kind` ile KARIŞTIRMA: o satırın KAYNAĞINI söylüyor
	// (problem | exception | anomaly), bu ise ÖZNENİN TÜRÜNÜ. İkisi de
	// string olduğu için ne Go ne TypeScript yanlış olanı okumayı
	// engeller — ayrı JSON adı (`subjectKind`) tek koruma.
	// Boş = service (exception/anomaly üreticileri gerçek servis yazar).
	SubjectKind string `json:"subjectKind,omitempty"`
	Title       string `json:"title"` // rule name / exception type / pattern
	Description string `json:"description"`
	StartedAt   int64  `json:"startedAt"` // unix ns
	LastSeen    int64  `json:"lastSeen"`  // unix ns; for problems == StartedAt
	Assignee    string `json:"assignee,omitempty"`
	// OwnerTeam + SRETeam attached server-side from
	// service_metadata so the inbox can render team chips
	// without each row firing a per-service lookup. Empty when
	// no catalog row exists for the service. OwnerTeam mirrors
	// what's auto-set on Problem.Assignee at open time;
	// surfacing it on every row (even exceptions / anomalies)
	// keeps the column meaningful across kinds.
	OwnerTeam string `json:"ownerTeam,omitempty"`
	SRETeam   string `json:"sreTeam,omitempty"`
	Status    string `json:"status"` // open | acknowledged | resolved (problems);
	// open | regressed (exceptions); active | cleared (anomalies)
	Clusters []string `json:"clusters,omitempty"`

	// v0.9.255 — enrichment results the inbox was already PAYING for and
	// then dropping. listInbox runs EnrichProblemsWithRunbooks /
	// WithDeploys before mapping (three CH round-trips per poll), but
	// problemToInbox never copied the results out, so every triage row
	// arrived without the two facts an operator reaches for first:
	// "is there a runbook" and "did something just deploy". The queries
	// were billed and the answers thrown away.
	//
	// Problem-kind only for now: exceptions and anomalies have their own
	// deploy correlation paths and are not enriched on this route.
	RunbookURL   string                `json:"runbookUrl,omitempty"`
	RecentDeploy *chstore.RecentDeploy `json:"recentDeploy,omitempty"`
	// AISummary (v0.9.530) — AYNI hata sınıfının ikinci nüshası. Hem
	// Problem.AISummary (problem.go:789 SELECT'inde) hem
	// ExceptionGroup.AISummary (exception_inbox.go SELECT'inde) bu
	// handler'ın belleğine ZATEN geliyordu; mapper ikisini de atıyordu.
	// Faturası ödenmiş, cevabı çöpe atılmış — yukarıdaki v0.9.255
	// yorumunun tarif ettiği durumun aynısı, 1300 satır aşağıda.
	//
	// Sunucuda kırpılır: özet tek cümle değil, çok bölümlü bir blok
	// ("Olası neden: / Kanıt: / İlk kontroller:"), ~700 karakter. Tam
	// metnin yeri detay yüzeyi; satırın işi TARAMA.
	//
	// AISummaryAt olmadan gönderilmez: özet tek yazımlıktır ama satırın
	// gövdesi (occurrences, mesaj) altından değişmeye devam eder, ve
	// yaşsız bir çıkarım canlı sayının altında taze görünür.
	AISummary   string `json:"aiSummary,omitempty"`
	AISummaryAt int64  `json:"aiSummaryAt,omitempty"`
	// Kind-specific drill-down hints. Only one is populated per
	// row. Keeps the JSON shape skinny — frontend reads exactly
	// the one matching `kind`.
	Problem   *InboxProblemRef   `json:"problem,omitempty"`
	Exception *InboxExceptionRef `json:"exception,omitempty"`
	Anomaly   *InboxAnomalyRef   `json:"anomaly,omitempty"`
	Incident  *InboxIncidentRef  `json:"incident,omitempty"`
}

// InboxIncidentRef — v0.9.321. A declared Incident is the one triage object
// a HUMAN created on purpose, and it was the only source the merged queue
// never showed: an operator working from /inbox could miss an open incident
// entirely while the sidebar's own /incidents badge counted it.
type InboxIncidentRef struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Status   string `json:"status"`
}

type InboxProblemRef struct {
	ID        string  `json:"id"`
	RuleID    string  `json:"ruleId"`
	Metric    string  `json:"metric"`
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
}

type InboxExceptionRef struct {
	Fingerprint string `json:"fingerprint"`
	Type        string `json:"type"`
	Message     string `json:"message"`
	Occurrences uint64 `json:"occurrences"`
}

type InboxAnomalyRef struct {
	ID           string  `json:"id"`
	Kind         string  `json:"kind"` // "log_pattern" | "trace_op"
	Pattern      string  `json:"pattern"`
	PeakRatio    float64 `json:"peakRatio"`
	CurrentRatio float64 `json:"currentRatio"`
}

// inbox unifies the three triage sources into one ranked list.
// Kept aggressively bounded — at 1000s of services, an inbox
// that returns 5k items isn't actionable. Default cap 200,
// max 500; the operator filters by priority/service/kind to
// shrink further. Cached 15s — see the TTL comment at the
// serveCached call below for why it can't be 10.
func (s *Server) inbox(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	service := strings.TrimSpace(q.Get("service"))
	// v0.9.251 — free-text triage search. `service` stays a
	// service-only substring filter (older shared links and other
	// callers depend on it); `q` is the broader one the page's single
	// search box drives, matching the service, the TITLE and the
	// source label. Title was the gap: with thousands of open items an
	// operator could narrow to a service but never to "timeout" or
	// "OOMKilled".
	search := strings.TrimSpace(q.Get("q"))
	ownerTeam := strings.TrimSpace(q.Get("ownerTeam"))
	sreTeam := strings.TrimSpace(q.Get("sreTeam"))
	// v0.9.1246 (operatör: "takımımın exception'ları dediğinde o takım
	// filtreli exceptions açabilir") — TEK EKSENLİ takım süzgeci:
	// ownerTeam VEYA sreTeam eşleşmesi, yani "bu takımın dokunduğu
	// servisler". owner/sre eksenlerinden FARKLI ve bilerek:
	//
	//   ?owner=X&sre=X  → owner X VE sre X (AND — iki ayrı süzgeç)
	//   ?team=X         → owner X VEYA sre X (BİRLEŞİM)
	//
	// Sohbetin "takımımın exception'ları" cevabı birleşim üzerinden
	// sayıyor (servicesForUserTeam → mcptools.TeamServiceNames), yani
	// köprü ?owner= yazsaydı link, cevabın SAYDIĞINDAN dar bir sayfa
	// açardı: SRE'si o takım olan servislerin satırları sessizce
	// düşerdi ("fallback kapsamı taşımalı" sınıfı — cevaptaki sayı ile
	// açılan sayfa aynı kümeyi göstermeli).
	//
	// Çözümleme TEK KAYNAKTAN: servicesForUserTeam guided'ın ve
	// get_team_services'in kullandığı aynı saf fonksiyona delege ediyor
	// (v0.9.1244 seam'i). İkinci bir uygulama = v0.9.553 sapma sınıfı.
	team := strings.TrimSpace(q.Get("team"))
	// v0.8.387 — global ?env= picker, service-scoped semantics shared
	// with /problems (envKeepsRow): keep rows whose service ran in the
	// env in the last hour, plus service-less (global) rows. Applied
	// post-merge so all three sources filter identically.
	env := strings.TrimSpace(q.Get("env"))
	// open (default) | all | ignored
	//
	// v0.9.254 — `ignored` added. Until now an exception group silenced
	// with Ignore was reachable from NO inbox pivot: pickExceptionState
	// returned "" for both open and all, and the store's default view
	// excludes ignored (exception_inbox.go). The only surface that could
	// show them — and the only place with an Unignore button — was the
	// /problems page's Ignored tab. Retiring that page without this
	// would have made ignoring PERMANENT and irreversible: a group
	// silenced by mistake could never be found again.
	//
	// `ignored` is its own pivot rather than folding into `all` on
	// purpose. Ignoring is a deliberate silencing act; dumping those
	// rows back into the everyday "all" view would re-add exactly the
	// noise the operator silenced.
	statusFilter := strings.TrimSpace(q.Get("status"))
	switch statusFilter {
	case "open", "all", "ignored":
		// ok
	default:
		statusFilter = "open"
	}
	limit := parseInt(q.Get("limit"), 200)
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	// v0.9.319 — server-side sort. The page sorted the RETURNED rows
	// client-side, so "Last seen ascending" meant "the oldest of the
	// priority-ranked top 300", not "the oldest in the queue". Every column
	// but priority silently answered a different question than the one the
	// header claimed. Ids match the frontend COLS exactly.
	sortID, sortDir := normalizeInboxSort(q.Get("sort"), q.Get("dir"))
	// v0.9.320 — occurrence floor, the /problems one (v0.9.315) finally
	// applied to the surface that is supposed to REPLACE /problems. Operator:
	// "1 defa Java timeout aldığı için problems'ta exception gözüküyor" —
	// and the merged triage queue was showing exactly the same one-off rows.
	// Negative/absent → the default floor; an explicit 0 means "show all"
	// and is honoured, which is why this can't use parseInt's default alone.
	minOcc := normalizeInboxMinOcc(q.Get("minOcc"))
	// v0.9.330 (operator-reported, prod) — kind/priority moved SERVER-side.
	//
	// They were client-side facets over a server-CAPPED page: the handler
	// ranked the whole queue by priority, returned the top 300, and the page
	// then kept only the matching kinds out of THOSE. On prod the top 300 were
	// all Incidents, so the v0.9.328 exception-first default rendered "Queue
	// clear" over 2,144 real items — an empty landing page on a queue that was
	// anything but empty. The chips lied too ("Exceptions 0").
	//
	// This is the same lie the search filter told before v0.9.318, in the one
	// place that hurts most: the default view.
	kinds := normalizeInboxSet(q.Get("kind"), inboxKindsAll)
	prios := normalizeInboxSet(q.Get("prio"), inboxPriosAll)
	// v0.9.525 (operatör isteği: "son 1 gün veya 2 saat seçebilmek
	// isterim") — İLK GÖRÜLME penceresi. StartedAt dört türde de "ilk
	// görülme" taşır (problem started_at, exception first_seen, incident/
	// anomaly started_at), yani filtre "bu pencerede ORTAYA ÇIKANLAR"
	// demek — kuyruğun "şu an ne yanıyor" semantiği bozulmaz, "bugün ne
	// çıktı" sorusu tek tıkla cevaplanır. SON GÖRÜLME bilerek değil:
	// 35 gündür yanan bir P1 "son 2 saat"te de görünürdü ve filtre
	// hiçbir şeyi elememiş olurdu.
	//
	// Sabit basamaklar (2h/24h/7d/boş) — cache anahtarına giren her
	// parametrenin kardinalitesi sınırlı olmalı (v0.8.270).
	since := normalizeInboxSince(q.Get("since"))

	// v0.9.221 — :v2: marks the response-shape change (bare array → object
	// with the total). Without the bump a pre-upgrade array could still be
	// sitting under this key and would deserialize into the new shape as an
	// empty page.
	cacheKey := inboxListKey(statusFilter, service, search, ownerTeam, sreTeam, team, env, limit, sortID, sortDir, minOcc, kinds, prios) + ":since=" + since
	// v0.9.228 — 10s → 15s. v0.9.220 gave the inbox list a 30s poll; at a 10s
	// TTL the SWR window is ttl×staleFactor = 30s and the Redis entry expires
	// at 30s too, so each poll arrived at age = 30s + previous latency —
	// always PAST the window, on an already-evicted key. Every single poll
	// therefore paid the full cold path: 8 sequential CH round-trips
	// (ListProblems → 3 enrichers → exceptions → anomalies → env members →
	// service metadata) inside the request. At 15s the window is 45s > 30s,
	// so the poll lands on STALE and returns in ~10ms while refreshing behind
	// it. Identical arithmetic to problems-count (api.go:9020), problems-list
	// (api.go:9132) and inbox-count (below) — the list was the one endpoint
	// that never got it.
	// v0.9.318 — every narrowing filter below runs on the MERGED list, so the
	// per-source fetch has to cover the candidates those narrows will cut
	// down. See inboxSourceLimit.
	// A non-default sort needs the candidates too: ordering a truncated slice
	// by a key the truncation did not use returns the top of the SLICE, not
	// the top of the queue.
	narrowed := service != "" || search != "" || env != "" || ownerTeam != "" || sreTeam != "" ||
		team != "" ||
		sortID != inboxSortDefault || sortDir != "desc" ||
		len(kinds) < len(inboxKindsAll) || len(prios) < len(inboxPriosAll)
	srcLimit := inboxSourceLimit(limit, narrowed)

	s.serveCached(w, r, cacheKey, 15*time.Second, func(ctx context.Context) (any, error) {
		items := make([]InboxItem, 0, 256)

		// v0.9.353 (operator-reported) — the team filter moves INTO the
		// per-source SQL.
		//
		// It used to run in Go over the merged, already-capped scan: an
		// owner pick pulled up to 2000 problems + 1000 incidents + 1000
		// exception rows for the WHOLE estate, enriched all of them (four
		// CH round-trips over 2000 problems), and only then dropped the
		// other teams' rows. On prod that request was so slow the page
		// kept showing the previous, unfiltered response — the operator
		// read it as "the owner filter does not work".
		//
		// The catalog is read ONCE here and reused for the chip enrichment
		// below (it used to be a second identical read). Soft-fail keeps
		// v0.9.342's posture: if the catalog is unreachable the SQL narrow
		// is skipped and the Go pass below still applies — a catalog blip
		// must not blank the page.
		mdMap, mdErr := s.store.ListServiceMetadata(ctx)
		var teamServices []string // nil = no team constraint
		if (ownerTeam != "" || sreTeam != "" || team != "") && mdErr == nil {
			ta := s.teamAliasesCtx(ctx)
			if ownerTeam != "" || sreTeam != "" {
				teamServices = servicesForTeam(ta, mdMap, ownerTeam, sreTeam)
			}
			if team != "" {
				// KESİŞİM: iki eksen birden seçiliyse cevap "bu takımın
				// dokunduğu servisler İÇİNDE owner'ı X olanlar" olmalı —
				// biri diğerini ezmemeli (exception-groups'un env×takım
				// kesişimiyle aynı sözleşme). intersectServices nil'i
				// "bu eksenden kısıt yok" sayar, yani tek eksenli çağrı
				// da bu satırdan geçebilir.
				teamServices = intersectServices(teamServices, servicesForUserTeam(ta, mdMap, team))
			}
		}
		// A team that resolves to no services means an EMPTY page, and the
		// sources must not even be asked: the exception filter's Services
		// field treats an empty slice as "no constraint" (v0.8.310), so
		// passing it through would return an UNFILTERED page.
		teamIsEmpty := teamServices != nil && len(teamServices) == 0
		// v0.9.354 — the kind facet gates WHICH sources are fetched at all.
		// Selecting just "Exceptions" used to fetch + enrich up to 2000
		// problems (four CH round-trips) and then throw every one of them
		// away in applyInboxFacets — the second half of the operator's
		// "çok yavaş geliyor ya da gelmiyor". Only problems and exceptions
		// are gated: they are the expensive sources (enrichment / double
		// fetch). Anomalies and incidents stay always-fetched — small FINAL
		// state tables, no enrichment — so their chip counts remain exact
		// from rows.
		kindOn := make(map[string]bool, len(kinds))
		for _, k := range kinds {
			kindOn[k] = true
		}
		// scanCapped: a source came back exactly full, so there were more
		// candidates than we looked at and the narrow below is answering over
		// a slice. Travels with the response — the "no silent caps" rule
		// (v0.9.221) applied to the SCAN, not just the final page.
		scanCapped := false

		// v0.5.245 — service filter is now case-insensitive
		// substring across all three sources. Per-source SQL
		// filters dropped (each capped at 200 rows so a wider
		// fan-out is cheap); the substring narrow happens once
		// over the merged item list below. Operator typing
		// "java" now matches "java-demo", "java-frontend",
		// etc. without remembering the exact service name.
		// ── Problems ─────────────────────────────────────────────
		//
		// v0.9.254 — skipped entirely on the `ignored` pivot. That view is
		// exception-only: Problems are MUTED and anomalies are SILENCED,
		// different verbs backed by different state, so folding them in
		// would make one view mean three things. Skipping also keeps four
		// CH round-trips (list + three enrichers) off a rarely-opened view.
		// skippedCounts carries exact chip totals for sources we did not
		// fetch, merged into `counts` after inboxFacetCounts.
		skippedCounts := map[string]int{}
		var probs []chstore.Problem
		if statusFilter != "ignored" && kindOn["problem"] {
			var err error
			probs, err = s.store.ListProblems(ctx, chstore.ProblemFilter{
				Status:      pickStatus(statusFilter),
				NotStatuses: pickExcludedStatuses(statusFilter),
				// v0.9.353 — nil = no constraint; empty = "1=0" (ProblemFilter
				// contract, v0.9.342). The enrichers below now run over ONE
				// team's rows instead of the newest 2000 of the estate.
				Services: teamServices,
				Limit:    srcLimit,
			})
			if err != nil {
				return nil, err
			}
			if len(probs) >= srcLimit {
				scanCapped = true
			}
		}
		// Same enrichment chain Problems UI runs through, so the
		// derived priority lines up exactly. No-ops on the empty slice
		// the `ignored` pivot leaves behind.
		if statusFilter != "ignored" && !kindOn["problem"] {
			// Deselected → one cheap COUNT instead of fetch + four enrichers.
			// EDGE, stated honestly: the count's allowlist keeps service-less
			// (global) rows via its `service='' OR …` escape, while the row
			// path's strict team matching drops them — so under an ACTIVE
			// team filter this chip can overcount by the number of
			// service-less problems. Without a team filter it is exact.
			if teamIsEmpty {
				skippedCounts["problem"] = 0
			} else if n, err := s.store.CountProblemsNotInStatuses(ctx,
				pickExcludedStatuses(statusFilter), teamServices); err == nil {
				skippedCounts["problem"] = int(n)
			}
		}
		probs = s.store.EnrichProblemsWithRunbooks(ctx, probs)
		probs = s.store.EnrichProblemsWithClusters(ctx, probs, time.Hour)
		probs = s.enrichProblemsForRead(ctx, probs) // v0.9.553 — deploy+öncelik, sırası sabit
		for _, p := range probs {
			// v0.8.287 — drop resolved Problems from the open inbox. pickStatus
			// fetched every status (see its comment), so the narrow happens here.
			if !inboxKeepsProblem(p.Status, statusFilter) {
				continue
			}
			items = append(items, problemToInbox(p))
		}

		// ── Exception groups ─────────────────────────────────────
		// v0.9.336 — the occurrence floor moved INTO the SQL, in two fetches.
		//
		// It used to be a Go filter over rows the store had already capped at
		// 500 by last_seen. On an install whose recent exceptions are mostly
		// one-offs — which is the operator's whole complaint about them — the
		// window filled with rows the floor was about to drop, so the queue
		// showed a handful of real exceptions or none. The same lesson as the
		// status narrow (v0.9.322): the LIMIT must bite on rows that survive.
		//
		// Two fetches rather than one because `hiddenByMinOcc` has to stay
		// honest: it is counted AFTER the service / search / env / team
		// narrows, none of which SQL can express here. So the below-floor
		// rows are fetched too and ride through the exact same narrows; only
		// then are they split off and counted (applyInboxMinOcc). A single
		// SQL count would ignore those narrows and report an inflated number,
		// which is its own kind of lie.
		// v0.9.441 (operator-reported: "Exception'da gördüğüm kaydı
		// Problems'te P3 seçsem de göremiyorum") — paylaşılan 500 tavanı
		// exception ailesi için ANLAMSIZ dar. 3.1K gruplu prod'da 2.6K
		// grup yapısal olarak aday setine hiç giremiyordu; scanCapped
		// şeridi durumu söylüyordu ama kayıt "yok" gibi görünüyordu.
		//
		// v0.9.571 (operator-reported: "Gece bazı sql exception'ları
		// gelmiş ama problems altında gözükmüyor") — o düzeltme YALNIZ
		// tür filtresi sadece exception'ken uygulanıyordu; VARSAYILAN
		// görünüm (tüm türler seçili) hâlâ 500'e sıkışıyordu.
		//
		// Somut vaka: 2.4K gruplu filoda gece 03:04-03:06 arasında biten
		// bir ORA-18730 patlaması. Aday penceresi last_seen sıralı
		// olduğu için, sabah 08:20'de ilk 500 satır çoktan daha taze
		// gruplarla dolmuştu ve patlama pencereye HİÇ giremedi. Operatör
		// Exceptions sayfasında 2441 kaydı görürken Problems'ta yoktu.
		//
		// Bütçe artık KAYNAĞIN MALİYETİNE bağlı, kaç tür seçili
		// olduğuna değil: exception_groups küçük bir ReplacingMergeTree
		// state tablosu ve 3000 satırlık FINAL okuma inbox'ın 15s
		// cache'i arkasında ucuz — bu gerekçe diğer kaynakların seçili
		// olmasından etkilenmiyor. Cache anahtarı kind setini zaten
		// taşıyor, bütçe farkı slot karıştırmaz.
		//
		// DİKKAT — bağlayıcı kısıt TAVAN DEĞİL TABANDI. İlk düzeltme
		// denemem tavanı 500'den 3000'e çıkardı ve HİÇBİR ŞEY
		// değiştirmezdi: varsayılan görünüm daraltılmamış sayıldığı
		// için srcLimit = inboxBaseScan = 200. Yani exception kaynağı
		// zaten 200 aday görüyordu; 500'lük tavana hiç değmiyordu.
		// 2.4K gruplu bir filoda 200 satırlık last_seen penceresi,
		// birkaç saat önce bitmiş bir patlamayı kesinlikle dışarıda
		// bırakır.
		//
		// Bu yüzden exception bütçesi bir TABAN: kaynak ucuz olduğu
		// için diğer kaynakların dar taramasına ORTAK OLMAZ.
		excLimit := srcLimit
		if excLimit < inboxExcScanMax {
			excLimit = inboxExcScanMax
		}
		// v0.9.353 — teamIsEmpty short-circuits BOTH exception fetches: the
		// exception filter's Services field treats an empty slice as "no
		// constraint" (v0.8.310 contract, opposite of ProblemFilter's), so
		// passing it through would silently return an unfiltered page.
		// v0.9.443 — "httperror" türü: error.type fallback'inin (v0.8.494)
		// ürettiği çıplak 3-haneli tipler ("404") beklenen istemci hatasıdır,
		// exception değil. İki tür AYNI store'dan gelir; HTTPErrors süzgeci
		// hangi sınıfın çekileceğini söyler. Seçilmeyen sınıf kesin COUNT
		// chip'i alır (v0.9.330 sözleşmesi).
		excOn, httpOn := kindOn["exception"], kindOn["httperror"]
		if !teamIsEmpty && !excOn {
			// Deselected → COUNT (state + floor + allowlist + Search, fetch'le
			// aynı). Kalan sapma payı problem-chip'iyle AYNI belgeli kenar:
			// Go-tarafı service-substring/env daraltmaları COUNT'a inemez —
			// chip bu daraltmalar altında hafif ŞİŞKİN sayabilir, asla eksik
			// değil (v0.9.330 "chip 0 yalanı"nın tersi, zararsız yön).
			if n, err := s.store.CountExceptionGroups(ctx, chstore.ExceptionGroupFilter{
				State: pickExceptionState(statusFilter), MinOccurrences: minOcc,
				Services: teamServices, HTTPErrors: "exclude", Search: search,
			}); err == nil {
				skippedCounts["exception"] = int(n)
			}
		}
		if !teamIsEmpty && !httpOn {
			if n, err := s.store.CountExceptionGroups(ctx, chstore.ExceptionGroupFilter{
				State: pickExceptionState(statusFilter), MinOccurrences: minOcc,
				Services: teamServices, HTTPErrors: "only", Search: search,
			}); err == nil {
				skippedCounts["httperror"] = int(n)
			}
		}
		if !teamIsEmpty && (excOn || httpOn) {
			httpFilter := ""
			switch {
			case excOn && !httpOn:
				httpFilter = "exclude"
			case httpOn && !excOn:
				httpFilter = "only"
			}
			exFilter := chstore.ExceptionGroupFilter{
				State: pickExceptionState(statusFilter), Limit: excLimit,
				MinOccurrences: minOcc,
				Services:       teamServices,
				HTTPErrors:     httpFilter,
				// v0.9.441 — arama STORE'a iner (ex_type/message/service
				// ILIKE): eskiden Go'da yalnız ≤500 aday içinde aranıyordu,
				// aday setine girmemiş kayıt aramayla da bulunamıyordu.
				// Go tarafındaki genel arama süzgeci yine uygulanır
				// (service+title+source semantiği değişmez).
				Search: search,
			}
			exGroups, err := s.store.ListExceptionGroups(ctx, exFilter)
			if err != nil {
				return nil, err
			}
			if len(exGroups) >= excLimit {
				scanCapped = true
			}
			for _, g := range exGroups {
				items = append(items, exceptionToInbox(g))
			}
			if minOcc > 0 {
				belowFloor, err := s.store.ListExceptionGroups(ctx, chstore.ExceptionGroupFilter{
					State: pickExceptionState(statusFilter), Limit: excLimit,
					MaxOccurrences: minOcc,
					Services:       teamServices,
					HTTPErrors:     httpFilter,
					Search:         search,
				})
				if err != nil {
					return nil, err
				}
				for _, g := range belowFloor {
					items = append(items, exceptionToInbox(g))
				}
			}
		}

		// ── Anomaly events ───────────────────────────────────────
		// 24h window matches the Anomalies page default. ListAnomaly
		// EventsByService isn't a thing — filter client-side.
		var evs []chstore.AnomalyEvent
		if statusFilter != "ignored" {
			var err error
			// v0.9.335 — the "open" pivot keeps only ACTIVE events, so say so
			// in SQL. Dropping cleared ones in Go after the LIMIT spent the
			// whole scan budget on history: the fourth and last source still
			// narrowing post-cap (problems + incidents got this in v0.9.322).
			evs, err = s.store.ListAnomalyEvents(ctx, chstore.ListAnomalyEventsFilter{
				Limit: srcLimit, ActiveOnly: statusFilter == "open",
				// v0.9.353 — nil = no constraint; empty = match nothing.
				Services: teamServices,
			})
			if err != nil {
				return nil, err
			}
			if len(evs) >= srcLimit {
				scanCapped = true
			}
		}
		for _, e := range evs {
			if statusFilter == "open" && e.Status != "active" {
				continue
			}
			items = append(items, anomalyToInbox(e))
		}

		// ── Incidents ────────────────────────────────────────────
		// v0.9.321 — the fourth source. Skipped on `ignored` for the same
		// reason Problems and anomalies are: that pivot is exception-only
		// (muting a group is a different verb from resolving an incident).
		if statusFilter != "ignored" {
			incLimit := inboxEffectiveLimit(srcLimit, inboxIncStoreMax)
			incs, err := s.store.ListIncidents(ctx, chstore.IncidentFilter{
				NotStatuses: pickExcludedStatuses(statusFilter), Limit: incLimit,
				// v0.9.353 — nil = no constraint; empty = match nothing.
				Services: teamServices,
			})
			if err != nil {
				return nil, err
			}
			if len(incs) >= incLimit {
				scanCapped = true
			}
			for _, inc := range incs {
				if !inboxKeepsIncident(inc.Status, statusFilter) {
					continue
				}
				items = append(items, incidentToInbox(inc))
			}
		}

		// v0.5.245 — case-insensitive substring service filter
		// applied across the merged item set. Operator types
		// "java" → matches "java-demo", "java-frontend", etc.
		// Empty value (no filter) leaves items untouched.
		if service != "" {
			needle := strings.ToLower(service)
			filtered := items[:0]
			for _, it := range items {
				if strings.Contains(strings.ToLower(it.Service), needle) {
					filtered = append(filtered, it)
				}
			}
			items = filtered
		}

		// v0.9.251 — free-text search across the fields an operator
		// actually reads in the row: service, title, and the source
		// label ("Alert rule" / "Exception" / "Anomaly"). Applied on
		// the merged set like the service filter above, so all three
		// kinds match identically. Case-insensitive substring — no
		// tokenising: triage titles are short and operators paste
		// fragments ("OOMKill", "504") rather than words.
		if search != "" {
			needle := strings.ToLower(search)
			filtered := items[:0]
			for _, it := range items {
				if strings.Contains(strings.ToLower(it.Service), needle) ||
					strings.Contains(strings.ToLower(it.Title), needle) ||
					strings.Contains(strings.ToLower(it.Source), needle) {
					filtered = append(filtered, it)
				}
			}
			items = filtered
		}

		// Env filter (v0.8.387) — one cached-map lookup covers the whole
		// merged list (no per-poll query beyond it). Soft-fails to
		// UNFILTERED on a map error, matching envScopeProblems: a
		// transient CH blip must never hide a firing P1. envKeepsRow
		// pins the row semantics (empty-service rows always survive).
		if env != "" {
			if members, err := s.store.EnvMemberServices(ctx, env); err == nil {
				memberSet := make(map[string]bool, len(members))
				for _, m := range members {
					memberSet[m] = true
				}
				filtered := items[:0]
				for _, it := range items {
					if envKeepsRow(it.Service, memberSet) {
						filtered = append(filtered, it)
					}
				}
				items = filtered
			}
		}

		// Team enrichment — reuses the catalog read hoisted to the top of
		// the closure (v0.9.353); this used to be a second identical query.
		if len(mdMap) > 0 {
			for i := range items {
				if items[i].Service == "" {
					continue
				}
				md, ok := mdMap[items[i].Service]
				if !ok {
					continue
				}
				items[i].OwnerTeam = md.OwnerTeam
				items[i].SRETeam = md.SRETeam
			}
		}

		// Team filter — applies AFTER enrichment so the operator
		// can narrow to "the rows whose service is owned by team
		// X". Matches an empty value with empty (so the chip "—"
		// for unattributed rows works), and ignores case so URL
		// pastes between dashboards / chat don't surface a
		// false-negative on a mismatched capitalisation.
		if ownerTeam != "" || sreTeam != "" || team != "" {
			ta := s.teamAliasesCtx(ctx)
			filtered := items[:0]
			for _, it := range items {
				if ownerTeam != "" && !ta.TeamEqual(it.OwnerTeam, ownerTeam) {
					continue
				}
				if sreTeam != "" && !ta.TeamEqual(it.SRETeam, sreTeam) {
					continue
				}
				// v0.9.1246 — tek eksenli takım süzgeci: owner VEYA SRE.
				// Yazım/harf kasası ta.TeamEqual'da katlanıyor ("sy" =
				// "SY"), yani sohbetten gelen link ile kataloğun yazımı
				// ayrı olabilir.
				if !inboxTeamKeepsRow(ta, it.OwnerTeam, it.SRETeam, team) {
					continue
				}
				filtered = append(filtered, it)
			}
			items = filtered
		}

		// v0.9.525 — first-seen penceresi, diğer satır daraltmalarıyla
		// birlikte ve minOcc'tan ÖNCE (hidden sayacı dürüst kalsın).
		if d := inboxSinceDuration(since); d > 0 {
			cutoff := time.Now().Add(-d).UnixNano()
			kept := items[:0]
			for _, it := range items {
				if it.StartedAt >= cutoff {
					kept = append(kept, it)
				}
			}
			items = kept
		}

		// Occurrence floor — last of the row-level narrows, so `hidden` is
		// honest (see applyInboxMinOcc).
		items, hiddenByMinOcc := applyInboxMinOcc(items, minOcc)

		// v0.9.487 (operatör kararı, prod) — exception türü dışındaki
		// türler inbox'ta HEP P3: "bakmadığın türde yazmasına gerek yok,
		// onlar hep P3 olsun, defaultta sadece P1'ler gözüksün". Facet
		// sayaçlarından ÖNCE uygulanır ki öncelik chip'leri de zorlanmış
		// değerleri saysın. Yalnız inbox GÖRÜNÜM önceliği — evaluator'ın
		// Problem.Priority'si (bildirim yönlendirme, drawer) değişmez.
		forceNonExceptionP3(items)

		// v0.9.330 — facet counts are computed HERE, over everything that
		// survived the row-level narrows and BEFORE the kind/priority facets
		// and the cap. That ordering is the whole point: a chip has to report
		// what exists, not what happens to be on the returned page. The old
		// client-side counts were derived from the capped page, so on prod
		// "Exceptions 0" meant "no exceptions in the top 300 incidents".
		counts := inboxFacetCounts(items)
		// v0.9.354 — chips for kinds we did not fetch still show what EXISTS
		// (the v0.9.330 contract); the numbers come from the COUNT queries
		// above. Priority chips now cover the FETCHED kinds only — the
		// honest cost of not fetching 2000 rows to discard them; the
		// hidden-kind guard on the page reads these per-kind totals and
		// stays intact.
		for k, n := range skippedCounts {
			counts[k] = n
		}
		items = applyInboxFacets(items, kinds, prios)

		// Rank the WHOLE candidate set before the cap (v0.9.318 scan fix +
		// v0.9.319 server sort). Sorting after the cap would rank a page,
		// which is the same lie the filters used to tell.
		sortInboxItems(items, sortID, sortDir)
		// v0.9.221 — the cap used to truncate SILENTLY: the response was a
		// bare array, so 200 rows looked identical whether that was the whole
		// queue or the top slice of 900. Since the sort above is priority-desc,
		// what fell off was the low-priority tail — the operator cleared the
		// visible list and believed the queue was empty. CLAUDE.md's "no
		// silent caps" rule; the total travels with the page now.
		total := len(items)
		if len(items) > limit {
			items = items[:limit]
		}
		return map[string]any{
			"items":     items,
			"total":     total,
			"limit":     limit,
			"truncated": total > limit,
			// scanCapped says "there were more candidates than I looked at",
			// which is a DIFFERENT statement from truncated ("more matches
			// than I returned"). Under a search the second can be false while
			// the first is true — that combination is precisely the case
			// where an empty table must not be read as an empty queue.
			"scanCapped": scanCapped,
			// Never silent: the floor says what it hid so the UI can offer
			// those rows in one click. A rare exception that fires once can
			// still be the important one.
			"minOcc":         minOcc,
			"hiddenByMinOcc": hiddenByMinOcc,
			// Facet totals over the pre-facet, pre-cap set. The chips render
			// from these, so they stay truthful about what is being excluded.
			"counts": counts,
		}, nil
	})
}

// forceNonExceptionP3 pins every non-exception kind to P3 for the inbox
// view. v0.9.487 (operatör kararı, prod): operatörün triage sinyali
// exception grupları; problem/httperror/anomaly/incident satırları kendi
// önceliğiyle P1 görünümüne karışmasın — hepsi P3 kovasında, tür + öncelik
// chip'leri üzerinden tek tıkla ulaşılır. Yalnız GÖRÜNÜM: kaynağın kendi
// Priority'si (Problem drawer'ı, bildirim yönlendirme, /problems eski
// yüzeyi) değişmez; PriorityReason zorlamayı açıkça söyler.
func forceNonExceptionP3(items []InboxItem) {
	for i := range items {
		if items[i].Kind == "exception" {
			continue
		}
		if items[i].Priority == "P3" {
			continue
		}
		orig := items[i].Priority
		items[i].Priority = "P3"
		items[i].PriorityReason = "tür kuralı: exception dışı kalemler inbox'ta P3 (kaynak önceliği " + orig + ")"
	}
}

// pickStatus translates the inbox filter into a Problem status.
// "open" inbox shows open + acknowledged (still in-flight);
// "all" passes through to the store's no-filter.
// inboxCount serves GET /api/inbox/count — the single triage badge total
// (v0.8.288, Option B Slice 1b). Sums the three inbox sources with the SAME
// "open" semantics the inbox uses: not-resolved Problems (open+acknowledged,
// consistent with inboxKeepsProblem), open Exception groups, and active
// Anomaly events. COUNT-only on small state tables (no enrichment/sort), 10s
// cache — cheap enough for the 30s sidebar poll at scale.
func (s *Server) inboxCount(w http.ResponseWriter, r *http.Request) {
	// v0.8.472 (perf dalga-1 #2) — ölçülen en büyük tekil gecikme (rozet
	// 24h p95 7.9s): 4 ARDIŞIK CH count'u 3 paralel sorguya indi
	// (open+ack tek IN'li FINAL taraması) → soğuk maliyet toplam yerine
	// max(). TTL 10s→15s: SWR penceresi 45s > 30s poll — rozet STALE
	// yolundan <10ms döner; problem/exception mutasyonları inbox:count'u
	// anında düşürür (read-your-writes).
	// v0.9.219 — env joins the key. The badge used to ignore ?env= while the
	// inbox LIST honoured it, so with an env picked the sidebar said 47 and
	// the page it linked to showed 12. Cache invalidation is prefix-based
	// (cacheInvalidatePrefix "inbox:count"), so every env variant still
	// drops on a problem/exception mutation.
	env := strings.TrimSpace(r.URL.Query().Get("env"))
	s.serveCached(w, r, inboxCountKey(env), 15*time.Second, func(ctx context.Context) (any, error) {
		return s.computeInboxCountFor(ctx, env)
	})
}

// inboxCountKey is shared by the handler and the warm loop (api.go) so the
// pre-warmed payload and the served one can never diverge.
func inboxCountKey(env string) string { return "inbox:count:env=" + env }

// inboxNarrowScan is the per-source candidate ceiling used when a narrowing
// filter is active. All three sources are small ReplacingMergeTree state
// tables (problems, exception_groups, anomaly_events) read with FINAL — a
// 2000-row slice off one is a state-table read, not a spans scan.
const inboxNarrowScan = 2000
const inboxBaseScan = 200

// The per-source ceilings the STORE enforces, which inboxSourceLimit cannot
// exceed no matter what it asks for:
//   ListExceptionGroups clamps Limit > 500 down to 500   (exception_inbox.go)
//   ListIncidents collapses Limit > 1000 to 200          (incident.go)
//   ListProblems / ListAnomalyEvents bind Limit straight through (no clamp)
//
// v0.9.322 — these matter because the honesty probe was `len(rows) >= srcLimit`.
// Asking for 2000 and receiving the store's 500 made that comparison FALSE, so
// the one source most likely to be truncated was also the one that could never
// raise the flag. The probe now compares against what the store will actually
// return.

// inboxExcScanMax (v0.9.441, v0.9.571'de HER duruma genişletildi) —
// exception ailesinin aday tavanı.
//
// Neden paylaşılan 500'den ayrı: aday penceresi last_seen sıralı ve
// exception grup sayısı binlerle ölçülüyor. 500'lük pencere, birkaç
// saat önce SONA ERMİŞ bir patlamayı yapısal olarak dışarıda bırakır —
// grup hâlâ açık, sayfada görünüyor, ama inbox'a hiç aday olmuyor.
//
// v0.9.441 bunu yalnız "tür filtresi sadece exception" hâlinde
// düzeltmişti; varsayılan görünüm (tüm türler) 500'de kalmıştı ve aynı
// hata v0.9.571'de tekrar raporlandı. Bütçe kaynağın MALİYETİNE bağlı
// olmalı, kaç tür seçili olduğuna değil: exception_groups küçük bir
// ReplacingMergeTree state tablosu ve 3000 satırlık FINAL okuma
// inbox'ın 15s cache'i arkasında ucuz.
const inboxExcScanMax = 3000
const inboxIncStoreMax = 1000
const inboxNoStoreMax = 0 // sources that honour the requested limit

// inboxEffectiveLimit is what a source will actually return at most: the
// requested scan, capped by that store's own ceiling. Pass inboxNoStoreMax for
// sources with no clamp.
func inboxEffectiveLimit(want, storeMax int) int {
	if storeMax > 0 && want > storeMax {
		return storeMax
	}
	return want
}

// inboxSourceLimit decides how many rows to pull from EACH source before the
// merge.
//
// v0.9.318 — until now this was a hardcoded 200 per source while every
// narrowing filter (service / q / env / owner / sre) ran on the MERGED list
// AFTERWARDS. That ordering is a lie of exactly the shape this codebase keeps
// paying for: the filter narrows a slice that was already truncated, so a row
// matching "OOMKill" sitting at rank 400 of its source is invisible — the
// operator searches, sees an empty table, and concludes the queue is clean.
// The filter looked like it searched the queue; it searched the first page.
//
// Two independent defects fixed here:
//
//   - narrowed → scan the candidate set, not a page of it. The narrow can only
//     REMOVE rows, so to answer honestly you need the candidates first.
//   - unnarrowed → per-source must at least cover the requested limit. With
//     200/source a limit=300 request (which is what the page asks for) could
//     not be satisfied from a single source even when that source held 300
//     genuinely open rows.
//
// Pure so the arithmetic is pinned by test rather than by reading the handler.
func inboxSourceLimit(limit int, narrowed bool) int {
	n := inboxBaseScan
	if narrowed {
		n = inboxNarrowScan
	}
	if limit > n {
		n = limit
	}
	return n
}

// inboxListKey builds the /api/inbox cache key. Pure + hoisted so
// cache_key-style tests can pin it (canonical: cache_key_test.go).
//
// EVERY input that changes the response is in the key. v0.9.251 added
// `q`: a free-text search that altered the rows but not the key would
// hand one operator's filtered page to another operator's unfiltered
// request — the v0.5.187 cross-poisoning shape, with a search box as
// the vector. The `:v2:` prefix marks the v0.9.221 response-shape
// change (bare array → object with the total).
// v0.9.1246 — `team` (tek eksenli owner∪sre süzgeci) anahtara KATLANMIŞ
// hâliyle girer (chstore.NormTeamName): operatör "SY", sohbet köprüsü
// "sy" yazabilir ve ikisi AYNI satırları döndürür — iki ayrı cache
// girdisi olsalardı 15sn'lik TTL içinde aynı görünüm iki farklı yaşta
// veri gösterirdi.
//
// Neden ALIAS çözümü (CanonTeam) değil de yalnız katlama: alias tablosu
// bir CH point-read'idir (GetTeamAliases → system_settings FINAL) ve
// anahtar serveCached'in DIŞINDA kuruluyor, yani her cache İSABETİ de
// o okumayı öderdi. Katlama saf ve I/O'suz. Bedeli: aynı takımın iki
// alias yazımı iki girdi üretir — içerikleri AYNI olduğu için bu bir
// çapraz-zehirlenme (v0.5.187) değil, yalnız ufak bir tekrar.
func inboxListKey(status, service, search, ownerTeam, sreTeam, team, env string, limit int, sortID, sortDir string, minOcc uint64, kinds, prios []string) string {
	// v0.9.330 — the facets join the key. They now decide WHICH rows come
	// back, not just which of the returned ones render, so two operators on
	// different facets sharing one cached page would be the v0.5.187
	// cross-poisoning shape. Sorted+joined rather than length-only: a
	// length-based digest is the exact bug that release fixed.
	// :v6: — v0.9.487 forceNonExceptionP3 satır önceliklerini değiştirdi;
	// eski cache'lenmiş sayfa yeni sözleşmeymiş gibi servis edilemez.
	return fmt.Sprintf("inbox:v6:status=%s:svc=%s:q=%s:owner=%s:sre=%s:team=%s:env=%s:limit=%d:sort=%s:dir=%s:minOcc=%d:kind=%s:prio=%s",
		status, service, search, ownerTeam, sreTeam, chstore.NormTeamName(team), env, limit, sortID, sortDir, minOcc,
		strings.Join(sortedCopyOf(kinds), "+"), strings.Join(sortedCopyOf(prios), "+"))
}

// sortedCopyOf returns a sorted copy so the key is order-independent:
// ?kind=a,b and ?kind=b,a are the same view and must share one entry.
func sortedCopyOf(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// inboxListCachePrefix is what a mutation drops to make the list re-read.
//
// v0.9.321 — deliberately VERSION-FREE. Every mutation site used to hardcode
// "inbox:v2", so when v0.9.319 bumped the key to :v3 for the sort input, the
// invalidations silently stopped matching anything: acknowledging a problem
// left the queue showing it for the full TTL, and nothing failed. The version
// suffix exists to stop a stale response SHAPE deserializing into a new
// reader — it was never meant to be part of the invalidation contract, and
// coupling the two means the next bump re-breaks this the same way.
//
// "inbox:" also covers "inbox:count", which every one of these sites already
// invalidates on the adjacent line.
const inboxListCachePrefix = "inbox:"

// invalidateInboxCaches drops the badge + list so a mutation is visible on
// the next read instead of up to a TTL later (read-your-writes).
//
// v0.9.321 — incidents are an inbox source now, so their handlers owe this
// the same way problem/exception handlers always have. Acknowledging an
// incident and watching it sit in the queue for another 15s is the kind of
// staleness that makes an operator stop trusting the queue.
func (s *Server) invalidateInboxCaches(r *http.Request) {
	s.cacheInvalidatePrefix(r.Context(), inboxListCachePrefix)
}

// v0.9.330 — kind/priority vocabularies, server-side.
//
// These used to exist only in the frontend (KIND_ALL / PRIO_ALL). Keeping the
// filter there meant it ran AFTER the server's cap, which is why prod showed
// "Queue clear" over 2,144 items: the top 300 by priority were all Incidents,
// and the page then kept only exceptions out of those 300.
var inboxKindsAll = []string{"problem", "exception", "httperror", "anomaly", "incident"}
var inboxPriosAll = []string{"P1", "P2", "P3"}

// normalizeInboxSet parses a comma-separated facet param against its
// vocabulary. Absent, empty, or entirely-unknown → the full set, never an
// empty one: a stale or hand-edited link must open the whole queue rather
// than an empty page that looks like "nothing is wrong".
func normalizeInboxSet(raw string, vocab []string) []string {
	allow := make(map[string]bool, len(vocab))
	for _, v := range vocab {
		allow[v] = true
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(vocab))
	for _, tok := range strings.Split(raw, ",") {
		t := strings.TrimSpace(tok)
		if t != "" && allow[t] && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return append([]string(nil), vocab...)
	}
	return out
}

// inboxFacetCounts totals each kind and priority over the set it is given.
//
// Called BEFORE the facet narrow and BEFORE the cap, so the chips report what
// EXISTS rather than what survived. The client used to count the returned
// page, which is how "Exceptions 0" appeared on a prod queue that held
// thousands of them.
func inboxFacetCounts(items []InboxItem) map[string]int {
	out := make(map[string]int, len(inboxKindsAll)+len(inboxPriosAll))
	for _, k := range inboxKindsAll {
		out[k] = 0
	}
	for _, p := range inboxPriosAll {
		out[p] = 0
	}
	for _, it := range items {
		out[it.Kind]++
		out[it.Priority]++
	}
	return out
}

// applyInboxFacets keeps rows matching BOTH selected sets. A full selection is
// a no-op fast path so the default view pays nothing.
func applyInboxFacets(items []InboxItem, kinds, prios []string) []InboxItem {
	if len(kinds) >= len(inboxKindsAll) && len(prios) >= len(inboxPriosAll) {
		return items
	}
	keepKind := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		keepKind[k] = true
	}
	keepPrio := make(map[string]bool, len(prios))
	for _, p := range prios {
		keepPrio[p] = true
	}
	kept := items[:0]
	for _, it := range items {
		if keepKind[it.Kind] && keepPrio[it.Priority] {
			kept = append(kept, it)
		}
	}
	return kept
}

// inboxDefaultMinOcc — v0.9.417 (operatör kararı 2026-07-30, eski
// "1-2-3'lük grupları gösterme" direktifini GERİ ALIR): varsayılan 0 —
// az-occurrence'lı gruplar da görünür; öncelik formülü (exceptionPriority)
// onları zaten P3'e koyar ve priority-sıralı liste dibe iter. "Bazen az
// sayıda hata olsa da gözükmesi iş görür." ?minOcc= eşiği aynen duruyor
// ve /problems ile aynı varsayılanı paylaşır.
const inboxDefaultMinOcc = 0

// normalizeInboxMinOcc parses ?minOcc=. Absent → the default floor; an
// explicit "0" → no floor ("show all"), which is the affordance that keeps
// the filtering non-silent. Garbage → the default, never an error: a
// hand-edited URL should still open a usable queue.
func normalizeInboxMinOcc(raw string) uint64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return inboxDefaultMinOcc
	}
	n := parseInt(raw, -1)
	if n < 0 {
		return inboxDefaultMinOcc
	}
	return uint64(n)
}

// applyInboxMinOcc drops exception rows below the floor and reports how many
// it hid.
//
// Exception-kind only: Problems come from alert rules and anomalies from
// detectors — neither carries an occurrence count, and silently dropping a
// firing P1 alert because it fired once would be the opposite of triage.
//
// Applied LAST, after every narrow, so the hidden count means "rows that
// passed everything else and failed only the floor". Counting at map time
// would overstate it by including rows the service/env filter would have
// removed anyway — and an inflated "42 hidden" is its own kind of lie.
func applyInboxMinOcc(items []InboxItem, minOcc uint64) ([]InboxItem, int) {
	if minOcc == 0 {
		return items, 0
	}
	kept := items[:0]
	hidden := 0
	for _, it := range items {
		if (it.Kind == "exception" || it.Kind == "httperror") && it.Exception != nil && it.Exception.Occurrences < minOcc {
			hidden++
			continue
		}
		kept = append(kept, it)
	}
	return kept, hidden
}

// inboxSortDefault is the historical rank: priority desc, most-recent first
// within a priority. Preserved exactly so an operator who never touches a
// header sees the page they saw before v0.9.319.
const inboxSortDefault = "priority"

// normalizeInboxSort validates the ?sort=/?dir= pair against the columns the
// page actually renders. Anything unknown falls back to the default rather
// than erroring: a stale link from an older build must still open a usable
// queue, not a 400.
func normalizeInboxSort(id, dir string) (string, string) {
	switch id {
	case "priority", "source", "service", "detail", "lastSeen", "assignee",
		// v0.9.333 — firstSeen is its own column again (operator: "First seen
		// ayrı kolon olabilir, öyleydi"). Sorting by it answers "what started
		// first", which is a different question from "what fired last" and the
		// one that orders a cascade.
		"firstSeen",
		// v0.9.331 — occurrences. The column the operator insisted on keeping
		// (v0.9.315) came back on the merged queue, and a column that can't be
		// sorted on a triage list is half a column: "which of these is
		// actually sustained" is the question the number exists to answer.
		"occurrences":
	default:
		id = inboxSortDefault
	}
	if dir != "asc" {
		dir = "desc"
	}
	return id, dir
}

// sortInboxItems ranks the merged triage set in place.
//
// Every branch keeps the priority-desc, lastSeen-desc tiebreak underneath, so
// two rows equal on the chosen column still arrive in triage order instead of
// whatever order the three sources happened to merge in. Stable on top of
// that, so repeated polls don't shuffle equal rows under the operator's
// cursor.
func sortInboxItems(items []InboxItem, id, dir string) {
	asc := dir == "asc"
	less := func(a, b InboxItem) bool {
		switch id {
		case "source":
			if a.Source != b.Source {
				return a.Source < b.Source
			}
		case "service":
			if !strings.EqualFold(a.Service, b.Service) {
				return strings.ToLower(a.Service) < strings.ToLower(b.Service)
			}
		case "detail":
			if !strings.EqualFold(a.Title, b.Title) {
				return strings.ToLower(a.Title) < strings.ToLower(b.Title)
			}
		case "lastSeen":
			if a.LastSeen != b.LastSeen {
				return a.LastSeen < b.LastSeen
			}
		case "assignee":
			if a.Assignee != b.Assignee {
				return a.Assignee < b.Assignee
			}
		case "occurrences":
			if inboxOccurrences(a) != inboxOccurrences(b) {
				return inboxOccurrences(a) < inboxOccurrences(b)
			}
		case "firstSeen":
			if a.StartedAt != b.StartedAt {
				return a.StartedAt < b.StartedAt
			}
		default: // priority
			ra, rb := priorityRank(a.Priority), priorityRank(b.Priority)
			if ra != rb {
				return ra < rb
			}
		}
		// Tiebreak — ALWAYS triage order, never reversed with dir. A page
		// sorted by service ascending should still show P1 above P3 inside
		// one service; flipping the tiebreak with the header would bury the
		// urgent row at the bottom of its own group.
		ra, rb := priorityRank(a.Priority), priorityRank(b.Priority)
		if ra != rb {
			return ra > rb
		}
		return a.LastSeen > b.LastSeen
	}
	sort.SliceStable(items, func(i, j int) bool {
		if asc {
			return less(items[i], items[j])
		}
		// Descending is the mirror of the primary key only; the tiebreak
		// above is already triage-ordered, so re-running less() with the
		// arguments swapped would invert it too. Compare primaries here and
		// fall through to less() when they are equal.
		if inboxPrimaryEqual(items[i], items[j], id) {
			return less(items[i], items[j])
		}
		return less(items[j], items[i])
	})
}

// inboxOccurrences is the row's occurrence count, or 0 for the kinds that
// don't have one. Problems come from alert-rule firings and incidents are
// declared by a human — neither counts occurrences, so they sort as 0 and
// render as "—" rather than pretending to a number they don't have.
func inboxOccurrences(it InboxItem) uint64 {
	if it.Exception != nil {
		return it.Exception.Occurrences
	}
	return 0
}

// inboxPrimaryEqual reports whether two rows tie on the chosen sort column —
// the point at which the (never-reversed) triage tiebreak takes over.
func inboxPrimaryEqual(a, b InboxItem, id string) bool {
	switch id {
	case "source":
		return a.Source == b.Source
	case "service":
		return strings.EqualFold(a.Service, b.Service)
	case "detail":
		return strings.EqualFold(a.Title, b.Title)
	case "lastSeen":
		return a.LastSeen == b.LastSeen
	case "assignee":
		return a.Assignee == b.Assignee
	case "occurrences":
		return inboxOccurrences(a) == inboxOccurrences(b)
	case "firstSeen":
		return a.StartedAt == b.StartedAt
	default:
		return priorityRank(a.Priority) == priorityRank(b.Priority)
	}
}

// computeInboxCount — rozet toplamının tek hesabı; hem inboxCount
// handler'ı hem 25s warm-loop (v0.8.473) aynı fonksiyonu çağırır ki
// ısıtılan payload ile canlı payload asla ıraksamasın.
func (s *Server) computeInboxCount(ctx context.Context) (any, error) {
	return s.computeInboxCountFor(ctx, "")
}

// computeInboxCountFor scopes the badge to an environment using the SAME
// semantics as the inbox list (envKeepsRow): a service-less row always
// counts, otherwise the service must be an env member.
//
// On an env-map error the count stays UNFILTERED — identical to the list's
// soft-fail (inbox.go:184) and for the same reason: a transient CH blip must
// never hide a firing P1 behind a badge that silently reads 0.
func (s *Server) computeInboxCountFor(ctx context.Context, env string) (any, error) {
	// nil = no env constraint. Non-nil (possibly empty) = constrain.
	var envServices []string
	if env != "" {
		if members, err := s.store.EnvMemberServices(ctx, env); err == nil {
			envServices = members
			if envServices == nil {
				envServices = []string{} // resolved, but empty — keep it non-nil
			}
		}
	}
	var (
		probN, anN, incN uint64
		exN, httpN       int64
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		var err error
		probN, err = s.store.CountProblemsNotInStatuses(gctx, inboxDoneStatuses, envServices)
		return err
	})
	g.Go(func() error {
		// Exception groups always carry a service (they're derived from
		// spans), so there is no service-less bucket to preserve here: an
		// env that resolves to no services can only mean zero. The shared
		// filter treats an empty Services slice as "no constraint", so that
		// case is short-circuited rather than passed down.
		if envServices != nil && len(envServices) == 0 {
			exN = 0
			return nil
		}
		var err error
		// v0.9.322 — the badge must apply the SAME occurrence floor the list
		// applies by default (v0.9.320), or the sidebar promises rows the page
		// then hides: locally badge 4 vs list 3, and on prod — where one-off
		// exceptions are the bulk of the table — the gap is thousands.
		//
		// The DEFAULT floor specifically, not the operator's current ?minOcc=:
		// the badge is global and the page param is per-view. Anchoring to the
		// default means "show all" makes the page show MORE than the badge,
		// never fewer — the harmless direction. A badge larger than the page
		// it links to is the one that reads as a broken count.
		// v0.9.443 — rozet terimi yalnız GERÇEK exception'lar; HTTP-hata
		// grupları ayrı sayıdır (varsayılan facet'te de kapalılar).
		exN, err = s.store.CountExceptionGroups(gctx, chstore.ExceptionGroupFilter{
			State:          pickExceptionState("open"),
			Services:       envServices,
			MinOccurrences: inboxDefaultMinOcc,
			HTTPErrors:     "exclude",
		})
		return err
	})
	g.Go(func() error {
		if envServices != nil && len(envServices) == 0 {
			httpN = 0
			return nil
		}
		var err error
		httpN, err = s.store.CountExceptionGroups(gctx, chstore.ExceptionGroupFilter{
			State:          pickExceptionState("open"),
			Services:       envServices,
			MinOccurrences: inboxDefaultMinOcc,
			HTTPErrors:     "only",
		})
		return err
	})
	g.Go(func() error {
		var err error
		anN, err = s.store.CountActiveAnomalyEvents(gctx, 0, envServices)
		return err
	})
	g.Go(func() error {
		// v0.9.321 — the list gained incidents, so the badge must too. A
		// badge that counts three of four sources disagrees with the page it
		// links to, which is exactly the drift v0.9.219 fixed for env.
		// Statuses match inboxKeepsIncident's "open" pivot.
		var err error
		incN, err = s.store.CountIncidentsNotInStatuses(gctx, inboxDoneStatuses, envServices)
		return err
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}
	problems := probN
	exceptions := uint64(exN)
	return map[string]any{
		"count":      problems + exceptions + anN + incN,
		"problems":   problems,
		"exceptions": exceptions,
		"httpErrors": uint64(httpN),
		"anomalies":  anN,
		"incidents":  incN,
	}, nil
}

// incidentToInbox maps a declared Incident onto the unified row.
//
// Priority comes from the declared severity rather than the derived blend the
// Problems path uses: a human already made the call when they opened the
// incident, so re-deriving it from thresholds would second-guess the only
// signal here that isn't inferred.
func incidentToInbox(inc chstore.Incident) InboxItem {
	prio, reason := "P3", "Declared incident"
	switch inc.Severity {
	case "critical":
		prio, reason = "P1", "Declared incident, critical"
	case "warning":
		prio, reason = "P2", "Declared incident, warning"
	}
	// An acknowledged incident is BEING worked, which is a weaker claim on
	// attention than one nobody has picked up — but it is still open, so it
	// drops one rung rather than out of the queue.
	if inc.Status == "acknowledged" {
		switch prio {
		case "P1":
			prio, reason = "P2", "Declared incident, critical — acknowledged"
		case "P2":
			prio, reason = "P3", "Declared incident, warning — acknowledged"
		}
	}
	last := inc.UpdatedAt
	if last == 0 {
		last = inc.StartedAt
	}
	return InboxItem{
		ID:             "incident:" + inc.ID,
		Kind:           "incident",
		Source:         "Incident",
		Priority:       prio,
		PriorityReason: reason,
		Severity:       inc.Severity,
		Service:        inc.Service,
		Title:          inc.Title,
		Description:    inc.Summary,
		StartedAt:      inc.StartedAt,
		LastSeen:       last,
		Assignee:       inc.Assignee,
		Status:         inc.Status,
		Clusters:       inc.Clusters,
		Incident:       &InboxIncidentRef{ID: inc.ID, Severity: inc.Severity, Status: inc.Status},
	}
}

// inboxKeepsIncident mirrors inboxKeepsProblem: IncidentFilter.Status takes a
// single value, so open+acknowledged can't be expressed in SQL and the narrow
// happens here. Same defensive shape — an empty status counts as active, so a
// row written before the field existed is never silently hidden.
func inboxKeepsIncident(incidentStatus, inboxStatus string) bool {
	if inboxStatus == "all" {
		return true
	}
	return incidentStatus != "resolved"
}

// inboxKeepsProblem decides whether a Problem row belongs in the inbox at the
// given inbox status pivot. Fixes the v0.8.287 leak: pickStatus fetches every
// status (a single-value CH filter can't express open+acknowledged), so the
// "open" pivot must drop resolved rows in Go. "all" keeps everything; an empty
// problem status is treated as active (never silently hide a pre-status row);
// an unknown inbox mode is treated as "open" (defensive). Pure + table-tested.
func inboxKeepsProblem(problemStatus, inboxStatus string) bool {
	if inboxStatus == "all" {
		return true
	}
	return problemStatus != "resolved"
}

// inboxDoneStatuses is the ONE definition of "finished, stop showing it",
// shared by the inbox list and the inbox badge, for Problems and Incidents.
//
// v0.9.322 — sharing it is the point. The badge narrowed in SQL while the
// list fetched EVERY status and dropped the resolved ones in Go after the
// LIMIT. On an install with a long history the two cannot agree, and locally
// they didn't: badge 29, list 2.
//
// It is an EXCLUSION rather than an allow-list because that is what the Go
// keepers already say — "anything that isn't resolved still needs a human".
// An allow-list would silently drop a row with an unrecognised or empty
// status, which is exactly the defensive case those keepers exist for; my
// first attempt at this fix used one and the agreement test caught it.
var inboxDoneStatuses = []string{"resolved"}

// pickExcludedStatuses returns the SQL-side exclusion for the given inbox
// pivot. nil on "all" — that pivot exists to show resolved rows. The Go-side
// keepers still run afterwards: they are cheap, and they keep the pivot
// semantics readable in one place rather than only in SQL.
func pickExcludedStatuses(inboxStatus string) []string {
	if inboxStatus == "all" {
		return nil
	}
	return inboxDoneStatuses
}

func pickStatus(inboxStatus string) string {
	if inboxStatus == "all" {
		return ""
	}
	// ProblemFilter.Status takes a single value — "open" picks
	// open only, missing acknowledged. The multi-value narrow now rides on
	// Statuses (v0.9.322); this stays empty so the two don't AND together
	// into "open AND (open|acknowledged)".
	return ""
}

func pickExceptionState(inboxStatus string) string {
	switch inboxStatus {
	case "ignored":
		// v0.9.254 — the ONLY pivot that reaches silenced groups. The
		// store's default view (and therefore "all") excludes them.
		return chstore.ExStateIgnored
	case "all":
		return "" // store default — still excludes 'ignored'
	default:
		return "open"
	}
}

func priorityRank(p string) int {
	switch p {
	case "P1":
		return 3
	case "P2":
		return 2
	case "P3":
		return 1
	}
	return 0
}

// problemToInbox normalises a Problem row. Priority +
// PriorityReason ride through verbatim — enrichment already
// computed them so the inbox bucket matches the /problems
// page bucket exactly.
func problemToInbox(p chstore.Problem) InboxItem {
	// (Resolved-row filtering happens at the call site via inboxKeepsProblem —
	// v0.8.287; this normaliser assumes the row already passed that gate.)
	return InboxItem{
		ID:             "problem:" + p.ID,
		Kind:           "problem",
		Source:         "Alert rule",
		Priority:       p.Priority,
		PriorityReason: p.PriorityReason,
		Severity:       p.Severity,
		Service:        p.Service,
		SubjectKind:    p.Kind, // v0.9.1339 — özne TÜRÜ, Kind (kaynak) DEĞİL
		Title:          p.RuleName,
		Description:    p.Description,
		StartedAt:      p.StartedAt,
		LastSeen:       p.StartedAt,
		Assignee:       p.Assignee,
		Status:         p.Status,
		Clusters:       p.Clusters,
		// v0.9.255 — see the field comments: these were computed by the
		// enrichment chain in listInbox and then discarded here.
		RunbookURL:   p.RunbookURL,
		RecentDeploy: p.RecentDeploy,
		// v0.9.530 — aynı sınıf: ListProblems bunu zaten SELECT ediyordu.
		AISummary:   inboxTruncate(p.AISummary, inboxAISummaryMax),
		AISummaryAt: p.AISummaryAt,
		Problem: &InboxProblemRef{
			ID: p.ID, RuleID: p.RuleID, Metric: p.Metric,
			Value: p.Value, Threshold: p.Threshold,
		},
	}
}

// exceptionToInbox derives a triage priority for an exception
// group from occurrences (volume) + recency. The signals we
// have on an exception are different from those on a Problem,
// so the formula is bespoke but the bucket targets the same
// "now / today / when-convenient" semantics:
//
//	P1 — fresh (last_seen ≤ 5min) AND occurrences ≥ 500
//	P2 — fresh (last_seen ≤ 1h)   AND occurrences ≥ 100
//	     OR regressed (state="regressed")
//	P3 — everything else
//
// "Fresh + high-volume" is the post-deploy spike pattern the
// oncall most wants to see; everything else is review-able
// later.
func exceptionToInbox(g chstore.ExceptionGroup) InboxItem {
	prio, reason := exceptionPriority(g)
	// We don't currently carry severity on the exception_groups
	// row; surface "—" so the column doesn't lie. Downstream UI
	// renders this as text not a badge.
	// v0.9.443 — sınıf ayrımı: çıplak 3-haneli tip = HTTP-hata grubu.
	// ID öneki her iki sınıfta "exception:" kalır — drawer deep-link'leri
	// (?exc=) ve toplu-ack parmak izi yolu tür değil payload okur.
	kind, source := "exception", "Exception"
	if isHTTPErrorType(g.Type) {
		kind, source = "httperror", "HTTP error"
	}
	return InboxItem{
		ID:             "exception:" + g.Fingerprint,
		Kind:           kind,
		Source:         source,
		Priority:       prio,
		PriorityReason: reason,
		Severity:       "warning", // best fit until exception rows carry one
		Service:        g.Service,
		Title:          g.Type,
		Description:    inboxTruncate(g.Message, 240),
		StartedAt:      g.FirstSeen,
		LastSeen:       g.LastSeen,
		Assignee:       g.Assignee,
		Status:         g.State,
		// v0.9.530 — ExceptionExplainer'ın özeti. Varsayılan Inbox
		// görünümünde (P1 + kind=exception) operatörün gördüğü TEK
		// AI cümlesi bu; problem satırları P3 kovasında kalıyor.
		AISummary:   inboxTruncate(g.AISummary, inboxAISummaryMax),
		AISummaryAt: g.AISummaryAt,
		Exception: &InboxExceptionRef{
			Fingerprint: g.Fingerprint, Type: g.Type, Message: g.Message,
			Occurrences: g.Occurrences,
		},
	}
}

// isHTTPErrorType — chstore.HTTPErrorTypeRe'nin Go tarafı; desen tek
// kaynaktan gelir ki CH süzgeci (WHERE match) ile satır sınıflandırması
// asla ayrışmasın.
var httpErrorTypeGo = regexp.MustCompile(chstore.HTTPErrorTypeRe)

func isHTTPErrorType(exType string) bool {
	return httpErrorTypeGo.MatchString(exType)
}

// fmtThousands — binlik ayraçlı sayı; "18217 total" yerine "18,217 total".
func fmtThousands(n uint64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// normalizeInboxSince — yalnız sabit basamaklar; bilinmeyen değer "".
// Saf + tablo-testli.
//
// v0.9.954 (UX denetimi F5 / Ö13) — en dar basamak 2h'ten 30m'e indi.
// Inbox "ne oldu?" sorusunun doğal girişi ama "şu 20 dakikada ortaya
// çıkanlar" KURULAMIYORDU: bir olayın hemen ardından bakan operatör en
// dar seçenekte bile 2 saatlik gürültüyü birlikte alıyordu.
//
// TAM CUSTOM PENCERE BİLİNÇLİ OLARAK YOK: bu değer sunucu cache
// anahtarına giriyor, serbest bir pencere kardinaliteyi patlatırdı
// (v0.8.270). Sabit basamak sayısı 3'ten 5'e çıktı, o kadar.
//
// İKİ FONKSİYON AYNI KÜMEYİ TANIMAK ZORUNDA — biri tanıyıp öteki
// tanımazsa filtre "geçerli" sayılır ama süre 0 döner, yani seçenek
// SESSİZCE "hepsi" gibi davranırdı. Tablo testi ikisini birlikte
// gezer.
func normalizeInboxSince(v string) string {
	switch v {
	case "30m", "1h", "2h", "24h", "7d":
		return v
	}
	return ""
}

func inboxSinceDuration(v string) time.Duration {
	switch v {
	case "30m":
		return 30 * time.Minute
	case "1h":
		return time.Hour
	case "2h":
		return 2 * time.Hour
	case "24h":
		return 24 * time.Hour
	case "7d":
		return 7 * 24 * time.Hour
	}
	return 0
}

// inboxSinceRungs — TEK doğruluk kaynağı: hem testin gezdiği küme hem de
// "iki fonksiyon ayrışmasın" kapısının girdisi. Sıra DAR → GENİŞ.
var inboxSinceRungs = []string{"30m", "1h", "2h", "24h", "7d"}

// exceptionPriority — satır başına çağrılan sarmalayıcı. Pencereler
// paket-global atomik config'ten okunur (exception_triage.go); burada CH
// okuması YOK, aksi hâlde 200 satırlık bir inbox listesi 200 ayar
// sorgusu demek olurdu.
func exceptionPriority(g chstore.ExceptionGroup) (string, string) {
	return exceptionPriorityAt(g, currentExceptionTriage(), time.Now())
}

// exceptionPriorityAt — SAF çekirdek (config + "şimdi" dışarıdan).
// Tablo testleri buradan geçer; sarmalayıcı yalnız zamanı ve ayarı
// bağlar.
func exceptionPriorityAt(g chstore.ExceptionGroup, cfg chstore.ExceptionTriageConfig, now time.Time) (string, string) {
	// v0.9.1188 — kelepçe BURADA. Pencereler kendi P1Window()/P2Window()
	// çağrılarında normalize oluyordu ama patlama eşikleri HAM alandan
	// okunuyor: sıfır-değerli bir config (elle kurulmuş bir test, hidrate
	// edilmemiş bir çağıran) eşiği 0/dk yapar, her grubu patlama sayar ve
	// gerekçeye "patlama eşiği 0/dk" yazardı.
	cfg = chstore.NormalizeExceptionTriage(cfg)
	age := now.UnixNano() - g.LastSeen
	freshMin := time.Duration(age) <= 5*time.Minute
	// v0.9.775 — adı artık pencerenin DEĞERİNİ değil ANLAMINI söylüyor.
	// "freshHour" idi ve pencere 4 saate çıkınca isim yalan olurdu; bu
	// sabitin üç kez ötelendiği bir yerde ismin değere çakılı olması
	// başlı başına bir tuzak.
	p1Window := cfg.P1Window()
	fresh := time.Duration(age) <= p1Window

	// v0.9.627 — operatör-bildirimli: tek servisten 12 dakikada 11.260
	// olay P2 göründü.
	//
	// Sebep iki katmanlıydı. (a) P1'in TEK kapısı "son 5 dakika içinde
	// görülmüş" idi; patlama 13:01'de bitmişti, operatör 13:22'de baktı,
	// 21 dakikalık yaş kapıyı kapattı. (b) HIZ diye bir kavram yoktu —
	// 11.260 ile 110 arasındaki fark yalnız eşik karşılaştırmasına
	// giriyordu, birim zamana değil.
	//
	// Yirmi dakika önce biten 11 binlik bir patlama hâlâ P1: olay
	// bitti diye etkisi bitmiyor, ve oncall'ın onu bir sonraki nöbet
	// devrinde değil ŞİMDİ görmesi gerekiyor.
	//
	// Hız grubun KENDİ ömründen türüyor (last_seen − first_seen) —
	// v0.9.524'ün dersi gereği elimizde OLMAYAN pencereli bir sayı
	// uydurmuyoruz. "12 dakikada 11.260" birebir doğru bir cümle.
	//
	// Bu kontrol `regressed` erken-dönüşünden ÖNCE: regressed bir grup
	// dakikada 938 olay üretiyorsa "regressed" etiketi onu P2'de
	// tutmamalı; etiket problemin GEÇMİŞİNİ anlatır, ŞİDDETİNİ değil.
	// v0.9.699 — operatör-bildirimli: "cm-put-service problemi P1 ya da
	// P2'ydi, sonra P3'e düştü neden?"
	//
	//	101.132 olay / 2dk58sn → ~34.000/dk (P1 hız eşiğinin 170 KATI)
	//	22:12:07'de bitti · 23:18'de bakıldı → 66 dk
	//
	// AYNI SINIF, İKİNCİ KEZ. v0.9.627'de şikâyet "12 dakikada 11.260
	// olay P2 göründü" idi; P1 kapısındaki tazelik penceresini 5 dk'dan
	// 1 saate TAŞIDIM. Asıl kusur pencerenin dar olması değil, UÇURUMUN
	// KENDİSİYDİ: eşiği öteleyince operatör bir çentik ötede aynı duvara
	// çarpıyor. Bu sefer daha kötüsü oldu — P1'den P2'ye değil, doğrudan
	// P3'e düştü, çünkü P2 kapıları da freshHour'a bağlıydı.
	//
	// CLAUDE.md'nin kendi tanımı zaten cevabı veriyor:
	// P1 "şimdi", P2 "bugün", P3 "sırası gelince". Bir saat önce biten
	// 101 binlik bir patlama tanımı gereği BUGÜN'dür — P3 olamaz.
	//
	// Yeni kural: patlama şiddeti bir OLGU, tazelik ise ONA ERİŞİM
	// aciliyeti. Şiddet zamanla silinmiyor, yalnız aciliyeti düşüyor:
	//   taze (≤1sa) → P1 · aynı gün (≤24sa) → P2 · sonrası → P3
	// Uçurum yerine basamak. Gerekçe her basamakta patlamanın gerçek
	// büyüklüğünü taşıyor, "steady" gibi yanlış bir cümle değil.
	//
	// v0.9.775 — operatör-bildirimli, AYNI SINIF ÜÇÜNCÜ KEZ:
	//
	//	22:37'de biten 1.575'lik patlama, 00:22'de bakıldı (1sa 45dk)
	//	→ P2 göründü, beklenen P1
	//	191'lik SQLTimeout 1 saati aşınca P2 → P3 oldu, beklenen P2
	//
	// Basamak fikri doğruydu, basamağın YERİ dardı. İki değişiklik:
	// (a) P1 tazeliği varsayılan olarak 4 saat — problem tarafındaki
	// "critical open ≥4h → P1" ile simetrik, ve gece nöbetinde iki
	// saat önce biten bir patlamayı hâlâ "şimdi" sayacak kadar geniş.
	// (b) Pencereler artık system_settings'te (exception_triage):
	// dördüncü vaka bir sürüm değil bir ayar değişikliği olsun.
	// v0.9.1205 (operatör-bildirimli) — AYNI SINIF BEŞİNCİ KEZ, ve bu kez
	// pencere oynatmıyoruz: MERDİVENİN P1 FELSEFESİ DEĞİŞTİ.
	//
	// Tarihçe: 627 (5dk→1sa) → 699 (uçurum→basamak) → 775 (1sa→4sa +
	// vida) → 1189 (hacim kapısı da pencereye) → bugün. Dört düzeltme de
	// "şiddet OLGU, tazelik ERİŞİM aciliyeti — aciliyet zamanla düşer"
	// ilkesini korudu ve operatör dördünde de aynı duvara çarptı: gece
	// biten P1-şiddetinde bir olay, sabah bakıldığında P2/P3'e gömülmüş.
	//
	// Operatör direktifi (2026-08-21): "Regressed olan sorun bittiyse
	// P2/P3 dönmesin, hep P1 kalsın." Yani: P1'İ HAK ETMİŞ bir grup, ele
	// alınana (resolve/ignore) dek P1 KALIR — akışın bitmiş olması
	// önceliği DÜŞÜRMEZ, yalnız gerekçeye yazılır. Kuyruktan düşürmenin
	// yolu zaman değil, operatör aksiyonudur. "Öncelik düşebilir, cümle
	// yalan olamaz" kuralının P1 yarısı artık "öncelik de düşmez"dir;
	// cümle dürüstlüğü aynen sürüyor ("· N önce bitti").
	//
	// P1-altı basamaklar (P2/P3) ESKİSİ gibi zamanla iner — direktif
	// yalnız P1'i çiviliyor.
	burst := exceptionIsBurst(g.Occurrences, g.FirstSeen, g.LastSeen, cfg)
	burstDesc := ""
	if burst {
		burstDesc = fmt.Sprintf("%s olay / %s (~%.0f/dk)",
			fmtThousands(g.Occurrences),
			shortDur(time.Duration(g.LastSeen-g.FirstSeen)),
			exceptionBurstRate(g.Occurrences, g.FirstSeen, g.LastSeen))
		if fresh {
			return "P1", burstDesc
		}
		// Bitiş yaşı gerekçede: satıra bakan operatör "devam ediyor mu"
		// sorusunun cevabını da görsün.
		return "P1", burstDesc + " · " + shortDur(time.Duration(age)) + " önce bitti"
	}

	if g.State == "regressed" {
		return "P2", "regressed"
	}
	// v0.9.524 — operatör-bildirimli: "28 Haziran'daki problemde bile
	// aynı sayı yazıyor". Cümle YANLIŞTI: freshMin grubun SON GÖRÜLME
	// zamanının 5 dk içinde olduğunu söyler, g.Occurrences ise ÖMÜR BOYU
	// toplamdır. İkisini "N in last 5min" diye birleştirmek, 35 gündür
	// biriken 18.217 occurrence'ı son 5 dakikada olmuş gibi gösteriyordu.
	//
	// Gerçek pencereli sayı elimizde YOK (ExceptionGroup yalnız
	// first_seen/last_seen/toplam taşır) ve onu üretmek grup başına yeni
	// bir sorgu demek — v0.9.522/523'te tam o sınıfı azalttık. Doğru
	// çözüm sayıyı uydurmak değil, elimizdeki iki gerçeği DOĞRU cümleyle
	// söylemek: tazelik ayrı, toplam ayrı.
	// v0.9.1189 (operatör-bildirimli) — HACİM KAPISI 5 DAKİKALIK UÇURUMDAN
	// ÇIKTI.
	//
	// Bildirilen vaka: mobil giriş servisinde 888 olay, son görülme 1sa12dk
	// önce. `freshMin` (≤5dk) kapalı olduğu için P1 olamadı; 888 < 1000
	// olduğu için patlama da sayılmadı → P2'de kaldı. Aynı grup beş dakika
	// önce durmuş olsaydı P1'di.
	//
	// v0.9.699 bu uçurumu ZATEN yanlış ilan etmişti — "şiddet bir OLGU,
	// tazelik ONA ERİŞİM aciliyeti" — ama düzeltmeyi yalnız PATLAMA yolunda
	// yaptı. Hacim yolu 5 dakikada kaldı ve aynı sınıf oradan geri döndü.
	// Kapı artık diğerleriyle AYNI pencerede (P1FreshHours) ve eşik de
	// ayarlanabilir (P1MinOccurrences).
	//
	// Gerekçe hâlâ "hâlâ akıyor mu" ayrımını taşıyor: ikisi operatör için
	// farklı şeyler ("şu an devam ediyor" ≠ "iki saat önce bitti"), ve bu
	// deponun kuralı gereği cümle olguyu doğru söylemeli.
	// v0.9.1205 — hacim kapısında pencere-dışı kalıcılık: YOĞUN bir
	// bölüm yaşamış (ömür hızı ≥ patlama eşiğinin yarısı — "neredeyse
	// patlama" P3 dalıyla aynı çizgi) ve eşiği aşmış grup, akışı bitse
	// de P1 KALIR (direktif). Kronik damlama (düşük hız, büyük ömür
	// toplamı) ESKİ davranışında: aktifken taze kapılardan P1/P2, ölünce
	// basamaklara iner — 35 günlük 11 binlik bir damlanın ölümü
	// operatörün kastettiği "bitmiş sorun" değil ve onu sonsuza dek P1
	// tutmak kuyruğu şişirirdi (steady-pini bilinçli korunuyor).
	if g.Occurrences >= uint64(cfg.P1MinOccurrences) {
		if freshMin {
			// v0.9.524'ün sözleşmesi BİREBİR korunuyor (inbox_test.go bu
			// metni çiviliyor): tazelik ayrı, toplamın TOPLAM olduğu ayrı.
			return "P1", fmt.Sprintf("active in last 5min · %s total", fmtThousands(g.Occurrences))
		}
		intense := exceptionBurstRate(g.Occurrences, g.FirstSeen, g.LastSeen) >= cfg.BurstMinRate/2
		if fresh || intense {
			// Durmuş hâli: aynı iki gerçek, artı ne zaman durduğu — "şu
			// an devam ediyor" ile "iki saat önce bitti" operatör için
			// farklı şeyler ve cümle bunu söylemeli.
			return "P1", fmt.Sprintf("%s total · stopped %s ago",
				fmtThousands(g.Occurrences), shortDur(time.Duration(age)))
		}
	}
	// v0.9.775 — bu kapı da AYNI pencereye bağlandı. Operatörün ikinci
	// şikâyeti tam buydu: 191 olaylık SQLTimeout grubu 1sa50dk'da
	// pencereyi aşınca P2'den doğrudan P3'e düştü. İki kapının ayrı
	// sabitlere bağlı olması, v0.9.699'da patlama tarafında düzelttiğim
	// uçurumun patlama-DEĞİL tarafında hâlâ durduğu anlamına geliyordu.
	//
	// Gerekçe metni pencereyi SÖYLER, "last hour" diye sabitlemez:
	// pencere ayarlanabilir olduğu an "seen in last hour" bir yalana
	// dönüşürdü ve bu depoda kural açık — öncelik düşebilir, cümle
	// yalan olamaz (v0.9.524, v0.9.699).
	if fresh && g.Occurrences >= 100 {
		return "P2", fmt.Sprintf("seen in last %s · %s total",
			shortDur(p1Window), fmtThousands(g.Occurrences))
	}
	// (v0.9.1205 — buradaki "24 saati geçmiş patlama → P3" dalı öldü:
	// patlama artık yukarıda koşulsuz P1 dönüyor. burstDesc yalnız o
	// dalda yaşıyor; derleyici ölü kodu bırakmasın diye dal silindi.)
	// v0.9.1188 (operatör-bildirimli) — "steady" YALNIZ gerçekten öyle
	// olanlar için, ve patlama KAPISINI KAÇIRAN bir grup da öyle değildir.
	//
	// Bildirilen satır: 2.374 olay / 13dk 09sn = 180,5/dk. Eski gömülü kapı
	// 200/dk olduğu için burst=false çıktı, sonra 8 saatlik yaş diğer bütün
	// kapıları kapattı ve satır "steady" gerekçesiyle P3'e düştü. Öncelik
	// tartışılır; CÜMLE tartışılmaz — 13 dakikada 2.374 olay hiçbir okumada
	// "steady" değildir. Bu deponun kuralı: öncelik düşebilir, cümle yalan
	// olamaz (v0.9.524, v0.9.699).
	//
	// Eşik ayarlanabilir olduğuna göre kapıyı kaçıran her grup bir sonraki
	// ayar değişikliğinin adayıdır; gerekçe bunu SÖYLEMELİ ki operatör
	// vidayı nereye çevireceğini görebilsin. Kapıyı gerçekten uzaktan
	// kaçıranlar (kronik, düşük hızlı gruplar) eskisi gibi "steady".
	if rate := exceptionBurstRate(g.Occurrences, g.FirstSeen, g.LastSeen); rate >= cfg.BurstMinRate/2 {
		return "P3", fmt.Sprintf("%s olay / %s (~%.0f/dk · patlama eşiği %.0f/dk)",
			fmtThousands(g.Occurrences),
			shortDur(time.Duration(g.LastSeen-g.FirstSeen)),
			rate, cfg.BurstMinRate)
	}
	return "P3", "steady"
}

// anomalyToInbox maps a detection score into a priority bucket.
// peak_ratio is how far above the historical baseline this
// metric got at its worst; current_ratio is right now. We use
// the peak for ranking — "worst hit so far" predicts how much
// the operator should care, even if the burst has subsided.
//
//	P1 — peak ≥ 5x baseline (extraordinary spike)
//	P2 — peak ≥ 2x baseline (clear anomaly worth a look)
//	P3 — everything else (mostly cleared / mild)
//
// Anomaly events don't have severity in the data model, so we
// derive one from the ratio bucket and surface it for visual
// consistency only.
func anomalyToInbox(e chstore.AnomalyEvent) InboxItem {
	prio, reason := anomalyPriority(e)
	sev := "info"
	if prio == "P1" {
		sev = "critical"
	} else if prio == "P2" {
		sev = "warning"
	}
	return InboxItem{
		ID:             "anomaly:" + e.ID,
		Kind:           "anomaly",
		Source:         "Anomaly",
		Priority:       prio,
		PriorityReason: reason,
		Severity:       sev,
		Service:        e.Service,
		Title:          fmt.Sprintf("%s · %s", e.Kind, e.Pattern),
		Description:    inboxTruncate(e.Sample, 240),
		StartedAt:      e.StartedAt,
		LastSeen:       e.LastSeen,
		Status:         e.Status,
		Clusters:       e.Clusters,
		Anomaly: &InboxAnomalyRef{
			ID: e.ID, Kind: e.Kind, Pattern: e.Pattern,
			PeakRatio: e.PeakRatio, CurrentRatio: e.CurrentRatio,
		},
	}
}

func anomalyPriority(e chstore.AnomalyEvent) (string, string) {
	if e.Status == "cleared" {
		return "P3", "cleared"
	}
	if math.IsNaN(e.PeakRatio) || e.PeakRatio <= 0 {
		return "P3", "no signal"
	}
	switch {
	case e.PeakRatio >= 5:
		return "P1", fmt.Sprintf("%.1fx baseline", e.PeakRatio)
	case e.PeakRatio >= 2:
		return "P2", fmt.Sprintf("%.1fx baseline", e.PeakRatio)
	default:
		return "P3", "mild"
	}
}

// inboxTruncate caps a string at n characters; the package's
// generic truncate() lives in api.go and has a different signature.
// inboxAISummaryMax — satırdaki AI özetinin bayt bütçesi (v0.9.530).
//
// Özet çok bölümlü bir blok ("Olası neden: / Kanıt: … / İlk kontroller:
// …"), ~700 karakter. Tamamını satıra basmak hem tabloyu şişirir hem
// Redis'e ve L1'e taşınır; satırın işi TARAMA, tam metnin yeri detay
// yüzeyi. 240, mevcut g.Message kırpmasıyla aynı bütçe — aynı satırda
// iki farklı kırpma eşiği tutarsız görünürdü.
const inboxAISummaryMax = 240

// v0.9.530 — kırpma RUNE sınırında yapılır, bayt sınırında değil.
//
// Eski hâli `s[:n]` idi ve çok baytlı bir karakteri ORTADAN bölebiliyordu:
// Türkçe metinde ç/ğ/ı/ö/ş/ü hepsi 2 bayt, yani 240. baytın bir runenin
// ortasına düşme olasılığı yüksek. Bozuk bayt JSON'a girince Go onu
// U+FFFD'ye çevirir ve operatör triage satırında "�" görür. Exception
// mesajları çoğunlukla ASCII olduğu için bu bugüne dek görünmedi; AI
// özeti Türkçe düzyazı olduğu için artık görünürdü.
func inboxTruncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// n bayta kadar TAM runeleri al.
	cut := 0
	for i := range s {
		if i > n {
			break
		}
		cut = i
	}
	return strings.TrimRight(s[:cut], " \n\t") + "…"
}
