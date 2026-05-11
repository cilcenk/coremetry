import { useEffect, useState } from 'react';

// BrandingSettings mirrors chstore.BrandingSettings — admin-set
// overlay applied to the login page + the browser tab title.
// Every field is optional; the SPA falls back to Coremetry
// defaults for any field left empty.
export interface BrandingSettings {
  appName?: string;
  browserTitle?: string;
  loginTitle?: string;
  loginSubtitle?: string;
  signInButtonLabel?: string;
  usernameLabel?: string;
  footerText?: string;
  logoDataUri?: string;
  primaryColor?: string;
  // 'en' (default) | 'tr'. Drives the i18n catalog the SPA
  // uses for sidebar labels, login strings, common buttons,
  // page titles, empty/error states.
  language?: 'en' | 'tr';
}

export const DEFAULT_BRANDING: Required<BrandingSettings> = {
  appName:           'Coremetry',
  browserTitle:      'Coremetry',
  loginTitle:        'Sign in to Coremetry',
  loginSubtitle:     '',
  signInButtonLabel: 'Sign in',
  usernameLabel:     'Email',
  footerText:        '',
  logoDataUri:       '',
  primaryColor:      '',
  language:          'en',
};

// Resolve fills empty fields with defaults so consumers never
// have to ?? — also normalises browserTitle to mirror appName
// when the operator only filled one.
export function resolveBranding(b: BrandingSettings | null | undefined): Required<BrandingSettings> {
  const r = { ...DEFAULT_BRANDING };
  if (!b) return r;
  if (b.appName)           r.appName = b.appName;
  if (b.browserTitle)      r.browserTitle = b.browserTitle;
  else if (b.appName)      r.browserTitle = b.appName;
  if (b.loginTitle)        r.loginTitle = b.loginTitle;
  else if (b.appName)      r.loginTitle = `Sign in to ${b.appName}`;
  if (b.loginSubtitle)     r.loginSubtitle = b.loginSubtitle;
  if (b.signInButtonLabel) r.signInButtonLabel = b.signInButtonLabel;
  if (b.usernameLabel)     r.usernameLabel = b.usernameLabel;
  if (b.footerText)        r.footerText = b.footerText;
  if (b.logoDataUri)       r.logoDataUri = b.logoDataUri;
  if (b.primaryColor)      r.primaryColor = b.primaryColor;
  if (b.language === 'tr' || b.language === 'en') r.language = b.language;
  return r;
}

// useBranding fetches once at mount + applies side-effects
// (document.title, CSS --accent override). Cached at module
// scope so multiple mounts share one fetch — login page +
// AppShell both pull this.
let cached: Required<BrandingSettings> | null = null;
let cachedPromise: Promise<Required<BrandingSettings>> | null = null;

async function fetchBranding(): Promise<Required<BrandingSettings>> {
  if (cached) return cached;
  if (cachedPromise) return cachedPromise;
  cachedPromise = fetch('/api/branding', { credentials: 'include' })
    .then(r => r.ok ? r.json() : null)
    .then((b: BrandingSettings | null) => {
      cached = resolveBranding(b);
      applyBranding(cached);
      return cached;
    })
    .catch(() => {
      cached = resolveBranding(null);
      return cached;
    });
  return cachedPromise;
}

export function useBranding(): Required<BrandingSettings> {
  const [b, setB] = useState<Required<BrandingSettings>>(
    () => cached ?? DEFAULT_BRANDING
  );
  useEffect(() => {
    let active = true;
    fetchBranding().then(next => { if (active) setB(next); });
    return () => { active = false; };
  }, []);
  return b;
}

// applyBranding pushes side-effects to the document. Called
// once on first successful fetch; idempotent so a re-fetch
// (after an admin saves new branding) updates the same DOM.
function applyBranding(b: Required<BrandingSettings>) {
  if (typeof document !== 'undefined') {
    if (b.browserTitle) document.title = b.browserTitle;
  }
  if (typeof document !== 'undefined' && b.primaryColor) {
    document.documentElement.style.setProperty('--accent', b.primaryColor);
  }
}

// invalidateBranding drops the cache + re-fetches. Called from
// the Settings save handler so the rest of the app picks up
// new strings without a page reload.
export async function invalidateBranding(): Promise<Required<BrandingSettings>> {
  cached = null;
  cachedPromise = null;
  return fetchBranding();
}
