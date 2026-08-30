import { useEffect, useState } from 'react';
import { api } from '@/lib/api';

// useCopilotEnabled — /api/copilot/config'in TEK, paylaşılan cevabı
// (v0.9.477). Önceden her CopilotExplain mount'u kendi isteğini atıyordu;
// anomali listesinde satır başına bir buton var → tek render'da N config
// isteği. Sonuç süreç ömrü boyunca sabit (operatör Settings'ten
// değiştirirse zaten sayfa yenilenir), o yüzden modül düzeyinde
// cache'leniyor: ilk çağrı bir kez uçar, sonrakiler SENKRON okur —
// çekmece açılırken "null render" titremesi de böylece kalkar.
//
// v0.9.1037 — cache artık TÜM cevabı tutuyor (yalnız `enabled` bayrağını
// değil): AI çekmecesinin model çipi aynı cevaptan besleniyor. İkinci bir
// istek YOK — bir uç, bir fetch, iki okuyucu.
export interface CopilotConfig {
  enabled: boolean;
  /** Yalnız Copilot aktifken gelir; kapalı kurulumda alan hiç yok. */
  model?: string;
  /** v0.10.183 — >1 profil varsa sohbet seçicisi için (sırsız: id/label/model) */
  profiles?: { id: string; label?: string; model?: string }[];
  defaultProfile?: string;
}

const OFF: CopilotConfig = { enabled: false };

let cached: CopilotConfig | null = null;
let inflight: Promise<CopilotConfig> | null = null;

function load(): Promise<CopilotConfig> {
  if (cached !== null) return Promise.resolve(cached);
  if (!inflight) {
    inflight = api.copilotConfig()
      .then(c => { cached = { enabled: !!c.enabled, model: c.model }; return cached; })
      .catch(() => { cached = OFF; return OFF; })
      .finally(() => { inflight = null; });
  }
  return inflight;
}

// `active=false` iken HİÇ istek atılmaz — kapalı çekmece (ve anonim
// /public/* sayfaları) boşuna config sormasın.
export function useCopilotConfig(active = true): CopilotConfig | null {
  const [cfg, setCfg] = useState<CopilotConfig | null>(() => cached);

  useEffect(() => {
    if (!active || cfg !== null) return;
    let alive = true;
    void load().then(v => { if (alive) setCfg(v); });
    return () => { alive = false; };
  }, [active, cfg]);

  return active ? cfg : cached;
}

// useCopilotEnabled — çağıranların ezici çoğunluğu yalnız bayrağı
// istiyor; sözleşme v0.9.477'deki gibi kalıyor (null = henüz bilinmiyor).
export function useCopilotEnabled(active = true): boolean | null {
  const cfg = useCopilotConfig(active);
  return cfg === null ? null : cfg.enabled;
}

// Testler / hot-reload için: modül cache'ini sıfırla.
export function __resetCopilotEnabledCache() {
  cached = null;
  inflight = null;
}
