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

// depPillLines — v0.10.517 (operatör, prod topoloji: "oracle altta, db name
// pgts02 üstte yazsa daha iyi olur"): bağımlılık pilinin İKİ satırı.
// Somut kimlik (db.name / @instance) varsa BAŞLIK odur, sistem adı
// ("oracle") alt satıra iner — operatör hangi veritabanına gittiğini
// başlıkta okur; sistem türü zaten renk/ikon ve alt satırda. Kimlik yoksa
// eski düzen: başlık sistem adı, alt satır tür etiketi (kindLabel).
export function depPillLines(
  n: { service: string; subkind?: string; dbName?: string },
  kindLabel: string,
): { title: string; sub: string } {
  const system = n.subkind || n.service.replace(/^(db|queue|ext):/, '');
  const inst = depInstanceLabel(n);
  if (inst) return { title: inst, sub: system };
  return { title: system, sub: kindLabel };
}
