// settings/tabIndex — Settings sekmelerinin YAPRAK dizini (v0.9.1272,
// Dynatrace-parite #3). Ayrı modül olmasının tek nedeni bundle:
// CommandPalette bu listeyi okur; Settings.tsx'ten okusaydı 23 sekme
// bileşeni palet chunk'ına sürüklenirdi. Settings.tsx TABS'ını BU
// dizinden türetir (tek kaynak); eşleme tamlığı settingsTabIndex
// vitest'inde kaynak-pin ile kilitli.
export interface SettingsTabRef { slug: string; label: string }

export const SETTINGS_TAB_INDEX: SettingsTabRef[] = [
  { slug: 'smtp', label: 'SMTP' },
  { slug: 'channels', label: 'Notification channels' },
  { slug: 'team-routing', label: 'Team routing' },
  { slug: 'api-tokens', label: 'API Tokens' },
  { slug: 'maintenance', label: 'Maintenance windows' },
  { slug: 'ai', label: 'CoSRE' },
  { slug: 'knowledge', label: 'Knowledge (RAG)' },
  { slug: 'tempo', label: 'Tempo backend' },
  { slug: 'metrics-backend', label: 'Metrik backend’i' },
  { slug: 'clusters', label: 'Remote clusters' },
  { slug: 'elastic', label: 'Elasticsearch logs' },
  { slug: 'kibana', label: 'Kibana link' },
  { slug: 'log-bridge', label: 'Log köprüsü' },
  { slug: 'devops', label: 'Kod entegrasyonu' },
  { slug: 'mcp-servers', label: 'MCP sunucuları' },
  { slug: 'ldap', label: 'LDAP / AD' },
  { slug: 'sso', label: 'SSO presets' },
  { slug: 'retention', label: 'Data retention' },
  { slug: 'anomaly', label: 'Anomaly promotion' },
  { slug: 'branding', label: 'Branding' },
  { slug: 'roles', label: 'Custom roles' },
  { slug: 'pipeline', label: 'Pipeline' },
  { slug: 'backup', label: 'Backup / Restore' },
  { slug: 'danger', label: 'Danger zone' },
];
