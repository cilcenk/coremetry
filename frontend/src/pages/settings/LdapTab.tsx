import { useEffect, useState, type FormEvent } from 'react';
import { Spinner } from '@/components/Spinner';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import type { LDAPConfig, LDAPGroupRoleMapping, Role } from '@/lib/types';
import { Row, Field2, SectionTitle } from './shared';
import { LDAPUserPicker } from './LdapUserPicker';

// LDAPTab — enterprise auth configuration. Three sections:
//   1. Connection — host/port/TLS/bind credentials (Test button verifies
//      the service-account bind without saving).
//   2. Group → Role mapping — admin lists AD groups whose members
//      should land as admin / editor / viewer in Coremetry. First
//      match wins (admin > editor > viewer).
//   3. Directory user picker — search the LDAP for a user, pick a
//      role, post to /api/users/from-ldap to pre-provision them so
//      they show up in /users with explicit role even before first
//      sign-in.
//
// Bind password is never echoed back from the API; the GET endpoint
// returns the literal "__SET__" sentinel when one is saved, which we
// translate into a "leave empty to keep current" affordance.
export function LDAPTab() {
  const [cfg, setCfg] = useState<LDAPConfig | null>(null);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  // Track whether the bind password input has been touched. We wipe
  // the "__SET__" sentinel on first focus so the user types a fresh
  // value into an empty box, not on top of the placeholder.
  const [pwTouched, setPwTouched] = useState(false);

  useEffect(() => {
    api.getLDAPSettings().then(c => setCfg(c)).catch(() => setCfg(emptyLDAP()));
  }, []);

  if (!cfg) return <Spinner />;

  const update = (patch: Partial<LDAPConfig>) => setCfg({ ...cfg, ...patch });

  // Strip the "__SET__" sentinel before any outbound call. Server
  // treats empty BindPassword as "preserve saved one".
  const outbound = (): LDAPConfig => {
    const c = { ...cfg };
    if (!pwTouched && c.bindPassword === '__SET__') c.bindPassword = '';
    return c;
  };

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      const next = await api.putLDAPSettings(outbound());
      setCfg(next);
      setPwTouched(false);
      setMsg({ kind: 'ok', text: 'Saved.' });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Save failed' });
    } finally {
      setBusy(false);
    }
  };

  const test = async () => {
    setBusy(true); setMsg(null);
    try {
      const r = await api.testLDAPConnection(outbound());
      if (r.ok) setMsg({ kind: 'ok', text: 'Connection OK — bind succeeded.' });
      else setMsg({ kind: 'err', text: r.error || 'Connection failed' });
    } catch (err) {
      setMsg({ kind: 'err', text: err instanceof Error ? err.message : 'Test failed' });
    } finally {
      setBusy(false);
    }
  };

  return (
    <div style={{ maxWidth: 800 }}>
      <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: 6 }}>LDAP / Active Directory</h2>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        Authenticate users against your corporate directory. Domain users sign in
        with their normal credentials; their Coremetry role is resolved from AD
        group membership via the mapping table below. Local accounts (bootstrap
        admin) keep working as a fallback.
      </p>

      <form onSubmit={save} style={{
        padding: 16, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        {/* ── Section 1: Enable + connection ─────────────────────────── */}
        <label style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 14 }}>
          <input type="checkbox" checked={cfg.enabled}
                 onChange={e => update({ enabled: e.target.checked })} />
          <span style={{ fontSize: 13, fontWeight: 600 }}>Enable LDAP authentication</span>
        </label>

        <SectionTitle>Connection</SectionTitle>
        <Row>
          <Field2 label="Host" hint="e.g. ldap.corp.example.com">
            <input value={cfg.host} onChange={e => update({ host: e.target.value })}
                   placeholder="ldap.example.com" style={{ width: '100%' }} />
          </Field2>
          <Field2 label="Port" hint="636 for LDAPS, 389 for plain/StartTLS" small>
            <input type="number" value={cfg.port || ''}
                   onChange={e => update({ port: parseInt(e.target.value, 10) || 0 })}
                   placeholder="636" style={{ width: '100%' }} />
          </Field2>
        </Row>
        <Row>
          <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12 }}>
            <input type="checkbox" checked={cfg.useTLS}
                   onChange={e => update({ useTLS: e.target.checked, startTLS: e.target.checked ? false : cfg.startTLS })} />
            Use LDAPS (direct TLS, default port 636)
          </label>
          <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12 }}>
            <input type="checkbox" checked={cfg.startTLS} disabled={cfg.useTLS}
                   onChange={e => update({ startTLS: e.target.checked })} />
            StartTLS upgrade (legacy, port 389)
          </label>
          <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12 }}>
            <input type="checkbox" checked={cfg.skipVerify}
                   onChange={e => update({ skipVerify: e.target.checked })} />
            Skip cert verification (dev only)
          </label>
        </Row>
        <Field2 label="Internal CA certificate (PEM, optional)"
                hint="Paste the PEM bundle if your AD uses an internal CA. Leave empty to use system roots.">
          <textarea value={cfg.caCert || ''} onChange={e => update({ caCert: e.target.value })}
                    rows={3}
                    style={{ width: '100%', fontFamily: 'monospace', fontSize: 11 }}
                    placeholder="-----BEGIN CERTIFICATE-----..." />
        </Field2>

        <Row>
          <Field2 label="Bind DN (service account)" hint="Used to look up users + groups">
            <input value={cfg.bindDN} onChange={e => update({ bindDN: e.target.value })}
                   placeholder="CN=svc-coremetry,OU=Service Accounts,DC=corp,DC=example"
                   style={{ width: '100%' }} />
          </Field2>
        </Row>
        <Row>
          <Field2 label="Bind password"
                  hint={cfg.bindPassword === '__SET__' && !pwTouched
                    ? 'A password is saved. Leave empty to keep it; type a new one to rotate.'
                    : 'Service-account password.'}>
            <input type="password"
                   value={pwTouched ? cfg.bindPassword : (cfg.bindPassword === '__SET__' ? '' : cfg.bindPassword)}
                   onFocus={() => { if (!pwTouched && cfg.bindPassword === '__SET__') { setPwTouched(true); update({ bindPassword: '' }); } }}
                   onChange={e => { setPwTouched(true); update({ bindPassword: e.target.value }); }}
                   placeholder={cfg.bindPassword === '__SET__' && !pwTouched ? '••••••••••••' : ''}
                   autoComplete="off" style={{ width: '100%' }} />
          </Field2>
        </Row>

        {/* ── Section 2: Search ──────────────────────────────────────── */}
        <SectionTitle>User search</SectionTitle>
        <Row>
          <Field2 label="Base DN" hint="Where to search for users">
            <input value={cfg.baseDN} onChange={e => update({ baseDN: e.target.value })}
                   placeholder="OU=Users,DC=corp,DC=example" style={{ width: '100%' }} />
          </Field2>
        </Row>
        <Row>
          <Field2 label="User search filter" hint="{{username}} is replaced at runtime">
            <input value={cfg.userSearchFilter}
                   onChange={e => update({ userSearchFilter: e.target.value })}
                   placeholder="(sAMAccountName={{username}})" style={{ width: '100%' }} />
          </Field2>
        </Row>
        <Row>
          <Field2 label="User attribute" small>
            <input value={cfg.userAttribute} onChange={e => update({ userAttribute: e.target.value })}
                   placeholder="sAMAccountName" style={{ width: '100%' }} />
          </Field2>
          <Field2 label="Email attribute" small>
            <input value={cfg.emailAttribute} onChange={e => update({ emailAttribute: e.target.value })}
                   placeholder="mail" style={{ width: '100%' }} />
          </Field2>
          <Field2 label="Display attribute" small>
            <input value={cfg.displayAttribute} onChange={e => update({ displayAttribute: e.target.value })}
                   placeholder="displayName" style={{ width: '100%' }} />
          </Field2>
          <Field2 label="Team attribute" small
            hint="boş = department→ou · dn-ou = DN'deki en derin OU (alt ekip) · veya attribute adı">
            <input value={cfg.teamAttribute ?? ''} onChange={e => update({ teamAttribute: e.target.value })}
                   placeholder="örn. displayName / division / dn-ou" style={{ width: '100%' }} />
          </Field2>
          <Field2 label="Team regex" small
            hint={'opsiyonel — ilk yakalama grubu ekip olur; ör. "…ÜNVAN-Ekip" için -([^-]+)$ (son tireden sonrası). Eşleşme yoksa ekip boş kalır.'}>
            <input value={cfg.teamRegex ?? ''} onChange={e => update({ teamRegex: e.target.value })}
                   placeholder="-([^-]+)$" style={{ width: '100%', fontFamily: 'ui-monospace, monospace' }} />
          </Field2>
        </Row>
        {/* v0.8.430 — attribute discovery. Operator-reported: users.team
            herkes için üst division ("TEKNOLOJİ") geliyordu çünkü AD bunu
            department'ta tutuyor; alt ekibin HANGİ attribute'ta olduğunu
            görmek için bir kullanıcının tüm directory attribute'larını dök. */}
        <InspectPanel />

        {/* ── Section 3: Groups ──────────────────────────────────────── */}
        <SectionTitle>Group lookup (optional)</SectionTitle>
        <p style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 8 }}>
          Some directories don't populate <code>memberOf</code> on user entries.
          Set a group base + filter to do a separate group search; otherwise leave blank.
        </p>
        <Row>
          <Field2 label="Group search base">
            <input value={cfg.groupSearchBase}
                   onChange={e => update({ groupSearchBase: e.target.value })}
                   placeholder="OU=Groups,DC=corp,DC=example" style={{ width: '100%' }} />
          </Field2>
        </Row>
        <Row>
          <Field2 label="Group filter" hint="{{userDN}} is replaced at runtime">
            <input value={cfg.groupFilter} onChange={e => update({ groupFilter: e.target.value })}
                   placeholder="(member={{userDN}})" style={{ width: '100%' }} />
          </Field2>
        </Row>
        <Row>
          <label style={{
            display: 'flex', alignItems: 'flex-start', gap: 8,
            fontSize: 12, cursor: 'pointer', padding: '8px 0',
          }}>
            <input type="checkbox"
              checked={!!cfg.skipMemberOfFetch}
              onChange={e => update({ skipMemberOfFetch: e.target.checked })}
              style={{ marginTop: 2 }} />
            <span>
              <span style={{ fontWeight: 600 }}>
                Skip memberOf in user search (AD MaxValRange / 1MB workaround)
              </span>
              <span style={{ display: 'block', color: 'var(--text3)', fontSize: 11, marginTop: 2, lineHeight: 1.5 }}>
                Set when users with very large nested-group memberships trip
                AD's <code>MaxValRange</code> (1500) or <code>MaxReceiveBuffer</code>
                (~1MB) caps and login fails with "size limit" / "1MB area" /
                <code>LDAP_ADMIN_LIMIT_EXCEEDED</code>. Drops the
                <code>memberOf</code> attribute from the user search; the
                separate group search (above) then becomes the authoritative
                role source. <b>Requires Group search base + filter to be set.</b>
              </span>
            </span>
          </label>
        </Row>

        {/* ── Section 4: Role mapping ────────────────────────────────── */}
        <SectionTitle>Group → role mapping</SectionTitle>
        <p style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 8 }}>
          When a user signs in, we walk their group memberships and pick the
          highest-privilege match. Group string can be a full DN or a CN
          fragment — match is case-insensitive substring.
        </p>
        <table style={{ width: '100%', fontSize: 12, borderCollapse: 'collapse', marginBottom: 8 }}>
          <thead>
            <tr style={{ background: 'var(--bg)', color: 'var(--text2)' }}>
              <th style={{ padding: 6, textAlign: 'left' }}>Group (DN or CN substring)</th>
              <th style={{ padding: 6, textAlign: 'left', width: 160 }}>Role</th>
              <th style={{ padding: 6, width: 32 }}></th>
            </tr>
          </thead>
          <tbody>
            {(cfg.groupRoleMap || []).map((m, i) => (
              <tr key={i}>
                <td style={{ padding: 4 }}>
                  <input value={m.group}
                         onChange={e => updateMapping(cfg, setCfg, i, { group: e.target.value })}
                         placeholder="CN=Coremetry-Admins,OU=Groups,DC=corp,DC=example"
                         style={{ width: '100%' }} />
                </td>
                <td style={{ padding: 4 }}>
                  <select value={m.role}
                          onChange={e => updateMapping(cfg, setCfg, i, { role: e.target.value as Role })}>
                    <option value="admin">admin</option>
                    <option value="editor">editor</option>
                    <option value="viewer">viewer</option>
                  </select>
                </td>
                <td style={{ padding: 4, textAlign: 'center' }}>
                  <Button variant="secondary" size="sm"
                          onClick={() => removeMapping(cfg, setCfg, i)}
                          style={{ color: 'var(--err)' }}>×</Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        <Button type="button" variant="secondary" size="sm"
                onClick={() => addMapping(cfg, setCfg)}>
          + Add mapping
        </Button>

        <Row>
          <Field2 label="Default role" hint="Applied when no group matches">
            <select value={cfg.defaultRole}
                    onChange={e => update({ defaultRole: e.target.value as Role })}
                    style={{ width: '100%' }}>
              <option value="viewer">viewer</option>
              <option value="editor">editor</option>
              <option value="admin">admin</option>
            </select>
          </Field2>
        </Row>

        {msg && (
          <div style={{
            margin: '14px 0 12px', padding: '6px 10px', borderRadius: 4, fontSize: 12,
            color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)',
            background: msg.kind === 'ok' ? 'rgba(63,185,80,0.10)' : 'rgba(220,38,38,0.08)',
            border: `1px solid ${msg.kind === 'ok' ? 'rgba(63,185,80,0.35)' : 'rgba(220,38,38,0.3)'}`,
          }}>
            {msg.text}
          </div>
        )}

        <div style={{ display: 'flex', gap: 8, marginTop: 16 }}>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? 'Saving…' : 'Save'}
          </Button>
          <Button type="button" variant="secondary" disabled={busy} onClick={test}>
            Test connection
          </Button>
        </div>
      </form>

      {cfg.enabled && (
        <LDAPUserPicker />
      )}
    </div>
  );
}

function emptyLDAP(): LDAPConfig {
  return {
    enabled: false, host: '', port: 636, useTLS: true, startTLS: false,
    skipVerify: false, caCert: '',
    bindDN: '', bindPassword: '', baseDN: '',
    userSearchFilter: '(sAMAccountName={{username}})',
    userAttribute: 'sAMAccountName', emailAttribute: 'mail',
    displayAttribute: 'displayName',
    groupSearchBase: '', groupFilter: '(member={{userDN}})',
    defaultRole: 'viewer', groupRoleMap: [],
  };
}

function updateMapping(
  cfg: LDAPConfig, set: (c: LDAPConfig) => void,
  i: number, patch: Partial<LDAPGroupRoleMapping>,
) {
  const next = [...cfg.groupRoleMap];
  next[i] = { ...next[i], ...patch };
  set({ ...cfg, groupRoleMap: next });
}
function addMapping(cfg: LDAPConfig, set: (c: LDAPConfig) => void) {
  set({ ...cfg, groupRoleMap: [...cfg.groupRoleMap, { group: '', role: 'viewer' }] });
}
function removeMapping(cfg: LDAPConfig, set: (c: LDAPConfig) => void, i: number) {
  set({ ...cfg, groupRoleMap: cfg.groupRoleMap.filter((_, j) => j !== i) });
}


// InspectPanel (v0.8.430) — bir kullanıcının okunabilir TÜM directory
// attribute'larını listeler; "Team attribute" için doğru alanı seçmeden
// önce nereye bakacağını gösterir. Salt-okunur, isteğe bağlı çağrı —
// liste render'ında hiçbir fetch yok.
function InspectPanel() {
  const [u, setU] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const [res, setRes] = useState<Awaited<ReturnType<typeof api.ldapInspect>> | null>(null);

  const run = async () => {
    if (!u.trim()) return;
    setBusy(true); setErr(''); setRes(null);
    try {
      setRes(await api.ldapInspect(u.trim()));
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div style={{
      marginTop: 10, padding: '10px 12px', borderRadius: 6,
      background: 'var(--bg1)', border: '1px solid var(--border)',
    }}>
      <div style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 8 }}>
        <span style={{ fontSize: 11, fontWeight: 700, textTransform: 'uppercase', letterSpacing: 0.4, color: 'var(--text2)' }}>
          Kullanıcı incele
        </span>
        <input value={u} onChange={e => setU(e.target.value)}
          onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); void run(); } }}
          placeholder="kullanıcı adı" style={{ width: 200, fontSize: 12 }} />
        <Button variant="secondary" size="sm" type="button" onClick={() => { void run(); }} disabled={busy || !u.trim()}>
          {busy ? 'Sorgulanıyor…' : 'Attribute\'ları getir'}
        </Button>
        {err && <span style={{ fontSize: 12, color: 'var(--err)' }}>{err}</span>}
      </div>
      {res && (
        <>
          <div className="mono" style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 6, overflowWrap: 'anywhere' }}>
            {res.dn}
          </div>
          <div style={{ fontSize: 11, marginBottom: 8, display: 'flex', gap: 12, flexWrap: 'wrap' }}>
            <span>Şu anki team değeri: <b className="mono">{res.team || '—'}</b></span>
            <span title="Team attribute = dn-ou seçilirse bu değer yazılır">
              dn-ou verirdi: <b className="mono">{res.deepestOu || '—'}</b>
            </span>
          </div>
          <div className="table-wrap" style={{ maxHeight: 320, overflowY: 'auto' }}>
            <table>
              <thead><tr><th>Attribute</th><th>Değer(ler)</th></tr></thead>
              <tbody>
                {Object.entries(res.attributes).sort(([a], [b]) => a.localeCompare(b)).map(([k, vs]) => (
                  <tr key={k}>
                    <td className="mono" style={{ whiteSpace: 'nowrap', fontSize: 11 }}>{k}</td>
                    <td className="mono" style={{ fontSize: 11, overflowWrap: 'anywhere' }}>{vs.join(' · ')}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
    </div>
  );
}
