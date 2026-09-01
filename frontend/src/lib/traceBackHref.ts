// traceBackHref — v0.10.219 (Dynatrace düzeni D4): /trace breadcrumb'ının
// "Traces" halkası nereye döner?
//
// /traces satırı (Traces.tsx renderRow) Link `state={{ from }}` ile
// listenin O ANKİ URL'sini (filtre + sayfa + ?range=) taşır; breadcrumb onu
// aynen geri açar — operatör aradığı listeyi kaybetmez. "← Back"
// (navigate(-1)) bunu tarayıcı geçmişiyle yapıyordu ama yeni sekmede /
// paylaşılan linkte geçmiş YOK: orada geri gidecek yer yoktu. Bu yardımcı
// her iki durumda da bir hedef verir.
//
// GÜVENLİK KAPISI: state istemciden gelir (history.state, herhangi bir
// sayfa yazabilir). Yalnız uygulama-içi /traces yolu kabul edilir;
// protokol-göreli (`//evil`), mutlak ya da başka bir rota → varsayılan.
export const TRACES_LIST_HREF = '/traces';

export function traceBackHref(state: unknown): string {
  if (!state || typeof state !== 'object') return TRACES_LIST_HREF;
  const from = (state as { from?: unknown }).from;
  if (typeof from !== 'string') return TRACES_LIST_HREF;
  // `/traces`, `/traces?x=y`, `/traces/…` — ama `/tracesfoo` ya da `//…` değil.
  if (from === TRACES_LIST_HREF || from.startsWith(TRACES_LIST_HREF + '?') || from.startsWith(TRACES_LIST_HREF + '/')) {
    return from;
  }
  return TRACES_LIST_HREF;
}
