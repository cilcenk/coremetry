package chstore

import "strings"

// v0.5.409 — known external (3rd-party) service catalogue.
// Datadog / Honeycomb / Dynatrace annotate outbound edges to
// known SaaS / cloud / infra endpoints so the operator sees
// "Stripe (payments)" instead of "ext:api.stripe.com" — same
// information, but the kind label reads at a glance vs needing
// to recognise the hostname.
//
// Lookup pattern: matches against the peer string after the
// `ext:` prefix (the peer_service / server.address /
// net.peer.name the aggregator stored). Substring match,
// case-insensitive, first-match-wins per category.
//
// Catalogue intentionally narrow — only widely-deployed
// vendors. Operators who want their own pattern set will get
// a system_settings extension hook in Phase 2; for now the
// hardcoded list covers the common cases observed across
// banking / fintech / e-commerce installs we've seen.

// externalKind is the SaaS category emitted via the topology
// edge response. Frontend uses it to render a small colored
// badge ("payments" red, "messaging" blue, etc.) so the
// operator's eye lands on category before reading the hostname.
type externalKind string

const (
	extPayments     externalKind = "payments"
	extMessaging    externalKind = "messaging"
	extEmail        externalKind = "email"
	extAuth         externalKind = "auth"
	extCDN          externalKind = "cdn"
	extObservability externalKind = "observability"
	extCloud        externalKind = "cloud"
	extAI           externalKind = "ai"
	extSearch       externalKind = "search"
	extPushNotif    externalKind = "push"
	extSMS          externalKind = "sms"
)

// extPattern is one row of the catalogue. Substrings tested
// against the lowercased peer name; first match wins. Display
// is what the frontend renders (e.g., "Stripe"); the category
// drives the badge colour.
type extPattern struct {
	substring string
	display   string
	kind      externalKind
}

var externalCatalogue = []extPattern{
	// Payments
	{"stripe.com", "Stripe", extPayments},
	{"checkout.com", "Checkout.com", extPayments},
	{"adyen.com", "Adyen", extPayments},
	{"braintreepayments.com", "Braintree", extPayments},
	{"paypal.com", "PayPal", extPayments},
	{"squareup.com", "Square", extPayments},

	// Messaging / chat
	{"slack.com", "Slack", extMessaging},
	{"discord.com", "Discord", extMessaging},
	{"hooks.slack.com", "Slack webhook", extMessaging},
	{"webhook.office.com", "Teams webhook", extMessaging},

	// SMS / voice
	{"twilio.com", "Twilio", extSMS},
	{"messagebird.com", "MessageBird", extSMS},
	{"vonage.com", "Vonage", extSMS},
	{"nexmo.com", "Vonage", extSMS},

	// Email
	{"sendgrid.com", "SendGrid", extEmail},
	{"sparkpostmail.com", "SparkPost", extEmail},
	{"mailgun.org", "Mailgun", extEmail},
	{"postmarkapp.com", "Postmark", extEmail},
	{"amazonses.com", "AWS SES", extEmail},

	// Push notif
	{"fcm.googleapis.com", "FCM", extPushNotif},
	{"push.apple.com", "APNs", extPushNotif},

	// Auth
	{"auth0.com", "Auth0", extAuth},
	{"okta.com", "Okta", extAuth},
	{"cognito-idp", "Cognito", extAuth},

	// AI
	{"api.openai.com", "OpenAI", extAI},
	{"api.anthropic.com", "Anthropic", extAI},
	{"generativelanguage.googleapis.com", "Gemini", extAI},

	// Search
	{"algolia.net", "Algolia", extSearch},
	{"typesense", "Typesense", extSearch},

	// Observability
	{"sentry.io", "Sentry", extObservability},
	{"datadoghq.com", "Datadog", extObservability},
	{"newrelic.com", "New Relic", extObservability},
	{"honeycomb.io", "Honeycomb", extObservability},
	{"pagerduty.com", "PagerDuty", extObservability},

	// CDN / edge
	{"cloudfront.net", "CloudFront", extCDN},
	{"cloudflare.com", "Cloudflare", extCDN},
	{"akamai.net", "Akamai", extCDN},
	{"fastly.net", "Fastly", extCDN},

	// Cloud — keep these last; they're broad substrings and
	// would shadow more-specific entries above (api.stripe.com
	// matches stripe before amazonaws). Substring-order is the
	// disambiguator since first-match-wins.
	{"amazonaws.com", "AWS", extCloud},
	{"azure.com", "Azure", extCloud},
	{"googleapis.com", "Google Cloud", extCloud},
	{"digitalocean.com", "DigitalOcean", extCloud},
}

// classifyExternal looks up `peer` (the string after `ext:`) in
// the catalogue. Returns ("", "", false) when no entry matches —
// the caller leaves the edge with its raw `ext:<peer>` label.
func classifyExternal(peer string) (display string, kind string, ok bool) {
	if peer == "" {
		return "", "", false
	}
	p := strings.ToLower(peer)
	for _, e := range externalCatalogue {
		if strings.Contains(p, e.substring) {
			return e.display, string(e.kind), true
		}
	}
	return "", "", false
}
