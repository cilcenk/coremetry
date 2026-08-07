// featureFlags — sayfa-bazlı migrasyon bayrakları (FAZ 3, v0.9.708).
//
// Spec: "Migrasyon aşamalı: sayfa bazında feature flag, tek adımda eski
// sarmalayıcıya dönülebilir." Bayrak GÖRÜNÜM tercihi değil MOTOR
// seçimi — paylaşılabilir view-state olmadığı için URL'e YAZILMAZ
// (ev kuralının kapsamı dışında); localStorage'da yaşar, URL parametresi
// yalnız test için OKUNUR (?chartsV2=1/0, kalıcılaşmaz).
//
// Geri dönüş tek adım: localStorage kaydını sil ya da ?chartsV2=0 ile
// doğrula — eski ChartCard yolu aynen yerinde duruyor.

const CHARTS_V2_KEY = 'cm.flags.chartsV2';

export function chartsV2(): boolean {
  // v0.9.743 (operatör, prod görseliyle onay): v2 artık VARSAYILAN —
  // "chartsV2=1 yapmaya gerek yok". Kaçış kapıları duruyor:
  // ?chartsV2=0 (tek görünüm) ya da localStorage '0' (kalıcı) eski
  // motora döner; eski ChartCard yolu aynen yerinde (tek adım geri
  // dönüş doktrini).
  try {
    const u = new URLSearchParams(window.location.search).get('chartsV2');
    if (u === '1') return true;
    if (u === '0') return false;
    return window.localStorage.getItem(CHARTS_V2_KEY) !== '0';
  } catch {
    return true; // storage kapalı / SSR — varsayılan yeni motor
  }
}

export function setChartsV2(on: boolean): void {
  try {
    // Varsayılan AÇIK olduğu için kalıcı tercih '0' işaretiyle tutulur;
    // açmak = işareti silmek.
    if (on) window.localStorage.removeItem(CHARTS_V2_KEY);
    else window.localStorage.setItem(CHARTS_V2_KEY, '0');
  } catch { /* storage kapalı — sessiz */ }
}
