import { CopilotChat } from '@/components/CopilotChat';

// AIDrawer — v0.10.483: ARTIK AYRI BİR ÇEKMECE DEĞİL. `?ai=<kind>:<id>`
// öznesini CoSRE çekmecesi (CopilotChat) kendisi okur ve aynı kabuğun
// içinde açıklama kipine geçer (AIDrawerBody). Bu bileşen yalnız eski
// import yerleri (testler) için CopilotChat'e delege eder; AppShell
// yalnız CopilotChat'i mount eder — ikisi birden mount edilirse iki
// çekmece açılır (drawerParity.test pinler).
export function AIDrawer() {
  return <CopilotChat />;
}
