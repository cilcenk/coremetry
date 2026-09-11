import { Button } from '@/components/ui';
import { Empty } from '@/components/Spinner';

// SessionEndedCard — v0.10.673 (trace kiosk modu Dilim 3; audit §6 + §11
// soru 7 → overlay). Kiosk penceresinde (yalnız /trace?kiosk=1) bir API
// çağrısı 401 verince AuthProvider /login'e ATLAMAZ; kabuk donmuş görünümün
// üstüne bu overlay'i çizer — operatör bağlamı (şelale + loglar) görmeye
// devam eder. "Yeniden giriş yap" aynı pencerede /login'e gider; dönüş
// kayıtlı derin bağlantıyla (v0.8.367, sessionStorage). Atomlar: Empty +
// Button; elle buton yok.
export function SessionEndedCard({ onRelogin }: { onRelogin: () => void }) {
  return (
    <div className="session-ended" role="alertdialog" aria-modal="true" aria-label="Oturum sonlandı">
      <div className="session-ended-card">
        <Empty icon="🔒" title="Oturum sonlandı" compact
          action={<Button variant="primary" onClick={onRelogin}>Yeniden giriş yap</Button>}>
          Görünüm dondu. Yeniden giriş yapınca aynı trace'e dönersin.
        </Empty>
      </div>
    </div>
  );
}
