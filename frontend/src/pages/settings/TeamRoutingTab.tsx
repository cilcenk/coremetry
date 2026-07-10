import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { Spinner, Empty } from '@/components/Spinner';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import { useServicesMetadata } from '@/lib/queries';
import { teamOptionsCI } from '@/lib/teamOptions';
import type { TeamContacts } from '@/lib/types';
import { Field, FlashBox, humanize } from './shared';

// ── Team routing tab (v0.8.429) ─────────────────────────────────────────────
// Operator ask: "yeni bir problem ilk defa geldiğinde ilgili sy ve ug
// team'e bildirim gönderilsin — mailleri katalogdan alsın." The catalog
// names the teams; this tab maps those names to e-mail addresses and
// arms the automatic problem-open mail (one per problem, notification_log
// dedup, template identical to a hand-configured email channel).
//
// Rows are pre-seeded from the catalog's owner/SRE team sets
// (teamOptionsCI — the same case-insensitive dedup the pickers use) so
// the operator fills blanks instead of retyping team names; teams
// without an address show a warning chip and are skipped silently at
// send time.

export function TeamRoutingTab() {
  const [tc, setTc] = useState<TeamContacts | null | undefined>(undefined);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);
  const [extraTeam, setExtraTeam] = useState('');

  const load = () => {
    setTc(undefined);
    api.getTeamContacts().then(setTc).catch(() => setTc(null));
  };
  useEffect(load, []);

  // Catalog-known team names (owner + SRE, case-insensitive dedup).
  const catalogQ = useServicesMetadata();
  const catalogTeams = useMemo(() => {
    const metas = Object.values(catalogQ.data ?? {});
    return teamOptionsCI([
      ...metas.map(m => m.ownerTeam),
      ...metas.map(m => m.sreTeam),
    ]);
  }, [catalogQ.data]);

  // Render rows = union of catalog teams and already-saved keys (a
  // saved contact for a team that later left the catalog must stay
  // visible/editable, not silently vanish).
  const rows = useMemo(() => {
    if (!tc) return [];
    const seen = new Set<string>();
    const out: string[] = [];
    for (const t of [...catalogTeams, ...Object.keys(tc.contacts)]) {
      const key = t.trim().toLowerCase();
      if (!key || seen.has(key)) continue;
      seen.add(key);
      out.push(t);
    }
    return out.sort((a, b) => a.localeCompare(b));
  }, [tc, catalogTeams]);

  if (tc === undefined) return <Spinner />;
  if (tc === null) return <Empty icon="⚠" title="Failed to load team routing settings" />;

  const contactFor = (team: string): string => {
    for (const [k, v] of Object.entries(tc.contacts)) {
      if (k.trim().toLowerCase() === team.trim().toLowerCase()) return v;
    }
    return '';
  };
  const setContact = (team: string, email: string) => {
    const next = { ...tc.contacts };
    // Rewrite under a single canonical key (the displayed one) so a
    // mixed-case duplicate can't linger with a stale value.
    for (const k of Object.keys(next)) {
      if (k.trim().toLowerCase() === team.trim().toLowerCase()) delete next[k];
    }
    if (email.trim() !== '') next[team] = email;
    setTc({ ...tc, contacts: next });
  };

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setMsg(null);
    try {
      const next = await api.putTeamContacts(tc);
      setTc(next);
      setMsg({ kind: 'ok', text: 'Saved.' });
    } catch (err) {
      setMsg({ kind: 'err', text: humanize(err) });
    } finally {
      setBusy(false);
    }
  };

  const missing = rows.filter(t => contactFor(t).trim() === '').length;

  return (
    <form onSubmit={save} style={{ maxWidth: 720 }}>
      <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>
        Yeni bir problem <b>ilk kez</b> açıldığında, servisin katalogdaki owner
        (ug) ve SRE (sy) takımlarına otomatik e-posta gönderilir — problem
        başına bir kez, SMTP ayarları üzerinden. Adresler virgülle
        çoğaltılabilir; adresi boş takımlar sessizce atlanır.
      </p>

      <div style={{ display: 'flex', gap: 16, alignItems: 'center', marginBottom: 14 }}>
        <label style={{ display: 'inline-flex', alignItems: 'center', gap: 8, fontSize: 13, cursor: 'pointer' }}>
          <input type="checkbox" checked={tc.enabled}
            onChange={e => setTc({ ...tc, enabled: e.target.checked })} />
          Team routing aktif
        </label>
        <label style={{ display: 'inline-flex', alignItems: 'center', gap: 8, fontSize: 13 }}>
          Minimum severity:
          <select value={tc.minSeverity ?? 'warning'}
            onChange={e => setTc({ ...tc, minSeverity: e.target.value as TeamContacts['minSeverity'] })}>
            <option value="info">info</option>
            <option value="warning">warning</option>
            <option value="critical">critical</option>
          </select>
        </label>
        {missing > 0 && (
          <span className="badge b-warn" title="Bu takımlar problem açılışında maillenmez — adres girin.">
            {missing} takımın e-postası eksik
          </span>
        )}
      </div>

      {rows.length === 0 ? (
        <Empty icon="👥" title="Katalogda takım yok">
          Service catalog'a owner/SRE team girildiğinde takımlar burada listelenir.
        </Empty>
      ) : (
        <div className="table-wrap" style={{ marginBottom: 14 }}>
          <table>
            <thead>
              <tr><th>Takım</th><th>E-posta adres(ler)i</th></tr>
            </thead>
            <tbody>
              {rows.map(team => {
                const v = contactFor(team);
                return (
                  <tr key={team.toLowerCase()}>
                    <td className="mono" style={{ whiteSpace: 'nowrap' }}>
                      {team}
                      {v.trim() === '' && (
                        <span className="badge b-warn" style={{ marginLeft: 8, fontSize: 9 }}>eksik</span>
                      )}
                    </td>
                    <td>
                      <input value={v}
                        onChange={e => setContact(team, e.target.value)}
                        placeholder="team@example.com, oncall@example.com"
                        aria-label={`${team} e-posta`}
                        style={{ width: '100%', fontSize: 12 }} />
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      {/* Katalog dışı takım ekleme — ör. henüz derive edilmemiş bir takım. */}
      <div style={{ display: 'flex', gap: 8, alignItems: 'flex-end', marginBottom: 14 }}>
        <Field label="Katalog dışı takım ekle">
          <input value={extraTeam} onChange={e => setExtraTeam(e.target.value)}
            placeholder="takım adı" style={{ width: 220 }} />
        </Field>
        <Button variant="secondary" size="sm" type="button"
          disabled={!extraTeam.trim()}
          onClick={() => {
            const t = extraTeam.trim();
            // Seed an empty row — the operator types the address next.
            setTc(prev => prev
              ? { ...prev, contacts: { ...prev.contacts, [t]: prev.contacts[t] ?? '' } }
              : prev);
            setExtraTeam('');
          }}>
          Ekle
        </Button>
      </div>

      {msg && <FlashBox kind={msg.kind}>{msg.text}</FlashBox>}
      <Button type="submit" disabled={busy}>{busy ? 'Saving…' : 'Save'}</Button>
    </form>
  );
}
