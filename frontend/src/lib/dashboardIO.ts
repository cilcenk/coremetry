import type { Dashboard, Panel, DashboardVariable } from './types';

// Dashboard JSON export/import (v0.6.50). Pure functions so the
// validation is unit-testable and shared between the list page's
// Import button and the single-dashboard Export button.
//
// Wire format is deliberately the SUBSET of Dashboard that's
// portable across installs: name, description, panels, variables.
// id / createdAt / updatedAt are install-specific and stripped on
// export (re-minted by the backend on import). A `schema` tag lets
// future format changes be detected + migrated rather than failing
// opaquely.

const SCHEMA = 'coremetry.dashboard/v1';

export interface DashboardExport {
  schema: string;
  name: string;
  description: string;
  panels: Panel[];
  variables: DashboardVariable[];
}

// serializeDashboard produces the pretty-printed JSON string written
// to the downloaded file. Strips install-specific fields.
export function serializeDashboard(d: Dashboard): string {
  const out: DashboardExport = {
    schema: SCHEMA,
    name: d.name,
    description: d.description ?? '',
    panels: d.panels ?? [],
    variables: d.variables ?? [],
  };
  return JSON.stringify(out, null, 2);
}

// suggestedFilename — "apm-overview.dashboard.json" from a name.
// Slugified so it's filesystem-safe across OSes.
export function suggestedFilename(name: string): string {
  const slug = (name || 'dashboard')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 60) || 'dashboard';
  return `${slug}.dashboard.json`;
}

// parseDashboardImport validates an uploaded file's text and returns
// the create-payload (name/description/panels/variables). Throws an
// Error with an operator-readable message on any shape problem —
// the caller surfaces it via toast.
//
// Accepts BOTH the v1 export envelope AND a bare Dashboard object
// (so an operator who hand-edits or pulls a dashboard straight from
// the GET endpoint can still import it). Unknown `schema` values are
// rejected so a future-version file fails loud instead of importing
// a half-understood shape.
export function parseDashboardImport(text: string): {
  name: string; description: string; panels: Panel[]; variables: DashboardVariable[];
} {
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch {
    throw new Error('not valid JSON');
  }
  if (raw === null || typeof raw !== 'object') {
    throw new Error('expected a JSON object');
  }
  const obj = raw as Record<string, unknown>;

  // Schema gate: present → must be a version we know. Absent → treat
  // as a bare/legacy dashboard object (best-effort).
  if (typeof obj.schema === 'string' && obj.schema !== SCHEMA) {
    throw new Error(`unsupported schema "${obj.schema}" (this build reads ${SCHEMA})`);
  }

  const name = typeof obj.name === 'string' ? obj.name.trim() : '';
  if (!name) {
    throw new Error('missing "name"');
  }
  if (!Array.isArray(obj.panels)) {
    throw new Error('"panels" must be an array');
  }
  const description = typeof obj.description === 'string' ? obj.description : '';
  const variables = Array.isArray(obj.variables) ? (obj.variables as DashboardVariable[]) : [];
  return {
    name,
    description,
    panels: obj.panels as Panel[],
    variables,
  };
}
