import type { ComponentType } from 'react';
import { useParams, Navigate, NavLink } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Empty } from '@/components/Spinner';
import { useAuth } from '@/components/AuthProvider';
import { IconLock } from '@/components/icons';

// Settings — the consolidated system-settings area. The fifteen former
// tab components that lived as functions inside one ~3950-line Settings.tsx
// are now one small file each under pages/settings/, mounted inside a left
// vertical sub-nav (the same `sys-subnav` / `sys-layout` / `sys-content`
// shell the System area uses). Deep links live at /settings/<slug>; bare
// /settings or an unknown slug redirects to the first section. Behaviour of
// each section — every save handler + API call — is unchanged; this was a
// pure file split.

import { SMTPTab } from './settings/SmtpTab';
import { ChannelsTab } from './settings/ChannelsTab';
import { MaintenanceTab } from './settings/MaintenanceTab';
import { AITab } from './settings/AiTab';
import { TempoTab } from './settings/TempoTab';
import { KibanaTab } from './settings/KibanaTab';
import { LDAPTab } from './settings/LdapTab';
import { SSOPresetsTab } from './settings/SsoTab';
import { RetentionTab } from './settings/RetentionTab';
import { AnomalyPromotionTab } from './settings/AnomalyTab';
import { BrandingTab } from './settings/BrandingTab';
import { CustomRolesTab } from './settings/RolesTab';
import { PipelineTab } from './settings/PipelineTab';
import { BackupTab } from './settings/BackupTab';

interface SettingsTab {
  slug: string;
  label: string;
  Comp: ComponentType;
}

// Slug order mirrors the former horizontal tab strip so deep links and the
// operator's muscle memory both stay stable.
const TABS: SettingsTab[] = [
  { slug: 'smtp',        label: 'SMTP',                  Comp: SMTPTab },
  { slug: 'channels',    label: 'Notification channels', Comp: ChannelsTab },
  { slug: 'maintenance', label: 'Maintenance windows',   Comp: MaintenanceTab },
  { slug: 'ai',          label: 'AI Copilot',            Comp: AITab },
  { slug: 'tempo',       label: 'Tempo backend',         Comp: TempoTab },
  { slug: 'kibana',      label: 'Kibana link',           Comp: KibanaTab },
  { slug: 'ldap',        label: 'LDAP / AD',             Comp: LDAPTab },
  { slug: 'sso',         label: 'SSO presets',           Comp: SSOPresetsTab },
  { slug: 'retention',   label: 'Data retention',        Comp: RetentionTab },
  { slug: 'anomaly',     label: 'Anomaly promotion',     Comp: AnomalyPromotionTab },
  { slug: 'branding',    label: 'Branding',              Comp: BrandingTab },
  { slug: 'roles',       label: 'Custom roles',          Comp: CustomRolesTab },
  { slug: 'pipeline',    label: 'Pipeline',              Comp: PipelineTab },
  { slug: 'backup',      label: 'Backup / Restore',      Comp: BackupTab },
];

export default function SettingsPage() {
  const { section } = useParams<{ section: string }>();
  const { user } = useAuth();

  // Admin-only — same gate the monolithic page enforced. Non-admins see the
  // lock state, not a blank page.
  if (user && user.role !== 'admin') {
    return (
      <>
        <Topbar title="Settings" />
        <div id="content">
          <Empty icon={<IconLock size={28} />} title="Admin access required">
            System settings are only available to administrators.
          </Empty>
        </div>
      </>
    );
  }

  // Bare /settings or an unknown slug → redirect to the first section.
  const active = TABS.find(t => t.slug === section);
  if (!active) {
    return <Navigate to={`/settings/${TABS[0].slug}`} replace />;
  }

  const Body = active.Comp;
  return (
    <>
      <Topbar title="Settings" />
      <div id="content">
        <div className="sys-layout">
          <nav className="sys-subnav" aria-label="Settings sections">
            <div className="sys-subnav-title">Settings</div>
            {TABS.map(t => (
              <NavLink
                key={t.slug}
                to={`/settings/${t.slug}`}
                className={({ isActive }) => 'sys-subnav-item' + (isActive ? ' active' : '')}>
                {t.label}
              </NavLink>
            ))}
          </nav>
          <div className="sys-content">
            <Body />
          </div>
        </div>
      </div>
    </>
  );
}
