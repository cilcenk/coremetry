import { Topbar } from '@/components/Topbar';
import { SavedViewsBar } from '@/components/SavedViewsBar';
import { TriageCrumb } from '@/components/TriageCrumb';
import { AnomalyStreams } from './streams';

// /anomalies — live early-warning streams. Distinct from /problems
// (which holds the assignable exception inbox + alert-rule firings).
// This page is observation-only: rows appear when a detector sees
// something unusual and disappear when it clears. For triage and
// assignment, the operator switches to /problems.
export default function AnomalyStreamsPage() {
  return (
    <>
      <Topbar title="Anomalies" />
      <div id="content">
        <TriageCrumb label="Anomalies" />
        <SavedViewsBar page="anomalies" />
        <AnomalyStreams />
      </div>
    </>
  );
}
