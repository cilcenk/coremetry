// Pure helpers for the resizable/reorderable Traces table columns (v0.7.47).
// The DOM drag/resize wiring lives in the page; the order math is here so it's
// unit-testable.

// reconcileColOrder keeps a persisted column order valid as the FIXED set stays
// constant and the attribute columns (extras) come and go: drop ids that no
// longer exist, preserve the operator's arrangement of the rest, and append any
// fixed/extra column not yet in the order (first run, a newly-added attr column,
// or a new build that introduced a column). Stable: returns the input order's
// elements in place, only filtering/appending.
export function reconcileColOrder(order: string[], fixed: string[], extras: string[]): string[] {
  const valid = new Set([...fixed, ...extras]);
  const out = order.filter((id) => valid.has(id));
  const present = new Set(out);
  for (const id of [...fixed, ...extras]) {
    if (!present.has(id)) {
      out.push(id);
      present.add(id);
    }
  }
  return out;
}

// moveColumn returns a new order with dragId moved to immediately BEFORE
// targetId. No-op when dragId === targetId; appends dragId if targetId is
// absent. Used by the header drag-and-drop reorder.
export function moveColumn(order: string[], dragId: string, targetId: string): string[] {
  if (dragId === targetId) return order;
  const without = order.filter((id) => id !== dragId);
  const idx = without.indexOf(targetId);
  if (idx < 0) return [...without, dragId];
  return [...without.slice(0, idx), dragId, ...without.slice(idx)];
}
