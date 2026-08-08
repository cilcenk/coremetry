// panelViz — dashboard MARKININ hangi motora gideceğine karar veren saf
// çekirdek (v0.9.796).
//
// Dashboard beş mark tanır (PanelVizType). v2 motoru (CorePanel/@grafana/ui)
// bunların ÜÇÜNÜ taşır — line · bar · area — ve markları kendi adlarıyla
// bilir ('bar' değil 'bars'). Kalan ikisi eski SVG motorunda (DashboardViz)
// kalır:
//
//   • stacked-bar  — DÜRÜST SINIR: v2'de yığılmış ÇUBUK markı YOK.
//     CorePanel'in 'stacked'ı yığılmış ALAN'dır; stacked-bar'ı oraya
//     bağlamak paneli sessizce BAŞKA bir grafiğe çevirirdi (çubuk → alan).
//     Ayrı dilim adayı; bu dilimde eskide kalıyor.
//   • stacked-area — bu dilimin mockup onayı "bar/area" için alındı;
//     yığılmışlar kapsam dışı bırakıldı, ikisi birden eski görünümde
//     kalsın diye (yarısı yeni yarısı eski bir yığın ailesi bırakmamak).
//
// Karar SAF ve tek yerde: PanelRenderer'ın üç paneli (metric · spanmetric ·
// promql) aynı fonksiyonu çağırır, üç kopya dallanma değil — kopya sınıfının
// bedeli bu depoda ölçülü (v0.9.790'a kadar metric paneli mark'a hiç
// dallanmıyordu, çünkü dallanma üç yere elle yazılmıştı).

import type { PanelVizType } from '@/lib/types';

// v2 motorunun taşıyabildiği dashboard markları.
export type DashV2Viz = 'line' | 'bar' | 'area';

// CorePanel'in mark adları (viz union'ının dashboard'a düşen alt kümesi).
export type CoreViz = 'line' | 'bars' | 'area';

export function isV2Viz(viz: PanelVizType): viz is DashV2Viz {
  return viz === 'line' || viz === 'bar' || viz === 'area';
}

// toCoreViz — dashboard adı → CorePanel adı. Tek fark 'bar' → 'bars';
// eşlemeyi elle yazmak yerine buradan geçirmek, üçüncü bir mark
// eklendiğinde derleyicinin (exhaustive switch) uyarmasını sağlar.
export function toCoreViz(viz: DashV2Viz): CoreViz {
  switch (viz) {
    case 'bar':  return 'bars';
    case 'area': return 'area';
    case 'line': return 'line';
  }
}
