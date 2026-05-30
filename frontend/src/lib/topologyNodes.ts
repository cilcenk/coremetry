// Infra topology nodes are encoded as strings in `childNode` / node ids:
//   db:<system>            | db:<system>@<host>
//   queue:<system>         | queue:<system>@<host> | queue:<system>:<topic>
//   ext:<service>
//
// v0.7.31 — the queue form gained a `:<topic>` segment so each Kafka topic is a
// distinct node (bsa.kafka.core.cache.refresh stops collapsing every topic on a
// broker into one queue:kafka hairball). These pure helpers parse + label the
// strings; everything else treats childNode as an opaque id.

// infraNodeSystem returns the messaging/db SYSTEM (kafka, postgresql, …) from a
// node name, ignoring any @host or :topic suffix. Per-instance breakdowns are
// system-scoped, so the edge-instances panel queries on this.
//
// Regression guard: pre-v0.7.31 the caller did
// `childNode.slice(childNode.indexOf(':') + 1)`, which for the new
// `queue:kafka:topic` form wrongly yielded "kafka:topic" and broke the
// instances query. This extracts just the system.
export function infraNodeSystem(childNode: string): string {
  const colon = childNode.indexOf(':');
  if (colon < 0) return '';
  const rest = childNode.slice(colon + 1); // "kafka:topic" | "kafka@host" | "postgresql"
  return rest.split(':')[0].split('@')[0]; // → "kafka" | "postgresql"
}

// infraNodeLabel returns the human-readable label shown in the diagram for an
// infra node: the topic for a topic-scoped queue (the operationally meaningful
// identity), else the system (+host). The node's kind icon already conveys
// db/queue/external, so the prefix is dropped.
export function infraNodeLabel(name: string): string {
  if (name.startsWith('queue:')) {
    const rest = name.slice('queue:'.length); // "kafka:topic" | "kafka@host" | "kafka"
    const c = rest.indexOf(':');
    return c >= 0 ? rest.slice(c + 1) : rest; // topic, else system(+host)
  }
  if (name.startsWith('db:')) return name.slice('db:'.length);
  if (name.startsWith('ext:')) return name.slice('ext:'.length);
  return name;
}
