'use client';
import { useState } from 'react';

/**
 * Tiny clipboard button. Renders a small icon next to copyable text;
 * click → write to clipboard, briefly flips to a check mark.
 */
export function CopyButton({ value, title }: { value: string; title?: string }) {
  const [copied, setCopied] = useState(false);

  const onClick = async (e: React.MouseEvent) => {
    e.stopPropagation();   // don't trigger the row click underneath
    e.preventDefault();
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(value);
      } else {
        // Fallback for non-secure contexts
        const ta = document.createElement('textarea');
        ta.value = value;
        ta.style.position = 'fixed';
        ta.style.opacity = '0';
        document.body.appendChild(ta);
        ta.select();
        document.execCommand('copy');
        document.body.removeChild(ta);
      }
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      /* swallow — UI just won't flash */
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
