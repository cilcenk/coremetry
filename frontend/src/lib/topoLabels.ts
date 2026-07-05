// topoLabels — pure label helpers for TopologyFlowGraph dependency pills
// (v0.8.297, operator-reported: the db pill only said "oracle"; the operator
// wants the instance name or db.name visible where calls land in
// postgres/mssql/oracle/redis).

// depInstanceLabel — the concrete identity for a dependency pill's sub-line:
// db.name when known ("COREBANK"), else the @instance suffix of the node id
// ("db:oracle@oracle-prod" → "oracle-prod") unless it merely repeats the
// system name ("db:redis@redis" adds nothing), else null so the caller falls
// back to the generic kind label.
export function depInstanceLabel(n: { service: string; subkind?: string; dbName?: string }): string | null {
  if (n.dbName) return n.dbName;
  const at = n.service.indexOf('@');
  if (at >= 0) {
    const inst = n.service.slice(at + 1);
    if (inst && inst !== n.subkind) return inst;
  }
  return null;
}
