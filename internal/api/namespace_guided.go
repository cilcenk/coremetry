package api

// namespace_guided.go — v0.10.470 (CoSRE Telemetry Agent Faz 2, F2-3): kabul
// kriteri 1 — "X namespace'indeki servisleri getir" → hangi cluster'larda X
// namespace'i var, altındaki workload'lar (tür, pod sayısı, telemetri var/yok,
// service.name'leri) ve namespace'te görülen service.name'ler; LLM YOK,
// deterministik kart + tablo + deep-link (open_page / find_entity emsali).
//
// İkinci parça: UCUZ VARLIK TARAMASI (audit G8 / parite D8). Router hiçbir
// niyet tanımadığında ve mesaj kısa ad-şekilliyse ("shop-payment") soru
// LLM'e düşmeden ÖNCE katalog indeksine (mcptools.ResolveEntityText, 60 s
// cache) sorulur: servis → mevcut kart, namespace → bu kart, karışık →
// cluster'lı aday çipleri; hiçbir şey yoksa eski yol (serbest döngü) aynen.
//
// Okuma çekirdeği tool'larla ORTAK (mcptools.ReadNamespaceOverview /
// ReadNamespaces): sohbet kartı ile list_workloads aynı satırları görür.

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/entity"
	"github.com/cilcenk/coremetry/internal/mcptools"
)

const guidedNamespaceServices guidedIntent = "namespace_services"

// namespaceWords — namespace / OpenShift proje sözcüğü (ek artıkları
// apostroftan kopar: "namespace'indeki" → "namespace", "indeki").
var namespaceWords = map[string]bool{"namespace": true, "namespaces": true, "ns": true, "proje": true, "projesi": true, "project": true}

// isNamespaceWord — tam sözcük ya da ekli hâli ("projesindeki", "namespacedeki").
func isNamespaceWord(t string) bool {
	return namespaceWords[t] || strings.HasPrefix(t, "namespace") || strings.HasPrefix(t, "proje") || strings.HasPrefix(t, "project")
}

func hasNamespaceWord(toks []string) bool {
	for _, t := range toks {
		if isNamespaceWord(t) {
			return true
		}
	}
	return false
}

// namespaceNameNear — namespace sözcüğünün hemen ÖNÜNDEKİ ya da ARDINDAKİ
// ad-şekilli jeton ("shop namespace'indeki", "namespace shop").
func namespaceNameNear(toks []string) string {
	for i, t := range toks {
		if !isNamespaceWord(t) {
			continue
		}
		if i > 0 && nameLikeToken(toks[i-1]) {
			return toks[i-1]
		}
		if i+1 < len(toks) && nameLikeToken(toks[i+1]) {
			return toks[i+1]
		}
	}
	return ""
}

func nameLikeToken(t string) bool {
	return len(t) >= 3 && asciiNameToken(t) && !guidedStopwords[t] && !findFiller[t] && !findVerbs[t] && !isNamespaceWord(t) && !findSuffixDebris[t]
}

// routeNamespaceAsk — "X namespace'i", "X namespace'indeki servisler(i getir)",
// "namespace X", "namespace'leri listele" / "hangi namespace'ler var".
func routeNamespaceAsk(toks []string, env string) (guidedRoute, bool) {
	if !hasNamespaceWord(toks) {
		return guidedRoute{}, false
	}
	if name := namespaceNameNear(toks); name != "" {
		return guidedRoute{Intent: guidedNamespaceServices, FindQuery: name, Env: env}, true
	}
	// Ad yok: liste sorusu mu? (listele / hangi / var / kaç / neler)
	for _, t := range toks {
		if findVerbs[t] || t == "var" || t == "neler" || t == "kaç" || t == "hangi" || t == "hangileri" {
			return guidedRoute{Intent: guidedNamespaceServices, FindList: true, Env: env}, true
		}
	}
	return guidedRoute{}, false
}

// entityScanRoute — v0.10.470 (G8): router none dedi; mesaj ≤3 ad-şekilli
// jetonsa katalog taraması için find_entity (FindQuery) — çözüm handler'da
// (ctx gerekir). Sohbet sözcükleri ("evet", "tamam") da geçer ama indeks
// 60 s cache'li ve boş sonuç eski yola düşer; maliyet bir map araması.
func entityScanRoute(norm string) (guidedRoute, bool) {
	toks := guidedTokens(norm)
	if len(toks) == 0 || len(toks) > 3 {
		return guidedRoute{}, false
	}
	named := false
	for _, t := range toks {
		if findSuffixDebris[t] || len(t) <= 2 {
			continue
		}
		if !nameLikeToken(t) {
			return guidedRoute{}, false
		}
		named = true
	}
	if !named {
		return guidedRoute{}, false
	}
	return guidedRoute{Intent: guidedFindEntity, FindQuery: strings.TrimSpace(norm)}, true
}

// ── render (SAF) ───────────────────────────────────────────────

func fmtInt64(n int64) string { return fmt.Sprintf("%d", n) }

func renderNamespaceCard(ov mcptools.NamespaceOverview, rangeS int64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s** namespace'i", ov.Namespace)
	// Namespace'in gerçekten bulunduğu cluster'lar = workload ya da servis satırı olanlar.
	seen := map[string]bool{}
	var present []string
	for _, w := range ov.Workloads {
		if !seen[w.Cluster] {
			seen[w.Cluster] = true
			present = append(present, w.Cluster)
		}
	}
	for _, s := range ov.Services {
		if !seen[s.Cluster] {
			seen[s.Cluster] = true
			present = append(present, s.Cluster)
		}
	}
	sort.Strings(present)
	if len(present) > 0 {
		fmt.Fprintf(&b, " · cluster: %s", strings.Join(present, ", "))
	}
	fmt.Fprintf(&b, " · son %s\n\n", fmtAgoTR(rangeS))
	if len(ov.Workloads) > 0 {
		b.WriteString("| Cluster | Workload | Tür | Pod | Telemetri | service.name |\n|---|---|---|---:|---|---|\n")
		for _, w := range ov.Workloads {
			tel := "yok"
			if w.Telemetry {
				tel = fmt.Sprintf("var (%s span, %s hata)", fmtInt64(w.Spans), fmtInt64(w.Errors))
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %d | %s | %s |\n", w.Cluster, w.Workload, w.Kind, w.Pods, tel, strings.Join(w.Services, ", "))
		}
	} else {
		b.WriteString("Katalogda workload yok")
		if ov.OrphanPods > 0 {
			fmt.Fprintf(&b, " (Thanos/KSM tanımsız ya da namespace süzgeci dışında); span kaynaklı %d pod var", ov.OrphanPods)
		}
		b.WriteString(".\n")
	}
	if len(ov.Services) > 0 {
		b.WriteString("\nNamespace'te görülen service.name'ler:\n\n| Cluster | service.name | Pod | Span | Hata |\n|---|---|---:|---:|---:|\n")
		for _, s := range ov.Services {
			fmt.Fprintf(&b, "| %s | %s | %d | %s | %s |\n", s.Cluster, s.Service, s.Pods, fmtInt64(s.Spans), fmtInt64(s.Errors))
		}
	} else {
		fmt.Fprintf(&b, "\nSon %s içinde bu namespace'ten span gelmedi (workload var ama telemetri yok ≠ namespace yok).\n", fmtAgoTR(rangeS))
	}
	if ov.OrphanPods > 0 && len(ov.Workloads) > 0 {
		fmt.Fprintf(&b, "\nNot: %d pod yalnız span'den biliniyor (katalogda sahibi yok).\n", ov.OrphanPods)
	}
	return b.String()
}

func renderNamespaceList(rows []mcptools.NamespaceRow, searched []string) string {
	if len(rows) == 0 {
		return fmt.Sprintf("Katalogda namespace yok (aranan cluster'lar: %s).", strings.Join(searched, ", "))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**%d namespace** (%d cluster):\n\n| Cluster | Namespace | Workload | Pod |\n|---|---|---:|---:|\n", len(rows), len(searched))
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %s | %d | %d |\n", r.Cluster, r.Namespace, r.Workloads, r.Pods)
	}
	return b.String()
}

// renderEntityCandidates — karışık aday listesi (tür + cluster + namespace).
func renderEntityCandidates(q string, cands []mcptools.EntityCandidate) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%q birden çok varlığa oturuyor (%d aday). Hangisini kastettin?\n\n| Tür | Ad | Cluster | Namespace |\n|---|---|---|---|\n", q, len(cands))
	for _, c := range cands {
		kind := c.Kind
		if c.WlKind != "" {
			kind = c.Kind + " (" + c.WlKind + ")"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", kind, c.Name, orDash(c.Cluster), orDash(c.Namespace))
	}
	b.WriteString("\nServis ya da namespace çipine tıkla; workload için pod'larını `X namespace'indeki servisler` ile gör.\n")
	return b.String()
}

// entityCandidateChips — round-trip eden çipler: servis → çıplak ad
// (find_entity), namespace → "X namespace'i" (namespace_services).
func entityCandidateChips(cands []mcptools.EntityCandidate) []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range cands {
		var chip string
		switch c.Kind {
		case entity.TypeService:
			chip = c.Name
		case entity.TypeNamespace:
			chip = c.Name + " namespace'i"
		default:
			continue
		}
		if !seen[chip] {
			seen[chip] = true
			out = append(out, chip)
		}
		if len(out) >= guidedServiceAskMax {
			break
		}
	}
	return out
}

func namespaceLinks(ov mcptools.NamespaceOverview, deps mcptools.Deps) []guidedAnswerLink {
	var links []guidedAnswerLink
	byName := map[string]string{}
	if deps.Clusters != nil {
		for _, c := range deps.Clusters() {
			byName[c.Name] = c.ID
		}
	}
	seen := map[string]bool{}
	for _, w := range ov.Workloads {
		if id := byName[w.Cluster]; id != "" && !seen[w.Cluster] {
			seen[w.Cluster] = true
			links = append(links, guidedAnswerLink{Label: w.Cluster + " · " + ov.Namespace, Href: "/clusters?cluster=" + url.QueryEscape(id) + "&ns=" + url.QueryEscape(id+"|"+ov.Namespace)})
		}
	}
	for i, s := range ov.Services {
		if i >= 3 {
			break
		}
		links = append(links, guidedAnswerLink{Label: s.Service + " · Overview", Href: "/service?name=" + url.QueryEscape(s.Service)})
	}
	return links
}

// ── handler'lar (deterministik, LLM yok) ───────────────────────

func (s *Server) guidedNamespaceServicesAnswer(ctx context.Context, emit func(string, any), route guidedRoute, from, to time.Time, rangeS int64) (handled, ok bool) {
	deps := s.mcpDeps()
	if !mcptools.EntityLayerOn(deps) {
		emit("answer", map[string]any{"text": "Varlık katmanı bu kurulumda kapalı (Settings → Entities); namespace/workload kataloğu okunamıyor. Servis düzeyinde sorabilirsin.", "suggestions": []string{"Servisleri listele", "Açık problemler?"}})
		return true, true
	}
	win := linkWindowBetween(from, to)
	if route.FindList {
		n := emitGuidedStep(emit, "list_namespaces", `{}`)
		rows, searched, err := mcptools.ReadNamespaces(ctx, deps, "", "", 200)
		if err != nil {
			emitGuidedStepResult(emit, n, "list_namespaces", "", err)
			return false, false
		}
		text := renderNamespaceList(rows, searched)
		emitGuidedStepResult(emit, n, "list_namespaces", text, nil)
		var chips []string
		for i, r := range rows {
			if i >= 6 {
				break
			}
			chips = append(chips, r.Namespace+" namespace'i")
		}
		emit("answer", map[string]any{"text": text, "suggestions": chips, "links": []guidedAnswerLink{{Label: "Clusters", Href: "/clusters"}}})
		return true, true
	}
	q := strings.TrimSpace(route.FindQuery)
	if q == "" {
		return false, false
	}
	n := emitGuidedStep(emit, "resolve_entity", `{"text":`+jsonStr(q)+`,"kind":"namespace"}`)
	cands, _, err := mcptools.ResolveEntityText(ctx, deps, q, "", "")
	if err != nil {
		emitGuidedStepResult(emit, n, "resolve_entity", "", err)
		return false, false
	}
	var nss []mcptools.EntityCandidate
	for _, c := range cands {
		if c.Kind == entity.TypeNamespace {
			nss = append(nss, c)
		}
	}
	names := map[string]bool{}
	for _, c := range nss {
		names[c.Name] = true
	}
	switch {
	case len(nss) == 0:
		emitGuidedStepResult(emit, n, "resolve_entity", "namespace adayı yok", nil)
		text := fmt.Sprintf("Katalogda %q adında ya da ona yakın bir namespace yok.", q)
		chips := entityCandidateChips(cands)
		if len(chips) > 0 {
			text += " Yakın varlıklar aşağıdaki çiplerde."
		}
		emit("answer", map[string]any{"text": text, "suggestions": chips})
		return true, true
	case len(names) > 1:
		emitGuidedStepResult(emit, n, "resolve_entity", fmt.Sprintf("%d aday", len(nss)), nil)
		emit("answer", map[string]any{"text": renderEntityCandidates(q, nss), "suggestions": entityCandidateChips(nss)})
		return true, true
	}
	name := nss[0].Name
	emitGuidedStepResult(emit, n, "resolve_entity", "namespace: "+name, nil)
	n2 := emitGuidedStep(emit, "list_workloads", `{"namespace":`+jsonStr(name)+`}`)
	ov, err := mcptools.ReadNamespaceOverview(ctx, deps, name, "", "", "", from, to)
	if err != nil {
		emitGuidedStepResult(emit, n2, "list_workloads", "", err)
		return false, false
	}
	text := renderNamespaceCard(ov, rangeS)
	emitGuidedStepResult(emit, n2, "list_workloads", text, nil)
	var chips []string
	for i, sv := range ov.Services {
		if i >= 3 {
			break
		}
		chips = append(chips, sv.Service+" sağlığı nasıl?")
	}
	chips = append(chips, "Namespace'leri listele")
	emit("answer", map[string]any{"text": text, "suggestions": chips, "links": dedupLinksByHref(win.applyAll(namespaceLinks(ov, deps)))})
	return true, true
}

// guidedEntityScanAnswer — find_entity + yalnız FindQuery (router none →
// ucuz tarama). Servis → mevcut kart; namespace → namespace kartı; karışık
// → çipler; hiç → false (eski yol).
func (s *Server) guidedEntityScanAnswer(ctx context.Context, emit func(string, any), route guidedRoute, from, to time.Time, rangeS int64) (handled, ok bool) {
	q := strings.TrimSpace(route.FindQuery)
	if q == "" {
		return false, false
	}
	cands, _, err := mcptools.ResolveEntityText(ctx, s.mcpDeps(), q, "", "")
	if err != nil || len(cands) == 0 {
		return false, false
	}
	emitGuidedContextStep(emit, fmt.Sprintf("katalog: %q → %d aday", q, len(cands)))
	if one := mcptools.ResolvedOne(cands); one != nil {
		switch one.Kind {
		case entity.TypeService:
			return s.guidedFindEntityAnswer(ctx, emit, guidedRoute{Intent: guidedFindEntity, Service: one.Name, Env: route.Env}, from, to, rangeS)
		case entity.TypeNamespace:
			return s.guidedNamespaceServicesAnswer(ctx, emit, guidedRoute{Intent: guidedNamespaceServices, FindQuery: one.Name, Env: route.Env}, from, to, rangeS)
		}
	}
	// Aynı adlı namespace birden çok cluster'da → kart (cluster kolonu ayırır).
	nsNames := map[string]bool{}
	allNS := true
	for _, c := range cands {
		if c.Kind != entity.TypeNamespace {
			allNS = false
			break
		}
		nsNames[c.Name] = true
	}
	if allNS && len(nsNames) == 1 {
		return s.guidedNamespaceServicesAnswer(ctx, emit, guidedRoute{Intent: guidedNamespaceServices, FindQuery: cands[0].Name, Env: route.Env}, from, to, rangeS)
	}
	emit("answer", map[string]any{"text": renderEntityCandidates(q, cands), "suggestions": entityCandidateChips(cands)})
	return true, true
}
