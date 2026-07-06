import { useState } from 'react';
import { Button } from '@/components/ui/Button';

// SSOPresetsTab — provider-template reference for OIDC + the
// trusted-header proxy mode. Today OIDC + trusted-header are
// configured via config.yaml / env vars (the runtime-persisted
// flow lives next to LDAP / AI / Branding and is queued for a
// follow-up); meanwhile operators paste a known-good snippet
// per provider here, apply it to their deployment, and
// restart the pod.
//
// Each card is a copy-paste-ready YAML block with the issuer
// URL pattern, recommended scopes, and any provider-specific
// notes (e.g. Azure AD's tenant placeholder, Keycloak's realm
// segment, oauth2-proxy's trusted-proxy CIDR). Same shape as
// the Profiling setup recipes that ship per-language.
export function SSOPresetsTab() {
  type Preset = { key: string; label: string; description: string; yaml: string };
  const presets: Preset[] = [
    {
      key: 'keycloak',
      label: 'Keycloak',
      description: 'Most common self-hosted identity provider for banks. Replace <realm> with your realm name; Coremetry discovers the rest via /.well-known/openid-configuration.',
      yaml:
`auth:
  oidc:
    enabled: true
    issuer_url: "https://keycloak.example.com/realms/<realm>"
    client_id: "coremetry"
    client_secret: "<from-keycloak-client-credentials>"
    redirect_url: "https://coremetry.example.com/api/auth/oidc/callback"
    scopes: ["openid", "email", "profile"]
    display_name: "Keycloak"
    default_role: "viewer"
    allowed_domains: []   # optional ["bank.com"]`,
    },
    {
      key: 'dex',
      label: 'Dex',
      description: 'CoreOS Dex — popular OIDC bridge in front of LDAP/SAML/GitHub for k8s shops. Issuer URL is the public host:port the SPA can reach.',
      yaml:
`auth:
  oidc:
    enabled: true
    issuer_url: "https://dex.example.com"
    client_id: "coremetry"
    client_secret: "<dex-static-client-secret>"
    redirect_url: "https://coremetry.example.com/api/auth/oidc/callback"
    scopes: ["openid", "email", "profile", "groups"]
    display_name: "Dex"
    default_role: "viewer"`,
    },
    {
      key: 'google',
      label: 'Google Workspace',
      description: 'Hosted Google. Restrict to a single GSuite domain via allowed_domains so anyone with a personal gmail.com can\'t sign in.',
      yaml:
`auth:
  oidc:
    enabled: true
    issuer_url: "https://accounts.google.com"
    client_id: "<google-cloud-oauth-client-id>"
    client_secret: "<google-cloud-oauth-secret>"
    redirect_url: "https://coremetry.example.com/api/auth/oidc/callback"
    scopes: ["openid", "email", "profile"]
    display_name: "Google"
    default_role: "viewer"
    allowed_domains: ["yourcompany.com"]`,
    },
    {
      key: 'azure-ad',
      label: 'Azure AD (Entra)',
      description: 'Microsoft Entra ID (formerly Azure AD). Replace <tenant-id> with your tenant GUID; the v2.0 endpoint is the one to use.',
      yaml:
`auth:
  oidc:
    enabled: true
    issuer_url: "https://login.microsoftonline.com/<tenant-id>/v2.0"
    client_id: "<app-registration-client-id>"
    client_secret: "<app-registration-client-secret>"
    redirect_url: "https://coremetry.example.com/api/auth/oidc/callback"
    scopes: ["openid", "email", "profile"]
    display_name: "Microsoft"
    default_role: "viewer"`,
    },
    {
      key: 'okta',
      label: 'Okta',
      description: 'Okta-as-a-service. Replace <your-okta-domain> with the host Okta assigned you (e.g. acme.okta.com).',
      yaml:
`auth:
  oidc:
    enabled: true
    issuer_url: "https://<your-okta-domain>"
    client_id: "<okta-app-client-id>"
    client_secret: "<okta-app-client-secret>"
    redirect_url: "https://coremetry.example.com/api/auth/oidc/callback"
    scopes: ["openid", "email", "profile"]
    display_name: "Okta"
    default_role: "viewer"`,
    },
    {
      key: 'auth0',
      label: 'Auth0',
      description: 'Hosted Auth0. Issuer URL includes the tenant slug.',
      yaml:
`auth:
  oidc:
    enabled: true
    issuer_url: "https://<your-tenant>.auth0.com/"
    client_id: "<auth0-application-client-id>"
    client_secret: "<auth0-application-client-secret>"
    redirect_url: "https://coremetry.example.com/api/auth/oidc/callback"
    scopes: ["openid", "email", "profile"]
    display_name: "Auth0"
    default_role: "viewer"`,
    },
    {
      key: 'oauth2-proxy',
      label: 'oauth2-proxy / IAP (trusted headers)',
      description: 'Banks running oauth2-proxy / Google IAP / Cloudflare Access in front of every internal app — Coremetry trusts the upstream identity headers without re-doing OIDC itself. trusted_proxies CIDR is REQUIRED so an attacker bypassing the proxy can\'t spoof X-Auth-Request-Email.',
      yaml:
`auth:
  trusted_header:
    enabled: true
    email_header: "X-Auth-Request-Email"
    user_header: "X-Auth-Request-User"
    groups_header: "X-Auth-Request-Groups"
    auto_provision: true        # first-sight email lands as DefaultRole
    default_role: "viewer"
    trusted_proxies:            # ← REQUIRED — your oauth2-proxy node CIDRs
      - "10.0.0.0/8"
      - "172.16.0.0/12"`,
    },
  ];
  const [activeKey, setActiveKey] = useState(presets[0].key);
  const active = presets.find(p => p.key === activeKey) ?? presets[0];
  const [copied, setCopied] = useState(false);
  const copy = () => {
    navigator.clipboard.writeText(active.yaml)
      .then(() => {
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      })
      .catch(() => {
        // Clipboard API can reject when the page isn't in a
        // secure context, when the tab loses focus mid-call,
        // or when the user denied permission. Silent fail —
        // the operator still has the visible YAML to copy
        // manually. Without the catch this surfaces as an
        // unhandled promise rejection in the console.
      });
  };
  return (
    <div style={{ maxWidth: 920 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>SSO presets</h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16, lineHeight: 1.6 }}>
        Provider-specific config snippets for OIDC + the oauth2-proxy trusted-header mode.
        Paste the YAML into your <code>config.yaml</code> (or the equivalent
        <code>COREMETRY_OIDC_*</code> / <code>COREMETRY_TRUSTED_HEADER_*</code> env vars in your
        deployment), then restart the pod. Live runtime persistence of OIDC config is queued for
        a follow-up — for now the file-driven path keeps things auditable in source control.
      </p>
      <div style={{ display: 'flex', gap: 4, marginBottom: 12, borderBottom: '1px solid var(--border)' }}>
        {presets.map(p => (
          <button key={p.key} onClick={() => setActiveKey(p.key)}
            style={{
              padding: '5px 14px', fontSize: 12, fontWeight: 600, cursor: 'pointer',
              background: 'transparent', border: 'none', borderBottom: '2px solid',
              borderColor: activeKey === p.key ? 'var(--accent)' : 'transparent',
              color: activeKey === p.key ? 'var(--text)' : 'var(--text3)',
            }}>
            {p.label}
          </button>
        ))}
      </div>
      <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 10, lineHeight: 1.6 }}>
        {active.description}
      </div>
      <div style={{ position: 'relative' }}>
        <Button variant="secondary" size="sm" onClick={copy}
          style={{ position: 'absolute', top: 8, right: 8 }}>
          {copied ? '✓ copied' : 'Copy'}
        </Button>
        <pre style={{
          margin: 0, padding: 14, background: 'var(--bg)',
          border: '1px solid var(--border)', borderRadius: 6,
          fontSize: 12, lineHeight: 1.6, overflowX: 'auto',
          fontFamily: 'ui-monospace, SFMono-Regular, monospace',
        }}>
          <code>{active.yaml}</code>
        </pre>
      </div>
      <div style={{
        marginTop: 14, padding: '10px 12px', borderRadius: 6,
        background: 'var(--bg2)', border: '1px solid var(--border)',
        fontSize: 12, color: 'var(--text2)', lineHeight: 1.6,
      }}>
        <b>Notes:</b>
        <ul style={{ paddingLeft: 18, margin: '6px 0 0' }}>
          <li>Restart the pod after applying — OIDC discovery runs at boot.</li>
          <li>Local username/password login stays available alongside OIDC so admins always have a fallback.</li>
          <li>Trusted-header mode <b>requires</b> <code>trusted_proxies</code> — empty list = boot refused. Source-IP gate prevents header spoofing from any caller outside the proxy mesh.</li>
          <li>First-sight OIDC / trusted-header users land with <code>default_role</code> (viewer). Admins promote via <code>/users</code>.</li>
        </ul>
      </div>
    </div>
  );
}
