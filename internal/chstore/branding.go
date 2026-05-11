package chstore

import (
	"context"
	"encoding/json"
)

// BrandingSettings — admin-customisable strings + logo rendered
// across the public surface (login + the browser tab title) and
// the chrome on logged-in pages. Stored in system_settings under
// the "branding" key as a JSON blob. Empty / zero-value fields
// fall back to the Coremetry defaults so a fresh install reads
// as plain "Coremetry" without an admin filling the form.
//
// Logo is a base64 data URI (capped at ~200KB at the API layer)
// so we don't introduce a separate blob/object-store dependency
// for what's typically a 30KB PNG. The same rendering path serves
// the login page (where the SPA isn't authed yet) and the
// in-app header.
type BrandingSettings struct {
	AppName           string `json:"appName,omitempty"`
	BrowserTitle      string `json:"browserTitle,omitempty"`
	LoginTitle        string `json:"loginTitle,omitempty"`
	LoginSubtitle     string `json:"loginSubtitle,omitempty"`
	SignInButtonLabel string `json:"signInButtonLabel,omitempty"`
	UsernameLabel     string `json:"usernameLabel,omitempty"`
	FooterText        string `json:"footerText,omitempty"`
	// LogoDataURI is a "data:image/png;base64,..." string. Empty
	// → the UI renders the built-in Telescope mark.
	LogoDataURI       string `json:"logoDataUri,omitempty"`
	// PrimaryColor overrides the --accent CSS var when set.
	// Optional; empty keeps the bundled theme.
	PrimaryColor      string `json:"primaryColor,omitempty"`
	// Language: "en" (default) or "tr". Drives the i18n catalog
	// the SPA uses to render sidebar labels, page titles, common
	// buttons, login strings, and empty/error states.
	Language          string `json:"language,omitempty"`
}

const brandingKey = "branding"

// GetBranding returns the saved branding overlay (or an empty
// struct if unset — caller applies defaults). The endpoint that
// serves this is public, since the login page renders the result
// before the operator has a session.
func (s *Store) GetBranding(ctx context.Context) (BrandingSettings, error) {
	var b BrandingSettings
	raw, err := s.GetSetting(ctx, brandingKey)
	if err != nil {
		return b, err
	}
	if len(raw) == 0 {
		return b, nil
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return b, err
	}
	return b, nil
}

// PutBranding overwrites the saved overlay. Admin-gated at the
// HTTP layer; the store side is unguarded so the boot-time
// seeder (future) can also call it.
func (s *Store) PutBranding(ctx context.Context, b BrandingSettings) error {
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return s.PutSetting(ctx, brandingKey, raw)
}
