import { useEffect, useState } from 'react';

// DetailsToc — v0.9.380, redesign D4 (mockup af7419e5). Details
// sekmesinin sağında 140px yapışkan mini-ToC rayı: dtl-cols grid'i
// söküldükten sonra tam genişliğe çıkan uzun sayfada bölüme tek tık.
// Scroll-spy IntersectionObserver'la — aktif bölüm accent çizgili.
// Salt sunum: veri/fetch yok, URL'e yazmaz (geçici görünüm durumu).

const SECTIONS: Array<{ id: string; label: string }> = [
  { id: 'dtl-props', label: 'Properties' },
  { id: 'dtl-perf', label: 'Performance' },
  { id: 'dtl-latency', label: 'Latency' },
  { id: 'dtl-db', label: 'Database' },
  { id: 'dtl-runtime', label: 'Runtime & rollouts' },
  { id: 'deploys', label: 'Rollouts' },
];

export function DetailsToc() {
  const [active, setActive] = useState<string>(SECTIONS[0].id);
  useEffect(() => {
    if (typeof IntersectionObserver === 'undefined') return;
    // En üstteki görünür bölüm kazanır; rootMargin üstten dar tutulur ki
    // başlık viewport'un üst yarısına girince aktifleşsin.
    const visible = new Map<string, number>();
    const io = new IntersectionObserver(entries => {
      for (const e of entries) {
        if (e.isIntersecting) visible.set(e.target.id, e.boundingClientRect.top);
        else visible.delete(e.target.id);
      }
      if (visible.size > 0) {
        const top = [...visible.entries()].sort((a, b) => a[1] - b[1])[0][0];
        setActive(top);
      }
    }, { rootMargin: '-10% 0px -55% 0px' });
    for (const s of SECTIONS) {
      const el = document.getElementById(s.id);
      if (el) io.observe(el);
    }
    return () => io.disconnect();
  }, []);
  return (
    <nav aria-label="Details bölümleri" style={{
      position: 'sticky', top: 70, alignSelf: 'flex-start',
      width: 140, flexShrink: 0, fontSize: 11.5, color: 'var(--text3)',
      borderLeft: '1px solid var(--border)', paddingLeft: 12,
    }} className="dtl-toc">
      {SECTIONS.map(s => (
        <div key={s.id}
          onClick={() => document.getElementById(s.id)?.scrollIntoView({ behavior: 'smooth', block: 'start' })}
          style={{
            padding: '4px 0', cursor: 'pointer',
            color: active === s.id ? 'var(--accent)' : undefined,
            borderLeft: active === s.id ? '2px solid var(--accent)' : '2px solid transparent',
            marginLeft: -14, paddingLeft: 12,
          }}>
          {s.label}
        </div>
      ))}
    </nav>
  );
}
