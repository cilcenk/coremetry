import { useState } from 'react';
import { IconLink, IconCheck } from './icons';
import { Button } from './ui/Button';

export interface ShareButtonProps {
  /** Idle label. Logs says "Copy link"; everywhere else "Share". */
  label?: string;
  /** Confirmation label shown for ~1.5s after a successful copy. */
  copiedLabel?: string;
  /** Hover tooltip — worth overriding where the shared slice needs
   *  explaining (Logs encodes filters in the querystring). */
  title?: string;
}

/**
 * Grafana-style share button — copies the current URL (with all encoded
 * page state) to the clipboard and flashes a confirmation.
 *
 * The ONE share button (v0.8.540). Explore, ProblemDetail and Logs each
 * carried their own copy of this; the other two never used `.share-btn`,
 * so recolouring that class would have painted one page of three. All
 * three copied `window.location.href` verbatim, so unifying them is
 * behaviour-preserving — only the label/title differ, and those are now
 * props. Renders `variant="accent"`: emphasised, but deliberately not
 * primary — on ProblemDetail this sits directly beside the solid-accent
 * `Resolve`, which must stay the loudest control in that bar.
 *
 * Every caller shares one URL: the address bar. Each page already keeps
 * its full state there (Explore's encoded query, ProblemDetail's
 * ?problem=/?exc= via problemLink.ts, Logs' filters — the same
 * mechanism SavedViewsBar persists), so `window.location.href` is
 * always the canonical shareable link. Open to every role incl. viewers
 * (v0.8.102), and NOT a public/unauth link — recipients still sign in.
 */
export function ShareButton({
  label = 'Share',
  copiedLabel = 'Link copied',
  title = 'Copy a shareable link to this view',
}: ShareButtonProps = {}) {
  const [copied, setCopied] = useState(false);
  const onClick = async () => {
    try {
      const url = window.location.href;
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(url);
      } else {
        // Non-secure-context fallback (mirrors CopyButton).
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
      setTimeout(() => setCopied(false), 1500);
    } catch {
      /* swallow */
    }
  };
  return (
    <Button
      variant="accent"
      size="sm"
      className={copied ? 'copied' : undefined}
      onClick={onClick}
      title={title}
      leftIcon={copied ? <IconCheck size={13} /> : <IconLink size={13} />}>
      {copied ? copiedLabel : label}
    </Button>
  );
}
