import { useEffect, useState } from 'react';
import { api } from '@/lib/api';

// useCopilotEnabled — /api/copilot/config'in TEK, paylaşılan cevabı
// (v0.9.477). Önceden her CopilotExplain mount'u kendi isteğini atıyordu;
// anomali listesinde satır başına bir buton var → tek render'da N config
// isteği. Sonuç süreç ömrü boyunca sabit (operatör Settings'ten
// değiştirirse zaten sayfa yenilenir), o yüzden modül düzeyinde
// cache'leniyor: ilk çağrı bir kez uçar, sonrakiler SENKRON okur —
// çekmece açılırken "null render" titremesi de böylece kalkar.
let cached: boolean | null = null;
let inflight: Promise<boolean> | null = null;

function load(): Promise<boolean> {
  if (cached !== null) return Promise.resolve(cached);
  if (!inflight) {
    inflight = api.copilotConfig()
      .then(c => { cached = !!c.enabled; return cached; })
      .catch(() => { cached = false; return false; })
      .finally(() => { inflight = null; });
  }
  return inflight;
}

// `active=false` iken HİÇ istek atılmaz — kapalı çekmece (ve anonim
// /public/* sayfaları) boşuna config sormasın.
export function useCopilotEnabled(active = true): boolean | null {
  const [enabled, setEnabled] = useState<boolean | null>(() => cached);

  useEffect(() => {
    if (!active || enabled !== null) return;
    let alive = true;
    void load().then(v => { if (alive) setEnabled(v); });
    return () => { alive = false; };
  }, [active, enabled]);

  return active ? enabled : cached;
}

// Testler / hot-reload için: modül cache'ini sıfırla.
export function __resetCopilotEnabledCache() {
  cached = null;
  inflight = null;
}
