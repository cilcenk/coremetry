// "Kodu da incele" tercihinin kalıcılığı (v0.9.1238).
//
// Kutu, cevabın stack trace'teki uygulama satırlarının KAYNAK KODUNA
// bakıp bakmayacağını belirler. Durumu mount'a bağlıydı ve AI çekmecesi
// her özne için yeni bir mount kuruyor (AIDrawer `key`), yani tercih her
// çekmece açılışında unutuluyordu. auto modda bunun bedeli iki katlı:
// ilk tur HER ZAMAN kodsuz gidiyor, operatör kutuyu yeniden işaretleyince
// aynı soru için İKİNCİ bir yerel LLM turu (+ ikinci `ai_calls` satırı)
// başlıyordu.
//
// ── Hatırlamak ≠ varsayılanı açmak ───────────────────────────────────
// Anahtar YOKKEN değer false döner. v0.9.831 varsayılanı bilerek KAPALI
// bıraktı (kod okumak bir depo listelemesi + dosya çekmesi demek, her
// Explain tıkında ödenecek bir maliyet değil); burada hatırlanan şey
// yalnızca operatörün KENDİ açık seçimi.
//
// ── Neden `codeCapable` kapısı okuma tarafında ───────────────────────
// Tercih tek anahtarda yaşıyor ama kod yalnız exception/trace için
// çekiliyor. Kapı olmasaydı, tercihi açık bir operatörün /problems
// açıklaması "CoSRE kodu okuyor…" derdi — hiçbir kod okunmazken. Yalan
// söyleyen bir spinner, unutulan bir kutudan daha kötüdür.
import { getRaw, setRaw, STORAGE_KEYS } from './storage';

/** Kayıtlı tercihi oku. `codeCapable` false ise (kod okunamayan tür)
 *  tercih ne olursa olsun KAPALI döner. */
export function readIncludeCodePref(codeCapable: boolean): boolean {
  if (!codeCapable) return false;
  return getRaw(STORAGE_KEYS.aiIncludeCode) === '1';
}

/** Operatörün açık seçimini yaz. Kapatmak da bir seçimdir ve '0' olarak
 *  KAYDEDİLİR — anahtarı silmek "hiç seçmedi" ile aynı şey olurdu ve
 *  ileride varsayılan değişirse sessizce geri açılırdı. */
export function writeIncludeCodePref(on: boolean): void {
  setRaw(STORAGE_KEYS.aiIncludeCode, on ? '1' : '0');
}
