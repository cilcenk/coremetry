'use client';
import { useState } from 'react';

/**
 * Grafana-style share button — copies the current URL (with all encoded
 * page state) to the clipboard and flashes a confirmation.
 */
export function ShareButton() {
  const [copied, setCopied] = useState(false);
  const onClick = async () => {
    try {
      const url = window.location.href;
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(url);
      } else {
        const ta = document.createElement('textarea');
        ta.value = url;
        ta.style.position = 'fixed';
        ta.style.opacity = '0';
        document.body.appendChild(ta);
        ta.select();
        document.execCommand('copy');
        document.body.removeChild(ta);
      }
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      /* swallow */
    }
  };
  return (
    <button className={'share-btn' + (copied ? ' copied' : '')}
      onClick={onClick}
      title="Copy a shareable link to this view">
      {copied ? '✓ Link copied' : '🔗 Share'}
    </button>
  );
}
