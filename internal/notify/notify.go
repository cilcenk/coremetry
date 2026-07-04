// Package notify dispatches Problem alerts to user-configured notification
// channels: email (SMTP), Slack/Mattermost (incoming-webhook compatible),
// generic webhook (raw JSON POST), and WhatsApp (via Twilio's Messages API).
//
// Two design decisions worth calling out:
//
//  1. SMTP credentials live in the system_settings ClickHouse table — not
//     in config.yaml — so the admin UI can rotate them without a restart.
//     Reads happen via the in-memory cache below; writes invalidate it.
//  2. Sending is fire-and-forget from the evaluator/anomaly tick. Failures
//     are logged but do not block or retry — alert spam from a flaky SMTP
//     is worse than a missed alert (which the operator notices anyway via
//     the Problems page).
package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

const settingsKey = "smtp"

// SMTPSettings is the JSON shape we persist under system_settings["smtp"].
type SMTPSettings struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	From       string `json:"from"`
	FromName   string `json:"fromName"`
	StartTLS   bool   `json:"startTLS"`
	SkipVerify bool   `json:"skipVerify"`
}

func (s SMTPSettings) Configured() bool {
	return s.Host != "" && s.Port != 0 && s.From != ""
}

// EmailChannelConfig is the per-channel JSON for type=email.
type EmailChannelConfig struct {
	Recipients []string `json:"recipients"` // one or more; comma-split also supported in UI
}

// SlackChannelConfig powers both type=slack and type=mattermost — they
// accept the same incoming-webhook JSON shape.
type SlackChannelConfig struct {
	WebhookURL string `json:"webhookUrl"`
}

// WebhookChannelConfig is the generic JSON-POST channel; the body is
// the raw chstore.Problem so the receiver can route it however it likes.
type WebhookChannelConfig struct {
	URL string `json:"url"`
}

// TeamsChannelConfig — Microsoft Teams incoming webhook. Same
// shape as Slack at the URL level (single endpoint), different
// payload format (Office-365 Connector / Adaptive Card JSON).
type TeamsChannelConfig struct {
	WebhookURL string `json:"webhookUrl"`
}

// ZoomChatChannelConfig — Zoom Chat via Server-to-Server OAuth.
// Replaces the older incoming-webhook flow because banks want
// the proper REST API (auditable, account-scoped, no webhook
// URL that leaks if dumped).
//
// Fields are exactly what Zoom's marketplace app dialog hands
// the admin:
//   - AccountID:    the Zoom account UUID (Zoom calls it Account ID)
//   - ClientID:     OAuth client_id from the Server-to-Server app
//   - ClientSecret: OAuth client_secret (write-only after save —
//                   the UI never echoes it back)
//   - ChannelID:    the channel JID for the target chat channel
//                   (Zoom's "to_channel" field on the messages API)
//   - ToContact:    optional fallback — email of a single contact
//                   to DM when ChannelID is empty
//
// On send the notifier exchanges credentials for an access
// token via /oauth/token and POSTs the chat message to
// /v2/chat/users/me/messages. Tokens cache for ~1h on a
// per-(account_id, client_id) key so a burst of N alerts
// doesn't N-times the OAuth round-trip.
type ZoomChatChannelConfig struct {
	AccountID    string `json:"accountId"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	ChannelID    string `json:"channelId,omitempty"`
	ToContact    string `json:"toContact,omitempty"`
	// APIBaseURL overrides the default `https://api.zoom.us` for
	// the chat messages endpoint. Optional — empty keeps the
	// public Zoom API. Banks routing outbound traffic through a
	// corporate proxy (api.zoom.us isn't directly reachable from
	// the perimeter) point this at their proxy host. Test
	// environments do the same for mock servers.
	APIBaseURL string `json:"apiBaseUrl,omitempty"`
	// OAuthBaseURL overrides the default `https://zoom.us` for
	// the OAuth token endpoint. Same use case as APIBaseURL but
	// the OAuth host differs from the API host in Zoom's
	// deployment, so it's a separate knob. Most proxies expose
	// both — operators typically fill both fields with the same
	// prefix or leave both empty.
	OAuthBaseURL string `json:"oauthBaseUrl,omitempty"`
	// InsecureSkipVerify disables TLS certificate validation on
	// the OAuth + chat HTTP calls. Use only when the operator
	// has routed Zoom traffic through a corporate proxy that
	// terminates TLS with a private CA the pod doesn't have in
	// its trust store. Setting this is the equivalent of
	// `curl -k` — turns off MITM detection, so reserve it for
	// trusted corp networks. Public Zoom hosts MUST verify.
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`
	// Legacy webhook fields — kept for graceful migration from
	// pre-v0.4.78 configs. When AccountID is empty AND
	// WebhookURL is set, sendZoomChat returns a clean
	// "reconfigure required" error rather than silently
	// failing. New channels never write these.
	WebhookURL        string `json:"webhookUrl,omitempty"`
	VerificationToken string `json:"verificationToken,omitempty"`
}

// WhatsAppChannelConfig wraps Twilio's WhatsApp messaging API.
//
// AccountSid + AuthToken are the standard Twilio API credentials.
// From is the sender number including the "whatsapp:" prefix and E.164
// formatting (e.g. "whatsapp:+14155238886" — the Twilio sandbox number).
// To is one or more recipient numbers, same format.
//
// Twilio is the de-facto standard for programmatic WhatsApp because it
// owns the relationship with Meta on the user's behalf. Meta's direct
// Cloud API works too but requires per-template approval, not viable
// for ad-hoc alert text.
type WhatsAppChannelConfig struct {
	AccountSid string   `json:"accountSid"`
	AuthToken  string   `json:"authToken"`
	From       string   `json:"from"`
	To         []string `json:"to"`
}

// EventPublisher is the minimum interface we need from the SSE
// broker. Defined here as an interface (rather than depending
// on internal/sse directly) so the notify package stays
// import-cycle-free — the broker can use chstore types if it
// ever wants to without circling back.
type EventPublisher interface {
	Publish(kind string, payload any)
}

// Notifier is the small surface the evaluator + anomaly worker call into.
// Construction is cheap; share one across the process.
type Notifier struct {
	store *chstore.Store

	mu       sync.RWMutex
	smtp     SMTPSettings
	smtpRead time.Time // last refresh — short TTL avoids hammering CH on every alert

	// bus is the optional SSE broker. When set, every problem-
	// open / problem-resolve / anomaly fire publishes a typed
	// event so connected browser tabs update in <1s instead of
	// waiting for the next poll. nil = behave as before
	// (poll-only).
	bus EventPublisher

	// zoomTokens caches Server-to-Server OAuth access tokens
	// keyed by (account_id, client_id). Lazy-allocated in New;
	// see sendZoomChat for the cache+invalidate flow.
	zoomTokens zoomTokenCache

	// smtpCacheTTL is how long the cached SMTP block is reused
	// before re-reading from system_settings. Config-driven so
	// operators on big CH clusters can back off the lookup
	// without recompiling. Zero falls back to 30s in SMTP().
	smtpCacheTTL time.Duration

	// publicURL is the operator-facing base URL of this
	// Coremetry deployment (e.g. https://coremetry.bank.local).
	// When set, every problem / anomaly / incident
	// notification body carries a "View in Coremetry:
	// <url>" link so the recipient (oncall on their phone,
	// team channel) can click straight to the relevant
	// detail page. Empty disables the link — back-compat
	// for deployments that haven't wired it yet.
	publicURL string

	// mwMu guards the maintenance-windows cache. Refreshed
	// lazily on each SendProblemAlert when older than 30s —
	// the window-list size stays small (<100 even on busy
	// stacks) and the table is read-heavy, so per-tick caching
	// avoids hammering CH from a flap-prone evaluator while
	// keeping suppression accurate to within 30s of an admin
	// adding / extending a window.
	mwMu   sync.Mutex
	mwAt   time.Time
	mwList []chstore.MaintenanceWindow
}

// maintenanceSilenced reports whether any active maintenance
// window matches (service, severity, now). Refreshes the
// cached list when older than 30s so a freshly-created
// window takes effect quickly without per-firing CH load.
func (n *Notifier) maintenanceSilenced(ctx context.Context, service, severity string) bool {
	n.mwMu.Lock()
	defer n.mwMu.Unlock()
	if time.Since(n.mwAt) > 30*time.Second {
		fresh, err := n.store.ListMaintenanceWindows(ctx, false)
		if err != nil {
			log.Printf("[notify] maintenance windows fetch: %v", err)
		} else {
			n.mwList = fresh
		}
		n.mwAt = time.Now()
	}
	return chstore.IsMaintenanceActive(n.mwList, service, severity, time.Now())
}

// SetPublicURL configures the deep-link base for notification
// bodies. Trims trailing slashes so URL composition is just
// base + "/route" with no double slash.
func (n *Notifier) SetPublicURL(u string) {
	n.mu.Lock()
	n.publicURL = strings.TrimRight(strings.TrimSpace(u), "/")
	n.mu.Unlock()
}

// PublicURL reads the configured base URL — safe to call from
// any goroutine; helpers below use it to build per-problem
// deep links.
func (n *Notifier) PublicURL() string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.publicURL
}

// problemURL builds the deep link to a problem detail. Returns
// "" when no public URL is configured — callers append a
// "View in Coremetry: <url>" line conditionally to avoid a
// dangling empty link in the notification body.
func (n *Notifier) problemURL(problemID string) string {
	base := n.PublicURL()
	if base == "" {
		return ""
	}
	// /anomalies is the page that surfaces Problems; the
	// ?problem= query param scrolls + highlights the matching
	// row.
	return base + "/anomalies?problem=" + problemID
}

// SetSMTPCacheTTL lets main.go wire the configurable refresh
// interval after construction. Zero is treated as "use the
// 30s default" so unit tests that build a Notifier without
// touching config still behave correctly.
func (n *Notifier) SetSMTPCacheTTL(d time.Duration) {
	n.mu.Lock()
	n.smtpCacheTTL = d
	n.mu.Unlock()
}

func New(store *chstore.Store) *Notifier {
	return &Notifier{
		store: store,
		zoomTokens: zoomTokenCache{
			entries: map[string]zoomTokenEntry{},
		},
	}
}

// SetEventBus wires the SSE broker. Called once at startup; the
// notifier stores the reference and publishes Problem.* events
// from SendProblemAlert.
func (n *Notifier) SetEventBus(bus EventPublisher) {
	n.mu.Lock()
	n.bus = bus
	n.mu.Unlock()
}

// Publish surfaces the event bus to other workers (evaluator,
// anomaly detector) that already have a Notifier reference but
// shouldn't import the broker directly. Safe pass-through; nil
// bus = no-op.
func (n *Notifier) Publish(kind string, payload any) {
	n.mu.RLock()
	bus := n.bus
	n.mu.RUnlock()
	if bus != nil {
		bus.Publish(kind, payload)
	}
}

// SMTP returns the cached settings (read-through). Cache TTL is
// config-driven (cfg.Background.SMTPCacheTTL) so operators can
// dial it up on big CH clusters; defaults to 30s when unset.
// Safe for concurrent callers.
func (n *Notifier) SMTP(ctx context.Context) SMTPSettings {
	n.mu.RLock()
	ttl := n.smtpCacheTTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	if time.Since(n.smtpRead) < ttl {
		s := n.smtp
		n.mu.RUnlock()
		return s
	}
	n.mu.RUnlock()
	return n.refreshSMTP(ctx)
}

func (n *Notifier) refreshSMTP(ctx context.Context) SMTPSettings {
	n.mu.Lock()
	defer n.mu.Unlock()
	raw, err := n.store.GetSetting(ctx, settingsKey)
	if err != nil {
		log.Printf("[notify] read smtp settings: %v", err)
		return n.smtp // keep last good copy
	}
	if len(raw) == 0 {
		n.smtp = SMTPSettings{}
		n.smtpRead = time.Now()
		return n.smtp
	}
	var s SMTPSettings
	if err := json.Unmarshal(raw, &s); err != nil {
		log.Printf("[notify] decode smtp settings: %v", err)
		return n.smtp
	}
	n.smtp = s
	n.smtpRead = time.Now()
	return n.smtp
}

// SaveSMTP persists new settings and busts the in-memory cache.
func (n *Notifier) SaveSMTP(ctx context.Context, s SMTPSettings) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := n.store.PutSetting(ctx, settingsKey, raw); err != nil {
		return err
	}
	n.mu.Lock()
	n.smtp = s
	n.smtpRead = time.Now()
	n.mu.Unlock()
	return nil
}

// SendProblemAlert fans out a problem to every channel that wants this
// severity. Errors are logged per-channel; partial failures don't abort
// the rest.
//
// Also fires an SSE event so the browser-side React Query
// caches invalidate immediately rather than waiting for the
// next poll. Kind is "problem.open" / "problem.resolve" so the
// client can decide what to invalidate (open events bump the
// sidebar badge; resolve events also do, plus drop a row from
// the open list).
func (n *Notifier) SendProblemAlert(ctx context.Context, p chstore.Problem) {
	// Enrich with the runbook URL — pulled from the firing
	// alert rule (preferred) or the service catalog metadata
	// (fallback). Done here, not at problem creation, so an
	// operator who edits a runbook URL after a rule fires
	// still sees the new link in the very next notification
	// (e.g. the resolved-event message).
	if p.RunbookURL == "" {
		enriched := n.store.EnrichProblemsWithRunbooks(ctx, []chstore.Problem{p})
		if len(enriched) > 0 {
			p = enriched[0]
		}
	}
	switch p.Status {
	case "open":
		n.Publish("problem.open", p)
	case "resolved":
		n.Publish("problem.resolve", p)
	case "acknowledged":
		n.Publish("problem.acknowledge", p)
	}
	// Maintenance windows — skip the live channel fan-out when
	// an active window matches (service, severity). Done after
	// the SSE Publish so the in-UI live feed still updates;
	// only the external channels (Slack / email / Zoom / etc.)
	// are silenced. Cached 30s so repeated firings during a
	// 1h window don't hammer CH.
	if n.maintenanceSilenced(ctx, p.Service, p.Severity) {
		log.Printf("[notify] maintenance window active — skipping channel fan-out: %s · %s", p.Service, p.RuleName)
		return
	}
	// Acknowledged — operator already saw this problem and
	// muted it. The evaluator's per-tick refresh path passes
	// the same Problem back through SendProblemAlert; without
	// this gate the channel fan-out would re-fire every tick.
	// Resolution events still go through (status=resolved
	// short-circuits this branch on its own).
	if p.Status == "acknowledged" {
		return
	}
	channels, err := n.store.EnabledChannelsForSeverity(ctx, p.Severity)
	if err != nil {
		log.Printf("[notify] load channels: %v", err)
		return
	}
	if len(channels) == 0 {
		return
	}
	// Per-channel routing predicates — channels can pin to a
	// specific service / SRE team / owner team. Empty rules
	// mean "catch-all" (fires for every problem). We look up
	// the service catalog row once and re-use across the
	// channel loop so the per-channel match check is O(1).
	var md *chstore.ServiceMetadata
	if md2, err := n.store.GetServiceMetadata(ctx, p.Service); err == nil {
		md = md2
	}
	// Enrich the in-memory problem with its cluster set so the
	// new ChannelMatchRules.Clusters predicate (v0.5.63) has
	// something to match against. The evaluator-fired path
	// passes us a freshly-constructed Problem with empty
	// Clusters; one bulk lookup is cheap and shared across the
	// channel loop. Soft-fail: on CH error we just leave
	// p.Clusters empty and cluster-scoped channels silently
	// don't match (same as a problem on a service that
	// genuinely has no cluster attr).
	if len(p.Clusters) == 0 {
		enriched := n.store.EnrichProblemsWithClusters(ctx,
			[]chstore.Problem{p}, time.Hour)
		if len(enriched) > 0 {
			p = enriched[0]
		}
	}
	in := chstore.MatchInput{
		Service:  p.Service,
		Metadata: md,
		Clusters: p.Clusters,
	}
	for _, c := range channels {
		if !c.MatchRules.MatchesProblem(in) {
			continue
		}
		if err := n.sendOne(ctx, c, p, "problem", p.ID); err != nil {
			log.Printf("[notify] %s (%s): %v", c.Name, c.Type, err)
		}
	}
}

// SendRunbookComplete fans a finished runbook execution out to the configured
// notification channels and publishes a runbook.complete SSE event. It reuses
// the Problem-shaped channel path via a synthetic Problem (the channels format
// Severity/Service/RuleName/Description). Called only when the runbook opted in
// (NotifyOnComplete). completed → info; failed → critical. (v0.7.7)
func (n *Notifier) SendRunbookComplete(ctx context.Context, e chstore.RunbookExecution, channelTypes []string) {
	severity := "info"
	if e.Status == chstore.RunExecFailed {
		severity = "critical"
	}
	done := 0
	for _, s := range e.StepStates {
		switch s.Status {
		case chstore.StepCompleted, chstore.StepSkipped, chstore.StepFailed:
			done++
		}
	}
	n.Publish("runbook.complete", e)
	p := chstore.Problem{
		Severity:    severity,
		Service:     e.TitleSnapshot,
		RuleName:    fmt.Sprintf("Runbook %s: %s", e.Status, e.TitleSnapshot),
		Description: fmt.Sprintf("Execution %s %s — %d/%d steps, started by %s.", e.ID, e.Status, done, len(e.StepStates), e.StartedBy),
		Status:      "open",
	}
	channels, err := n.store.EnabledChannelsForSeverity(ctx, severity)
	if err != nil {
		log.Printf("[notify] runbook-complete load channels: %v", err)
		return
	}
	want := runbookNotifyTypes(channelTypes)
	for _, c := range channels {
		if !want[strings.ToLower(c.Type)] {
			continue // runbook opted out of this channel type
		}
		if err := n.sendOne(ctx, c, p, "runbook", e.ID); err != nil {
			log.Printf("[notify] runbook-complete %s (%s): %v", c.Name, c.Type, err)
		}
	}
}

// runbookNotifyTypes is the set of channel TYPES a runbook-completion
// notification fires to. An empty selection defaults to email — both the
// sensible default and back-compat for runbooks created before the
// per-runbook channel selector (v0.7.22). Pure — unit-tested in notify_test.go.
func runbookNotifyTypes(types []string) map[string]bool {
	m := make(map[string]bool, len(types))
	for _, t := range types {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			m[t] = true
		}
	}
	if len(m) == 0 {
		m = map[string]bool{"email": true}
	}
	return m
}

// SendTest dispatches a synthetic Problem to a single channel — used by
// the "Send test" button on the settings UI so admins can verify config
// without waiting for a real incident.
func (n *Notifier) SendTest(ctx context.Context, c chstore.NotificationChannel) error {
	test := chstore.Problem{
		ID:          "test",
		RuleID:      "test",
		RuleName:    "Test alert from Coremetry",
		Severity:    "warning",
		Service:     "coremetry",
		Metric:      "test",
		Value:       42,
		Threshold:   10,
		Status:      "open",
		Description: "This is a test notification — your channel is configured correctly.",
		StartedAt:   time.Now().UnixNano(),
	}
	return n.sendOne(ctx, c, test, "test", "")
}

// sendOne is the single funnel every channel dispatch routes through
// (problem alerts, runbook-complete notices, "send test"). It records
// the outcome — success AND failure — to the append-only
// notification_log (v0.8.241) before returning, so the /events history
// captures every send from ONE place instead of per-channel sprinkling.
// relKind/relID describe the originating entity (problem|runbook|test).
func (n *Notifier) sendOne(ctx context.Context, c chstore.NotificationChannel, p chstore.Problem, relKind, relID string) error {
	err := n.dispatch(ctx, c, p)
	n.recordNotification(ctx, c, p, relKind, relID, err)
	return err
}

// dispatch performs the actual per-channel send. Kept separate from
// sendOne so the notification_log record wraps every branch (including
// the unknown-type error) exactly once.
func (n *Notifier) dispatch(ctx context.Context, c chstore.NotificationChannel, p chstore.Problem) error {
	switch c.Type {
	case "email":
		return n.sendEmail(ctx, c, p)
	case "slack", "mattermost":
		// Mattermost ships an incoming-webhook endpoint that consumes
		// the same JSON Slack does, so one impl covers both. We keep
		// them as distinct channel types so the UI can label them
		// correctly and operators see at a glance which is which.
		return n.sendSlack(ctx, c, p)
	case "teams":
		return n.sendTeams(ctx, c, p)
	case "zoomchat":
		return n.sendZoomChat(ctx, c, p)
	case "webhook":
		return n.sendWebhook(ctx, c, p)
	case "whatsapp":
		return n.sendWhatsApp(ctx, c, p)
	}
	return fmt.Errorf("unknown channel type: %s", c.Type)
}

// ── Email backend ───────────────────────────────────────────────────────────

func (n *Notifier) sendEmail(ctx context.Context, c chstore.NotificationChannel, p chstore.Problem) error {
	smtpCfg := n.SMTP(ctx)
	if !smtpCfg.Configured() {
		return errors.New("SMTP is not configured — set host/port/from in Settings")
	}
	var ec EmailChannelConfig
	if err := json.Unmarshal(c.Config, &ec); err != nil {
		return fmt.Errorf("decode email config: %w", err)
	}
	if len(ec.Recipients) == 0 {
		return errors.New("channel has no recipients")
	}

	subject := fmt.Sprintf("[%s] %s — %s", strings.ToUpper(p.Severity), p.Service, p.RuleName)
	body := n.buildEmailBody(p)

	from := smtpCfg.From
	fromHeader := from
	if smtpCfg.FromName != "" {
		fromHeader = fmt.Sprintf("%s <%s>", smtpCfg.FromName, from)
	}
	msg := strings.Builder{}
	msg.WriteString("From: " + fromHeader + "\r\n")
	msg.WriteString("To: " + strings.Join(ec.Recipients, ", ") + "\r\n")
	msg.WriteString("Subject: " + subject + "\r\n")
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(body)

	addr := net.JoinHostPort(smtpCfg.Host, strconv.Itoa(smtpCfg.Port))
	return sendSMTP(addr, smtpCfg, from, ec.Recipients, []byte(msg.String()))
}

func (n *Notifier) buildEmailBody(p chstore.Problem) string {
	t := time.Unix(0, p.StartedAt).UTC().Format(time.RFC3339)
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", p.Description)
	fmt.Fprintf(&b, "Service:    %s\n", p.Service)
	fmt.Fprintf(&b, "Rule:       %s\n", p.RuleName)
	fmt.Fprintf(&b, "Severity:   %s\n", strings.ToUpper(p.Severity))
	fmt.Fprintf(&b, "Metric:     %s\n", p.Metric)
	fmt.Fprintf(&b, "Value:      %.2f (threshold %.2f)\n", p.Value, p.Threshold)
	fmt.Fprintf(&b, "Started at: %s\n", t)
	if p.RunbookURL != "" {
		fmt.Fprintf(&b, "Runbook:    %s\n", p.RunbookURL)
	}
	if u := n.problemURL(p.ID); u != "" {
		fmt.Fprintf(&b, "Open:       %s\n", u)
	}
	return b.String()
}

// SendMail is a generic plain-text mailer over the operator's
// configured SMTP — used by surfaces that aren't problem alerts
// (status-page subscriber double-opt-in, future password-reset
// flows, etc.). Returns an error when SMTP isn't configured so
// the caller can decide whether the operation should fail or
// continue silently (status-page subscribe falls back to "we
// recorded your email; the operator will deliver the link
// manually" when SMTP isn't wired up).
func (n *Notifier) SendMail(ctx context.Context, to []string, subject, body string) error {
	cfg := n.SMTP(ctx)
	if !cfg.Configured() {
		return errors.New("SMTP is not configured — set host/port/from in Settings")
	}
	if len(to) == 0 {
		return errors.New("SendMail: no recipients")
	}
	from := cfg.From
	fromHeader := from
	if cfg.FromName != "" {
		fromHeader = fmt.Sprintf("%s <%s>", cfg.FromName, from)
	}
	msg := strings.Builder{}
	msg.WriteString("From: " + fromHeader + "\r\n")
	msg.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	msg.WriteString("Subject: " + subject + "\r\n")
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(body)
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	return sendSMTP(addr, cfg, from, to, []byte(msg.String()))
}

// sendSMTP is split out so it can be swapped in tests + handles the
// STARTTLS dance manually because net/smtp's SendMail can't do explicit
// TLS-or-not toggling cleanly with arbitrary verify settings.
func sendSMTP(addr string, cfg SMTPSettings, from string, to []string, msg []byte) error {
	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	defer c.Close()

	if cfg.StartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("server does not advertise STARTTLS")
		}
		tlsCfg := &tls.Config{ServerName: cfg.Host, InsecureSkipVerify: cfg.SkipVerify}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if cfg.Username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return errors.New("server does not advertise AUTH but username is set")
		}
		auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	for _, r := range to {
		if err := c.Rcpt(r); err != nil {
			return fmt.Errorf("rcpt to %s: %w", r, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close body: %w", err)
	}
	return c.Quit()
}

// ── Slack / Mattermost backend ──────────────────────────────────────────────
//
// Both consume the same incoming-webhook JSON. Use a coloured attachment
// keyed off the problem severity so the message renders with a clear
// status stripe in the channel — same convention Grafana / Prometheus /
// PagerDuty alerts use.
func (n *Notifier) sendSlack(ctx context.Context, c chstore.NotificationChannel, p chstore.Problem) error {
	var sc SlackChannelConfig
	if err := json.Unmarshal(c.Config, &sc); err != nil {
		return fmt.Errorf("decode slack config: %w", err)
	}
	if sc.WebhookURL == "" {
		return errors.New("channel has no webhook URL")
	}
	color := severityColor(p.Severity)
	t := time.Unix(0, p.StartedAt).UTC().Format(time.RFC3339)
	fields := []map[string]any{
		{"title": "Service",   "value": p.Service,                                       "short": true},
		{"title": "Severity",  "value": strings.ToUpper(p.Severity),                    "short": true},
		{"title": "Metric",    "value": p.Metric,                                        "short": true},
		{"title": "Value",     "value": fmt.Sprintf("%.2f (threshold %.2f)", p.Value, p.Threshold), "short": true},
		{"title": "Started",   "value": t,                                               "short": false},
	}
	// Runbook link as a clickable Slack mrkdwn field — the
	// oncall on mobile lands on the playbook in one tap.
	if p.RunbookURL != "" {
		fields = append(fields, map[string]any{
			"title": "Runbook",
			"value": fmt.Sprintf("<%s|Open runbook ↗>", p.RunbookURL),
			"short": false,
		})
	}
	if u := n.problemURL(p.ID); u != "" {
		fields = append(fields, map[string]any{
			"title": "Coremetry",
			"value": fmt.Sprintf("<%s|Open in Coremetry ↗>", u),
			"short": false,
		})
	}
	body := map[string]any{
		"text": fmt.Sprintf("[%s] %s — %s", strings.ToUpper(p.Severity), p.Service, p.RuleName),
		"attachments": []map[string]any{{
			"color":  color,
			"text":   p.Description,
			"fields": fields,
			"footer": "Coremetry",
		}},
	}
	return postJSON(ctx, sc.WebhookURL, body)
}

// ── Microsoft Teams backend (Office-365 Connector card) ────────────────────
//
// Teams' incoming webhook expects a `MessageCard` JSON envelope —
// distinct from the Slack/Mattermost format. We render the same
// fields (severity, service, metric, value, started, optional
// runbook button) as a card with a coloured side stripe.
func (n *Notifier) sendTeams(ctx context.Context, c chstore.NotificationChannel, p chstore.Problem) error {
	var tc TeamsChannelConfig
	if err := json.Unmarshal(c.Config, &tc); err != nil {
		return fmt.Errorf("decode teams config: %w", err)
	}
	if tc.WebhookURL == "" {
		return errors.New("channel has no webhook URL")
	}
	colour := strings.TrimPrefix(severityColor(p.Severity), "#")
	t := time.Unix(0, p.StartedAt).UTC().Format(time.RFC3339)
	facts := []map[string]string{
		{"name": "Service",  "value": p.Service},
		{"name": "Severity", "value": strings.ToUpper(p.Severity)},
		{"name": "Metric",   "value": p.Metric},
		{"name": "Value",    "value": fmt.Sprintf("%.2f (threshold %.2f)", p.Value, p.Threshold)},
		{"name": "Started",  "value": t},
	}
	body := map[string]any{
		"@type":      "MessageCard",
		"@context":   "https://schema.org/extensions",
		"summary":    fmt.Sprintf("[%s] %s — %s", strings.ToUpper(p.Severity), p.Service, p.RuleName),
		"themeColor": colour,
		"title":      fmt.Sprintf("[%s] %s — %s", strings.ToUpper(p.Severity), p.Service, p.RuleName),
		"text":       p.Description,
		"sections":   []map[string]any{{"facts": facts}},
	}
	// MessageCard supports up to multiple potentialAction
	// buttons. Surface Runbook + Coremetry-link side by side
	// so the oncall has both one tap away.
	var actions []map[string]any
	if p.RunbookURL != "" {
		actions = append(actions, map[string]any{
			"@type": "OpenUri",
			"name":  "Open runbook",
			"targets": []map[string]string{
				{"os": "default", "uri": p.RunbookURL},
			},
		})
	}
	if u := n.problemURL(p.ID); u != "" {
		actions = append(actions, map[string]any{
			"@type": "OpenUri",
			"name":  "Open in Coremetry",
			"targets": []map[string]string{
				{"os": "default", "uri": u},
			},
		})
	}
	if len(actions) > 0 {
		body["potentialAction"] = actions
	}
	return postJSON(ctx, tc.WebhookURL, body)
}

// ── Zoom Chat backend (Server-to-Server OAuth) ─────────────────────────────
//
// Two-step flow:
//
//   1. POST https://zoom.us/oauth/token?grant_type=account_credentials
//      &account_id=<ACCOUNT_ID>  with Basic(client_id:client_secret)
//      → { access_token, expires_in, … }
//
//   2. POST https://api.zoom.us/v2/chat/users/me/messages
//      Authorization: Bearer <token>
//      Body: { message, to_channel } or { message, to_contact }
//
// Token cache: per-(account_id, client_id) entry that's reused
// for the server-stated expires_in minus 30s. A burst of N
// alerts hits the OAuth endpoint exactly once. The cache also
// invalidates on a 401 from the messages API and retries once
// — covers the case where Zoom revoked the token early.

// zoomTokenCache is a tiny in-process LRU keyed by
// (account_id, client_id). One Notifier process = one cache.
type zoomTokenCache struct {
	mu      sync.Mutex
	entries map[string]zoomTokenEntry
}
type zoomTokenEntry struct {
	accessToken string
	expiresAt   time.Time
}

// zoomHTTPClient bounds every call to Zoom's OAuth + chat APIs.
// Without this the default client has no timeout — a Zoom
// regional outage would hang every alert-sending goroutine
// indefinitely and back-pressure the evaluator's send queue.
// 15s is generous for OAuth + a single chat POST yet still
// short enough that the evaluator's next tick (1 min) doesn't
// stack on a stalled batch.
var zoomHTTPClient = &http.Client{Timeout: 15 * time.Second}

// zoomHTTPClientInsecure is the InsecureSkipVerify=true variant
// reserved for channels with that flag set. Lazily initialised
// (sync.Once) so the certPool / TLS handshake setup happens
// once per process rather than per request. Kept as a package-
// level singleton instead of per-channel because every call
// hits the same upstream host pattern (api.zoom.us /
// proxy.bank.local) and the transport's connection pool
// benefits from sharing.
var (
	zoomInsecureOnce   sync.Once
	zoomInsecureClient *http.Client
)

func zoomClientFor(skipVerify bool) *http.Client {
	if !skipVerify {
		return zoomHTTPClient
	}
	zoomInsecureOnce.Do(func() {
		zoomInsecureClient = &http.Client{
			Timeout: 15 * time.Second,
			// nolint:gosec // operator-opt-in for corp-proxy MITM CAs.
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}
	})
	return zoomInsecureClient
}

func (n *Notifier) sendZoomChat(ctx context.Context, c chstore.NotificationChannel, p chstore.Problem) error {
	var zc ZoomChatChannelConfig
	if err := json.Unmarshal(c.Config, &zc); err != nil {
		return fmt.Errorf("decode zoom chat config: %w", err)
	}
	if zc.AccountID == "" {
		// Legacy webhook config → ask for migration. Returning a
		// clean error is better than a confused 401 from the
		// non-existent OAuth round-trip.
		if zc.WebhookURL != "" {
			return errors.New("Zoom Chat channel uses the legacy webhook format — please reconfigure with Account ID / Client ID / Client Secret / Channel ID (Settings → Notification channels)")
		}
		return errors.New("Zoom Chat channel missing Account ID")
	}
	if zc.ClientID == "" || zc.ClientSecret == "" {
		return errors.New("Zoom Chat channel missing Client ID or Client Secret")
	}
	if zc.ChannelID == "" && zc.ToContact == "" {
		return errors.New("Zoom Chat channel needs either a Channel ID or a contact email")
	}

	token, err := n.zoomAccessToken(ctx, zc.AccountID, zc.ClientID, zc.ClientSecret, zc.OAuthBaseURL, zc.InsecureSkipVerify)
	if err != nil {
		return fmt.Errorf("zoom oauth: %w", err)
	}

	t := time.Unix(0, p.StartedAt).UTC().Format(time.RFC3339)
	header := fmt.Sprintf("[%s] %s — %s",
		strings.ToUpper(p.Severity), p.Service, p.RuleName)
	msg := fmt.Sprintf(
		"%s\n%s\n\n• Service: %s\n• Severity: %s\n• Metric: %s\n• Value: %.2f (threshold %.2f)\n• Started: %s",
		header, p.Description, p.Service, strings.ToUpper(p.Severity),
		p.Metric, p.Value, p.Threshold, t)
	if p.RunbookURL != "" {
		msg += "\n• Runbook: " + p.RunbookURL
	}
	if u := n.problemURL(p.ID); u != "" {
		msg += "\n• View in Coremetry: " + u
	}

	payload := map[string]any{"message": msg}
	if zc.ChannelID != "" {
		payload["to_channel"] = zc.ChannelID
	} else {
		payload["to_contact"] = zc.ToContact
	}

	// First attempt with the (cached) token.
	if err := postZoomChatMessage(ctx, token, payload, zc.APIBaseURL, zc.InsecureSkipVerify); err != nil {
		// On auth failure, drop the cached token and retry once
		// with a freshly-minted one. Zoom can revoke tokens at
		// any moment (admin rotated the secret, app pause, etc.)
		// and we don't want a stale-cache window to swallow
		// alerts silently.
		if isAuthErr(err) {
			n.zoomInvalidate(zc.AccountID, zc.ClientID)
			token2, err2 := n.zoomAccessToken(ctx, zc.AccountID, zc.ClientID, zc.ClientSecret, zc.OAuthBaseURL, zc.InsecureSkipVerify)
			if err2 != nil {
				return fmt.Errorf("zoom oauth retry: %w", err2)
			}
			return postZoomChatMessage(ctx, token2, payload, zc.APIBaseURL, zc.InsecureSkipVerify)
		}
		return err
	}
	return nil
}

// zoomAccessToken returns a cached access token or fetches a new
// one. Cache key is (account_id, client_id) — separate apps under
// the same account hold separate tokens. Refresh threshold is 30s
// before the server-stated expires_in so an in-flight request
// doesn't race the expiry.
func (n *Notifier) zoomAccessToken(ctx context.Context, accountID, clientID, clientSecret, oauthBaseURL string, skipVerify bool) (string, error) {
	cacheKey := accountID + "|" + clientID
	n.zoomTokens.mu.Lock()
	if e, ok := n.zoomTokens.entries[cacheKey]; ok && time.Until(e.expiresAt) > 30*time.Second {
		tok := e.accessToken
		n.zoomTokens.mu.Unlock()
		return tok, nil
	}
	n.zoomTokens.mu.Unlock()

	form := url.Values{}
	form.Set("grant_type", "account_credentials")
	form.Set("account_id", accountID)
	// OAuth host override — `https://zoom.us` is the public
	// default; banks fronting Zoom with a corporate proxy point
	// this at their gateway. Trailing slash trimmed so we don't
	// emit a double slash before /oauth/token.
	base := strings.TrimRight(oauthBaseURL, "/")
	if base == "" {
		base = "https://zoom.us"
	}
	req, err := http.NewRequestWithContext(ctx, "POST",
		base+"/oauth/token?"+form.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(clientID, clientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := zoomClientFor(skipVerify).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("oauth %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("decode token: %w", err)
	}
	if parsed.AccessToken == "" {
		return "", errors.New("oauth response missing access_token")
	}
	exp := time.Now().Add(time.Duration(parsed.ExpiresIn) * time.Second)
	if parsed.ExpiresIn == 0 {
		// Defensive — Zoom always sets this, but if the shape
		// changes we don't want to cache forever.
		exp = time.Now().Add(45 * time.Minute)
	}
	n.zoomTokens.mu.Lock()
	n.zoomTokens.entries[cacheKey] = zoomTokenEntry{
		accessToken: parsed.AccessToken,
		expiresAt:   exp,
	}
	n.zoomTokens.mu.Unlock()
	return parsed.AccessToken, nil
}

func (n *Notifier) zoomInvalidate(accountID, clientID string) {
	n.zoomTokens.mu.Lock()
	delete(n.zoomTokens.entries, accountID+"|"+clientID)
	n.zoomTokens.mu.Unlock()
}

func postZoomChatMessage(ctx context.Context, token string, payload map[string]any, apiBaseURL string, skipVerify bool) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode chat payload: %w", err)
	}
	// API host override — `https://api.zoom.us` is the public
	// default. Proxy / sandbox deployments point this at their
	// gateway. Trailing slash trimmed so we don't emit a double
	// slash before /v2/chat/...
	base := strings.TrimRight(apiBaseURL, "/")
	if base == "" {
		base = "https://api.zoom.us"
	}
	req, err := http.NewRequestWithContext(ctx, "POST",
		base+"/v2/chat/users/me/messages", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := zoomClientFor(skipVerify).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return zoomAPIError{status: resp.StatusCode, body: strings.TrimSpace(string(respBody))}
	}
	return nil
}

type zoomAPIError struct {
	status int
	body   string
}

func (e zoomAPIError) Error() string {
	return fmt.Sprintf("zoom chat api %d: %s", e.status, e.body)
}

func isAuthErr(err error) bool {
	var ze zoomAPIError
	if errors.As(err, &ze) {
		return ze.status == 401 || ze.status == 403
	}
	return false
}

// ZoomChannel is one row of the channel-picker list the
// Settings UI uses to help operators pick a Channel ID without
// memorising JIDs. Mirrors Zoom's /chat/users/me/channels API
// shape — we only surface the fields a human needs to
// disambiguate one channel from another.
type ZoomChannel struct {
	ID   string `json:"id"`   // short id (rarely useful — included so the operator can search by it)
	JID  string `json:"jid"`  // the value that goes into `to_channel` on the messages API
	Name string `json:"name"` // human-readable channel name
	Type int    `json:"type"` // 1=DM, 2=Group, 3=Public Channel, 4=Private Channel
}

// ListZoomChannels fetches every channel the configured S2S
// OAuth app can see — walks the channels endpoint's pagination
// (page_size=50, next_page_token) until exhausted or a sane
// cap is hit. Used by the Settings UI's "List my channels"
// helper so the operator picks from a searchable list instead
// of pasting JIDs by hand.
//
// Caps:
//   - 20 pages of 50 = 1000 channels max. Large Zoom workspaces
//     can have thousands, but past 1000 the picker becomes
//     unusable anyway and we'd rather fail loud than return a
//     truncated list silently. The error message instructs the
//     operator to narrow scope (use a less-privileged service
//     account or filter on the receiving end).
//   - 25s overall wall-clock — context cancel breaks the loop
//     so an unresponsive Zoom doesn't hang the request.
func (n *Notifier) ListZoomChannels(
	ctx context.Context, accountID, clientID, clientSecret, oauthBaseURL, apiBaseURL string,
	skipVerify bool,
) ([]ZoomChannel, error) {
	token, err := n.zoomAccessToken(ctx, accountID, clientID, clientSecret, oauthBaseURL, skipVerify)
	if err != nil {
		return nil, fmt.Errorf("zoom oauth: %w", err)
	}
	base := strings.TrimRight(apiBaseURL, "/")
	if base == "" {
		base = "https://api.zoom.us"
	}
	out := []ZoomChannel{}
	nextToken := ""
	const (
		maxPages = 20
		pageSize = 50
	)
	for page := 0; page < maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		u := fmt.Sprintf("%s/v2/chat/users/me/channels?page_size=%d", base, pageSize)
		if nextToken != "" {
			u += "&next_page_token=" + nextToken
		}
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			return out, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := zoomClientFor(skipVerify).Do(req)
		if err != nil {
			return out, fmt.Errorf("zoom list channels: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			return out, fmt.Errorf("zoom list channels %d: %s",
				resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var parsed struct {
			Channels      []ZoomChannel `json:"channels"`
			NextPageToken string        `json:"next_page_token"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return out, fmt.Errorf("decode channels: %w", err)
		}
		out = append(out, parsed.Channels...)
		if parsed.NextPageToken == "" {
			return out, nil
		}
		nextToken = parsed.NextPageToken
	}
	// Hit the page cap — tell the operator so they know the
	// list is truncated.
	return out, fmt.Errorf("more than %d channels available — picker truncated; use a narrower service account or paste the JID directly", maxPages*pageSize)
}

// ── Generic webhook backend ─────────────────────────────────────────────────
//
// Posts the raw chstore.Problem JSON so the receiver can route however it
// wants (PagerDuty Events API, n8n, custom Lambda, etc.).
func (n *Notifier) sendWebhook(ctx context.Context, c chstore.NotificationChannel, p chstore.Problem) error {
	var wc WebhookChannelConfig
	if err := json.Unmarshal(c.Config, &wc); err != nil {
		return fmt.Errorf("decode webhook config: %w", err)
	}
	if wc.URL == "" {
		return errors.New("channel has no URL")
	}
	// Generic-webhook receivers (PagerDuty Events API, n8n,
	// custom Lambdas) typically build their own UI URL from
	// the payload. Surface the Coremetry deep-link as a
	// dedicated field so they don't have to reconstruct it
	// from the deployment's base URL. JSON shape is
	// backward-compatible: a receiver that doesn't know
	// about coremetryUrl just ignores the extra field.
	payload := map[string]any{
		"problem":      p,
		"coremetryUrl": n.problemURL(p.ID),
	}
	return postJSON(ctx, wc.URL, payload)
}

// ── WhatsApp backend (Twilio Messages API) ──────────────────────────────────
//
// Twilio is the de-facto standard for programmatic WhatsApp. The
// Messages API is form-encoded, basic-auth'd with the Account SID +
// Auth Token. Sender + recipient numbers must carry the "whatsapp:"
// prefix and be E.164-formatted.
//
// Multi-recipient channels send N independent requests — Twilio's API
// is one message per call.
func (n *Notifier) sendWhatsApp(ctx context.Context, c chstore.NotificationChannel, p chstore.Problem) error {
	var wc WhatsAppChannelConfig
	if err := json.Unmarshal(c.Config, &wc); err != nil {
		return fmt.Errorf("decode whatsapp config: %w", err)
	}
	if wc.AccountSid == "" || wc.AuthToken == "" {
		return errors.New("twilio credentials (accountSid + authToken) are required")
	}
	if wc.From == "" {
		return errors.New("twilio whatsapp 'from' is required (e.g. whatsapp:+14155238886)")
	}
	if len(wc.To) == 0 {
		return errors.New("channel has no recipients")
	}
	endpoint := fmt.Sprintf("https://api.twilio.com/2010-04-01/Accounts/%s/Messages.json",
		url.PathEscape(wc.AccountSid))
	bodyText := buildWhatsAppText(p)

	cli := &http.Client{Timeout: 10 * time.Second}
	for _, to := range wc.To {
		form := url.Values{}
		form.Set("From", normaliseWhatsAppAddr(wc.From))
		form.Set("To", normaliseWhatsAppAddr(to))
		form.Set("Body", bodyText)

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
			strings.NewReader(form.Encode()))
		if err != nil {
			return fmt.Errorf("build twilio request: %w", err)
		}
		req.SetBasicAuth(wc.AccountSid, wc.AuthToken)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := cli.Do(req)
		if err != nil {
			return fmt.Errorf("twilio post (%s): %w", to, err)
		}
		// Twilio returns 201 Created on success, 4xx with a JSON
		// {message: "..."} on rejection (bad number, unsubscribed,
		// throttled, etc.). Surface that to the operator instead of
		// the bare HTTP code.
		if resp.StatusCode >= 300 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			return fmt.Errorf("twilio %d for %s: %s", resp.StatusCode, to, strings.TrimSpace(string(b)))
		}
		resp.Body.Close()
	}
	return nil
}

// normaliseWhatsAppAddr lets operators paste either "+14155238886" or
// "whatsapp:+14155238886" into the form. Twilio requires the prefix.
func normaliseWhatsAppAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if strings.HasPrefix(addr, "whatsapp:") {
		return addr
	}
	return "whatsapp:" + addr
}

func buildWhatsAppText(p chstore.Problem) string {
	t := time.Unix(0, p.StartedAt).UTC().Format("15:04 MST")
	out := fmt.Sprintf(
		"*[%s]* %s — %s\n%s\nValue: %.2f (threshold %.2f)\nStarted: %s",
		strings.ToUpper(p.Severity), p.Service, p.RuleName,
		p.Description, p.Value, p.Threshold, t,
	)
	if p.RunbookURL != "" {
		out += "\nRunbook: " + p.RunbookURL
	}
	return out
}

// ── Shared HTTP-POST helper ─────────────────────────────────────────────────

func postJSON(ctx context.Context, endpoint string, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	cli := &http.Client{Timeout: 10 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func severityColor(sev string) string {
	switch strings.ToLower(sev) {
	case "critical":
		return "#ff5252"
	case "warning":
		return "#f59f00"
	default:
		return "#3b82f6"
	}
}
