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
  try {
    const u = new URLSearchParams(window.location.search).get('chartsV2');
    if (u === '1') return true;
    if (u === '0') return false;
    return window.localStorage.getItem(CHARTS_V2_KEY) === '1';
  } catch {
    return false; // storage kapalı / SSR — güvenli taraf: eski motor
  }
}

export function setChartsV2(on: boolean): void {
  try {
    if (on) window.localStorage.setItem(CHARTS_V2_KEY, '1');
    else window.localStorage.removeItem(CHARTS_V2_KEY);
  } catch { /* storage kapalı — sessiz */ }
}
