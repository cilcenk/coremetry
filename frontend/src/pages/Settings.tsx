import type { ComponentType } from 'react';
import { SETTINGS_TAB_INDEX } from './settings/tabIndex';
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
import { TeamRoutingTab } from './settings/TeamRoutingTab';
import { ApiTokensTab } from './settings/ApiTokensTab';
import { MaintenanceTab } from './settings/MaintenanceTab';
import { AITab } from './settings/AiTab';
import { KnowledgeTab } from './settings/KnowledgeTab';
import { TempoTab } from './settings/TempoTab';
import { MetricsBackendTab } from './settings/MetricsBackendTab';
import { TraceFacetsTab } from './settings/TraceFacetsTab';
import { DevOpsTab } from './settings/DevOpsTab';
import { McpServersTab } from './settings/McpServersTab';
import { ClustersTab } from './settings/ClustersTab';
import { InfluxTab } from './settings/InfluxTab';
import { EntitiesTab } from './settings/EntitiesTab';
import { ElasticTab } from './settings/ElasticTab';
import { KibanaTab } from './settings/KibanaTab';
import { LogBridgeTab } from './settings/LogBridgeTab';
import { LDAPTab } from './settings/LdapTab';
import { SSOPresetsTab } from './settings/SsoTab';
import { RetentionTab } from './settings/RetentionTab';
import { AnomalyPromotionTab } from './settings/AnomalyTab';
import { BrandingTab } from './settings/BrandingTab';
import { CustomRolesTab } from './settings/RolesTab';
import { PipelineTab } from './settings/PipelineTab';
import { BackupTab } from './settings/BackupTab';
import { DangerZoneTab } from './settings/DangerZoneTab';
import { PageShell } from '@/components/ui/PageShell';

interface SettingsTab {
  slug: string;
  label: string;
  Comp: ComponentType;
}

// Slug order mirrors the former horizontal tab strip so deep links and the
// operator's muscle memory both stay stable.
// v0.9.1272 — sekme kimlikleri (slug+label) YAPRAK dizinde
// (settings/tabIndex.ts): CommandPalette bileşen import'suz okur.
// Burası yalnız slug→bileşen eşlemesi; TABS dizinden türetilir.
// Tamlık kaynak-pin vitest'inde (settingsTabIndex.test.ts) — dizine
// girip buraya girmeyen (ya da tersi) slug testte patlar.
const TAB_COMPS: Record<string, ComponentType> = {
  'smtp': SMTPTab,
  'channels': ChannelsTab,
  'team-routing': TeamRoutingTab,
  'api-tokens': ApiTokensTab,
  'maintenance': MaintenanceTab,
  'ai': AITab,
  'knowledge': KnowledgeTab,
  'tempo': TempoTab,
  'metrics-backend': MetricsBackendTab,
  'trace-facets': TraceFacetsTab,
  'clusters': ClustersTab,
  'influx': InfluxTab,
  'entities': EntitiesTab,
  'elastic': ElasticTab,
  'kibana': KibanaTab,
  'log-bridge': LogBridgeTab,
  'devops': DevOpsTab,
  'mcp-servers': McpServersTab,
  'ldap': LDAPTab,
  'sso': SSOPresetsTab,
  'retention': RetentionTab,
  'anomaly': AnomalyPromotionTab,
  'branding': BrandingTab,
  'roles': CustomRolesTab,
  'pipeline': PipelineTab,
  'backup': BackupTab,
  'danger': DangerZoneTab,
};
const TABS: SettingsTab[] = SETTINGS_TAB_INDEX.map(t => ({ ...t, Comp: TAB_COMPS[t.slug] })).filter(t => !!t.Comp);

export default function SettingsPage() {
  const { section } = useParams<{ section: string }>();
  const { user } = useAuth();

  // Admin-only — same gate the monolithic page enforced. Non-admins see the
  // lock state, not a blank page.
  if (user && user.role !== 'admin') {
    return (
      <>
        <Topbar title="Settings" />
        <PageShell>
          <Empty icon={<IconLock size={28} />} title="Admin access required">
            System settings are only available to administrators.
          </Empty>
        </PageShell>
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
      <PageShell>
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
      </PageShell>
    </>
  );
}
