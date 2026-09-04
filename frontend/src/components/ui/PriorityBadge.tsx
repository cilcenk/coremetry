// PriorityBadge — P1/P2/P3 rozeti, paylaşılan .badge tonları: P1 err /
// P2 warn / P3 gray. Problems + Triage Inbox + Exceptions aynı renk kodunu
// okur. v0.10.364'te Inbox.tsx'ten ui/ altına taşındı (Exceptions listesi
// ikinci tüketici oldu; kopya atom yerine tek kaynak).
export function PriorityBadge({ p, reason }: { p: 'P1' | 'P2' | 'P3'; reason?: string }) {
  const cls = p === 'P1' ? 'b-err' : p === 'P2' ? 'b-warn' : 'b-gray';
  return (
    <span className={`badge ${cls}`} title={reason ? `${p} — ${reason}` : p}>
      {p}
    </span>
  );
}
