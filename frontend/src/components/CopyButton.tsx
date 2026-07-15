import { useState } from 'react';
import { copyToClipboard } from '@/lib/clipboard';

/**
 * Tiny clipboard button. Renders a small icon next to copyable text;
 * click → write to clipboard, briefly flips to a check mark.
 *
 * v0.8.550 — the hand-rolled fallback moved to lib/clipboard. Behaviour
 * gains one thing: the shared helper also falls back when writeText
 * REJECTS (permission denied, document not focused), where this copy only
 * fell back when the API was missing and swallowed a rejection into a
 * flash-less no-op.
 */
export function CopyButton({ value, title }: { value: string; title?: string }) {
  const [copied, setCopied] = useState(false);

  const onClick = async (e: React.MouseEvent) => {
    e.stopPropagation();   // don't trigger the row click underneath
    e.preventDefault();
    if (await copyToClipboard(value)) {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    }
  };

  return (
    <button
      type="button"
      onClick={onClick}
      title={title ?? (copied ? 'Copied!' : 'Copy to clipboard')}
      className={'copy-btn' + (copied ? ' copied' : '')}
      aria-label="Copy"
    >
      {copied ? '✓' : '⧉'}
    </button>
  );
}
