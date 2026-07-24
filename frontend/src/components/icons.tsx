// Inline SVG icon set. Tiny — each icon is a 16x16 stroke-based glyph
// using `currentColor`, so it inherits the surrounding text colour
// and weight without any per-icon styling. Replaces the colour
// emojis we used as quick stand-ins (📨 🔔 🤖 🔐 🗑 🔗 🔍 ⬇ etc.) —
// emoji rendering is OS-dependent and reads as "AI generated" copy
// in the wild, especially in enterprise contexts.
//
// All icons share one wrapper so size + stroke-width + class are
// consistent. `size` defaults to 14 to fit alongside our 13px UI
// text without dwarfing it; pass `size={16}` for headers or stand-
// alone buttons.

import type { CSSProperties } from 'react';

interface IconProps {
  size?: number;
  className?: string;
  style?: CSSProperties;
  strokeWidth?: number;
  // aria-hidden by default; set label only when the icon stands
  // alone without adjacent text (rare).
  label?: string;
}

function Svg({ size = 14, className, style, strokeWidth = 1.7, label, children }: IconProps & { children: React.ReactNode }) {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      width={size} height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={strokeWidth}
      strokeLinecap="round"
      strokeLinejoin="round"
      className={className}
      style={{ display: 'inline-block', verticalAlign: '-2px', flexShrink: 0, ...style }}
      aria-hidden={!label}
      role={label ? 'img' : undefined}
      aria-label={label}
    >
      {children}
    </svg>
  );
}

// ── Glyphs ──────────────────────────────────────────────────────────────────
// Drawn from the Lucide / Feather geometric vocabulary so they
// visually rhyme with each other. Coordinates manually authored to
// keep the file zero-dependency.

export const IconMail = (p: IconProps) => (
  <Svg {...p}>
    <rect x="3" y="5" width="18" height="14" rx="2" />
    <path d="M3 7l9 7 9-7" />
  </Svg>
);

export const IconBell = (p: IconProps) => (
  <Svg {...p}>
    <path d="M6 8a6 6 0 0 1 12 0c0 7 3 8 3 8H3s3-1 3-8" />
    <path d="M10 21a2 2 0 0 0 4 0" />
  </Svg>
);

export const IconSparkles = (p: IconProps) => (
  <Svg {...p}>
    <path d="M12 3v3m0 12v3M3 12h3m12 0h3M5.6 5.6l2.1 2.1m8.6 8.6l2.1 2.1M5.6 18.4l2.1-2.1m8.6-8.6l2.1-2.1" />
  </Svg>
);

export const IconLock = (p: IconProps) => (
  <Svg {...p}>
    <rect x="4" y="11" width="16" height="10" rx="2" />
    <path d="M8 11V7a4 4 0 0 1 8 0v4" />
  </Svg>
);

export const IconTrash = (p: IconProps) => (
  <Svg {...p}>
    <path d="M3 6h18" />
    <path d="M8 6V4a1 1 0 0 1 1-1h6a1 1 0 0 1 1 1v2" />
    <path d="M5 6l1 14a2 2 0 0 0 2 2h8a2 2 0 0 0 2-2l1-14" />
  </Svg>
);

export const IconLink = (p: IconProps) => (
  <Svg {...p}>
    <path d="M10 13a4 4 0 0 0 5.7 0l3-3a4 4 0 1 0-5.7-5.7l-1.3 1.3" />
    <path d="M14 11a4 4 0 0 0-5.7 0l-3 3a4 4 0 1 0 5.7 5.7l1.3-1.3" />
  </Svg>
);

export const IconSearch = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="11" cy="11" r="7" />
    <path d="m20 20-3.5-3.5" />
  </Svg>
);

export const IconDownload = (p: IconProps) => (
  <Svg {...p}>
    <path d="M12 3v12" />
    <path d="m7 10 5 5 5-5" />
    <path d="M5 21h14" />
  </Svg>
);

export const IconCheck = (p: IconProps) => (
  <Svg {...p}>
    <path d="m5 12 5 5L20 7" />
  </Svg>
);

export const IconClose = (p: IconProps) => (
  <Svg {...p}>
    <path d="M6 6l12 12M18 6 6 18" />
  </Svg>
);

export const IconAlert = (p: IconProps) => (
  <Svg {...p}>
    <path d="M12 3 2 21h20L12 3z" />
    <path d="M12 10v5" />
    <circle cx="12" cy="18" r="0.6" fill="currentColor" stroke="none" />
  </Svg>
);

export const IconList = (p: IconProps) => (
  <Svg {...p}>
    <path d="M4 6h16M4 12h16M4 18h16" />
  </Svg>
);

export const IconDashboard = (p: IconProps) => (
  <Svg {...p}>
    <rect x="3" y="3"  width="8" height="8" rx="1" />
    <rect x="13" y="3" width="8" height="5" rx="1" />
    <rect x="13" y="10" width="8" height="11" rx="1" />
    <rect x="3" y="13" width="8" height="8" rx="1" />
  </Svg>
);

export const IconCircle = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="12" cy="12" r="9" />
  </Svg>
);

// Spinner-friendly database cylinder for "no data" / monitor empty
// states.
export const IconDatabase = (p: IconProps) => (
  <Svg {...p}>
    <ellipse cx="12" cy="5" rx="8" ry="2.5" />
    <path d="M4 5v6c0 1.5 4 3 8 3s8-1.5 8-3V5" />
    <path d="M4 11v6c0 1.5 4 3 8 3s8-1.5 8-3v-6" />
  </Svg>
);

export const IconClock = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="12" cy="12" r="9" />
    <path d="M12 7v5l3 2" />
  </Svg>
);

// IconZoomOut — magnifier-minus, Grafana's "widen the time range"
// affordance next to the range picker (Lucide zoom-out geometry).
export const IconZoomOut = (p: IconProps) => (
  <Svg {...p}>
    <circle cx="11" cy="11" r="7" />
    <path d="M21 21l-4.35-4.35" />
    <path d="M8 11h6" />
  </Svg>
);

// IconFlame — profiling / hot-path indicator. Replaces the
// 🔥 emoji used as a quick stand-in on Profile / SpanDetail.
export const IconFlame = (p: IconProps) => (
  <Svg {...p}>
    <path d="M12 3c-1 4 1 6 2 7 1.5 1.5 2.5 3 2.5 5a4.5 4.5 0 0 1-9 0c0-1.5.5-2.5 1.5-3.5C9 12.5 12 10 12 3z" />
    <path d="M12 13c.5 1.5 1.5 2.5 1.5 4a1.5 1.5 0 0 1-3 0c0-1 .5-1.5 1-2 .3-.3.5-.7.5-1.5z" />
  </Svg>
);

// IconShield — security / admin gate. Replaces the 🛑 / 🔒
// emojis used as access-denied glyphs.
export const IconShield = (p: IconProps) => (
  <Svg {...p}>
    <path d="M12 3l8 3v6c0 4.5-3.5 8-8 9-4.5-1-8-4.5-8-9V6l8-3z" />
  </Svg>
);
