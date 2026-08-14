import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from 'react';
import { Modal } from './Modal';
import { Button } from './Button';

// ConfirmDialog + useConfirm (v0.9.1008, etkileşim denetimi M6 / KN-3).
//
// ÖNCESİ: `components/ui/` altında hiçbir onay atomu yoktu ve ev deseni
// tartışmasız `window.confirm` idi — 25 dosyada 27 çağrı. Yanında iki
// tekil desen daha yaşıyordu (AdminClickhouse'un elle yazdığı Modal,
// DangerZone'un satır-içi type-to-confirm'ü), yani aynı depoda ÜÇ ayrı
// onay dili vardı.
//
// `confirm()` K6'nın "kırmızı" yarısını YAPISAL OLARAK taşıyamaz:
//   - Tarayıcı diyaloğu dark/light/redhat token sisteminin tamamen
//     DIŞINDA; yıkıcı düğme vurgulanamıyor.
//   - Düğmeler OS dilinde. Uygulamada `LangToggle` varken diyalog
//     "OK/Cancel"i işletim sisteminden alıyordu.
//   - Silinecek şeyin adı düz metne sıkışıyor, biçimlendirilemiyor.
//   - `lib/escLayer`in LIFO katman modeli native diyaloğu GÖRMÜYOR:
//     açık bir çekmecenin üstünde Esc davranışı artık uygulamanın
//     değil tarayıcının. Bu atom Modal üzerine kurulu olduğu için
//     katman disiplinine geri giriyor.
//   - Orantı kurulamıyor: bir saved view silme ile bir alert rule
//     silme aynı ağırlıkta iki OK tuşuydu.
//
// NEDEN CONTEXT: diyalog bir `await` sonucu döndürmek zorunda, yani
// çağıranın render ağacından bağımsız TEK bir host'ta yaşamalı. Depoda
// `escLayer`/`navScope` gibi modül-seviyesi singleton'lar var ama
// onların React state'i yok; burada state şart (açık/kapalı + seçenekler).

export interface ConfirmOptions {
  /** Soru cümlesi. Ör. "Delete runbook?" */
  title: string;
  /**
   * ZORUNLU — ve bu bilinçli bir tip kararı.
   *
   * "Onay metni ne silineceğini söylüyor" şartı regex'e YAZILAMAZ:
   * `confirm(`Disable ${ids.length} rules?`)` bir interpolasyon taşır
   * ama adı değil SAYIYI söyler; statik bir kapı ikisini ayırt edemez.
   * `body`yi opsiyonel bırakmak, `title` tek başına yeterliymiş gibi
   * okunurdu ve 27 sitenin çoğu "Delete this X?" jenerikliğine geri
   * düşerdi. Tip burada regex'ten güçlü.
   */
  body: ReactNode;
  /**
   * Onay düğmesinin etiketi. Varsayılan "OK" YOK — eylemin kendisi
   * yazılır ("Delete runbook", "Revoke token"). Operatör düğmeye
   * bakınca ne olacağını okur, soruyu geri okumak zorunda kalmaz.
   */
  confirmLabel: string;
  cancelLabel?: string;
  /**
   * Yıkıcı mı? Onay düğmesi `danger`, değilse `primary`.
   * Şiddet kademesi burada; 27 sitenin hepsi yıkıcı DEĞİL.
   */
  danger?: boolean;
}

type Pending = { opts: ConfirmOptions; resolve: (ok: boolean) => void };

const Ctx = createContext<((opts: ConfirmOptions) => Promise<boolean>) | null>(null);

/**
 * `const confirm = useConfirm();` → `if (!await confirm({…})) return;`
 *
 * Çağrı biçimi bilerek `window.confirm`in erken-dönüş şekline benziyor:
 * 27 sitenin `if (!confirm('…')) return;` satırı tek satırlık bir
 * değişiklikle taşınıyor (handler `async` olmak zorunda).
 */
export function useConfirm(): (opts: ConfirmOptions) => Promise<boolean> {
  const fn = useContext(Ctx);
  if (!fn) {
    throw new Error('useConfirm: <ConfirmProvider> ağaçta yok');
  }
  return fn;
}

export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [pending, setPending] = useState<Pending | null>(null);
  // Tek uçuş kilidi: bir diyalog açıkken ikinci bir `confirm()` çağrısı
  // birincinin promise'ini ÖKSÜZ bırakırdı (hiç resolve edilmez → çağıran
  // handler sonsuza kadar askıda). Bu durumda ikinciyi reddetmek yerine
  // BİRİNCİYİ false ile kapatıyoruz: askıda kalan bir await, görünmeyen
  // bir hatadır.
  const pendingRef = useRef<Pending | null>(null);

  const settle = useCallback((ok: boolean) => {
    const p = pendingRef.current;
    pendingRef.current = null;
    setPending(null);
    // resolve YALNIZ BİR KEZ: Esc + backdrop + Cancel aynı anda
    // tetiklenebilir (Esc katmanı kapatırken onClose de koşar).
    p?.resolve(ok);
  }, []);

  const confirm = useCallback((opts: ConfirmOptions) => new Promise<boolean>(resolve => {
    if (pendingRef.current) pendingRef.current.resolve(false);
    const next = { opts, resolve };
    pendingRef.current = next;
    setPending(next);
  }), []);

  return (
    <Ctx.Provider value={confirm}>
      {children}
      {pending && (
        <Modal
          open
          onClose={() => settle(false)}
          title={pending.opts.title}
          size="sm"
          // Odak İPTALE gidiyor, onaya değil. Enter'a basmaya hazır bir
          // parmak yıkıcı yolu seçmemeli — `confirm()`ün OS düğmelerinde
          // kaybettiğimiz kontrol tam olarak buydu.
          initialFocus="[data-confirm-cancel]"
          footer={(
            <>
              <Button variant="secondary" data-confirm-cancel onClick={() => settle(false)}>
                {pending.opts.cancelLabel ?? 'Vazgeç'}
              </Button>
              <Button variant={pending.opts.danger ? 'danger' : 'primary'}
                onClick={() => settle(true)}>
                {pending.opts.confirmLabel}
              </Button>
            </>
          )}>
          <div style={{ fontSize: 'var(--fs-md)', lineHeight: 1.6 }}>{pending.opts.body}</div>
        </Modal>
      )}
    </Ctx.Provider>
  );
}
