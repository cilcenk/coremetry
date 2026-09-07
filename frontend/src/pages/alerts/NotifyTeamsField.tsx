// NotifyTeamsField.tsx — v0.10.519: /alerts "Bildirim — ekipler" alanı.
// Operatör (2026-09-06): "spesifik alarmı tek bir ekibe ya da uygulama
// geliştirme ekibi + SY takımı şeklinde gönderebilme." Seçenekler servis
// kataloğunun sahip/SRE ekipleri (zaten yüklü useServicesMetadata; yeni
// katalog çekimi YOK) + serbest metin (team_contacts'ta tanımlı ama
// katalogda olmayan ekip). Adresler Settings → Team routing'de; adressiz
// ekip sessizce düşer, yönlendirme kaydı "kimse yok" der.
import { useMemo, useState } from 'react';
import type { RuleNotify } from '@/lib/types';
import { useServicesMetadata } from '@/lib/queries/services';
import { teamOptionsCI } from '@/lib/teamOptions';
import { Chip } from '@/components/ui/Chip';
import { Button } from '@/components/ui/Button';
import { Combobox } from '@/components/Combobox';
import { addNotifyTeam, removeNotifyTeam, notifyTeamOptions, NOTIFY_MAX_TEAMS } from './notifyTeams';

export function NotifyTeamsField({ value, onChange }: { value?: RuleNotify; onChange: (n: RuleNotify | undefined) => void }) {
  const [text, setText] = useState('');
  const catalogQ = useServicesMetadata();
  const catalogTeams = useMemo(() => {
    const metas = Object.values(catalogQ.data ?? {});
    return teamOptionsCI([...metas.map(m => m.ownerTeam), ...metas.map(m => m.sreTeam)]);
  }, [catalogQ.data]);
  const teams = value?.teams ?? [];
  const options = notifyTeamOptions(catalogTeams, teams);
  const add = () => { onChange(addNotifyTeam(value, text)); setText(''); };
  return (
    <div>
      <div style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
        {/* Paylaşılan Combobox (ChannelModal ekip alanlarıyla aynı): yerel liste
            zaten yüklü katalogdan; serbest metin Enter ile kalır. */}
        <div style={{ flex: 1, minWidth: 0 }}>
          <Combobox value={text} onChange={setText} options={options} width="100%"
            placeholder={teams.length ? 'Ekip ekle…' : 'Varsayılan: sahip + SRE. Ekip ekle…'}
            disabled={teams.length >= NOTIFY_MAX_TEAMS} onEnter={add} ariaLabel="Bildirim ekibi" />
        </div>
        <Button variant="secondary" size="sm" onClick={add} disabled={!text.trim() || teams.length >= NOTIFY_MAX_TEAMS}>Ekle</Button>
      </div>
      {teams.length > 0 && (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4, alignItems: 'center', marginTop: 6 }}>
          {teams.map(t => (
            <Chip key={t} size="xs" pill onRemove={() => onChange(removeNotifyTeam(value, t))} removeLabel={`${t} ekibini çıkar`}>{t}</Chip>
          ))}
          <label style={{ marginLeft: 'auto', display: 'inline-flex', gap: 8, alignItems: 'center', fontSize: 11, color: 'var(--text2)' }}>
            <span>
              <input type="radio" name="notify-mode" checked={(value?.mode ?? 'add') === 'add'}
                onChange={() => onChange({ teams, mode: 'add' })} /> sahip + SRE'ye ek
            </span>
            <span>
              <input type="radio" name="notify-mode" checked={value?.mode === 'only'}
                onChange={() => onChange({ teams, mode: 'only' })} /> yalnız bu ekipler
            </span>
          </label>
        </div>
      )}
    </div>
  );
}
