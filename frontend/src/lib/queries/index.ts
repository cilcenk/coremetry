// Barrel for the query hook layer. Pages and components import
// from `@/lib/queries` rather than from individual files so the
// internal split (per-domain) can change without churn at the
// call sites.

export { keys } from './keys';
export { useHealth } from './health';
export { useProblems, useOpenProblemCount } from './problems';
export {
  useLogPatternAnomalies, useTraceOpAnomalies, useMetricAnomalies,
  useAnomalyEvents, useAnomalySilences,
  useCreateAnomalySilence, useDeleteAnomalySilence,
} from './anomalies';
export {
  useServices, useServiceNames, useServiceMap,
  useServiceInfra, useServiceNeighbors, useServiceRuntime,
  useAllServiceRuntimes,
} from './services';
export {
  useSystemStats, useCardinality, useSamplingSettings, useUpdateSampling,
} from './admin';
export {
  useIncidents, useIncident, useIncidentEvents, useIncidentProblems,
  useCreateIncident, useUpdateIncident,
} from './incidents';
export {
  useAlertRules,
  useCreateAlertRule, useUpdateAlertRule,
  useDeleteAlertRule, useEnableAlertRule,
} from './alerts';
export { useEventStream } from './eventStream';
