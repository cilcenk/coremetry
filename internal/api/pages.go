package api

// AvailablePage is one entry in the canonical sidebar-page registry
// that powers the custom-role checkbox grid in Settings → Roles. The
// frontend Sidebar.tsx mirrors these IDs as `href` values; keeping
// the registry on the backend means the role catalog's page IDs are
// validated against a single source of truth instead of drifting
// when somebody adds a sidebar entry.
//
// IDs match the frontend route paths verbatim (e.g. "/inbox",
// "/services"). Group + label are i18n keys; the frontend resolves
// them via useT() so a language switch surfaces immediately.
//
// AdminOnly pages are excluded from this registry — custom roles
// subset viewer's access only; admin/editor surfaces remain gated
// by their hard-coded role checks.
type AvailablePage struct {
	ID    string `json:"id"`    // route path; matches Sidebar href
	Label string `json:"label"` // i18n key (e.g. "nav.inbox")
	Group string `json:"group"` // i18n key for group heading
}

// availablePages mirrors NAV_GROUPS in frontend/src/components/Sidebar.tsx
// minus every entry tagged adminOnly. When a new viewer-visible page
// lands in the sidebar, add it here too — the custom-role grid won't
// expose it otherwise (which is the safer default; new features stay
// hidden until an admin opts them in).
var availablePages = []AvailablePage{
	{ID: "/inbox", Label: "nav.inbox", Group: ""},

	{ID: "/incidents", Label: "nav.incidents", Group: "navGroup.triage"},
	{ID: "/problems", Label: "nav.problems", Group: "navGroup.triage"},
	{ID: "/anomalies", Label: "nav.anomalies", Group: "navGroup.triage"},

	{ID: "/services", Label: "nav.services", Group: "navGroup.services"},
	{ID: "/topology", Label: "nav.topology", Group: "navGroup.services"},
	{ID: "/databases", Label: "nav.databases", Group: "navGroup.services"},
	{ID: "/messaging", Label: "nav.messaging", Group: "navGroup.services"},

	{ID: "/traces", Label: "nav.traces", Group: "navGroup.signals"},
	{ID: "/metrics", Label: "nav.metrics", Group: "navGroup.signals"},
	{ID: "/logs", Label: "nav.logs", Group: "navGroup.signals"},
	{ID: "/profiling", Label: "nav.profiling", Group: "navGroup.signals"},

	{ID: "/explore", Label: "nav.explore", Group: "navGroup.workspaces"},
	{ID: "/notebook", Label: "nav.notebook", Group: "navGroup.workspaces"},
	{ID: "/dashboards", Label: "nav.dashboards", Group: "navGroup.workspaces"},

	{ID: "/alerts", Label: "nav.alerts", Group: "navGroup.alerting"},
	{ID: "/monitors", Label: "nav.monitors", Group: "navGroup.alerting"},
	{ID: "/slos", Label: "nav.slos", Group: "navGroup.alerting"},

	{ID: "/admin/stats", Label: "nav.system", Group: "navGroup.system"},
}

// availablePageIDs returns just the IDs as a lookup set — used by the
// upsert handler to reject unknown page IDs (catches typos before
// they silently lock a user out).
func availablePageIDs() map[string]struct{} {
	out := make(map[string]struct{}, len(availablePages))
	for _, p := range availablePages {
		out[p.ID] = struct{}{}
	}
	return out
}
