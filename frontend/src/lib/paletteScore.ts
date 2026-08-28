// paletteScore — ⌘K skorlayıcısının saf çekirdeği (v0.9.1270,
// Dynatrace-parite B5#1). İki iş: (1) etiket skoru (eski davranış
// bire bir: tam 1000 > önek 500 > içerir 200 > sıralı-harf 50);
// (2) ALIAS skoru — sayfa yeniden adlandırılınca (nav.inbox →
// "Problems") eski adı yazan operatör sayfayı KAYBETMEZ: alias'lar
// etiketin hemen altında puanlanır (900/450/180; tam-etiket her
// zaman alias'ı yener). Alias'ta fuzzy YOK — kısa takma adlarda
// sıralı-harf eşleşmesi gürültü üretir.
export function scorePaletteEntry(q: string, label: string, aliases?: string[]): number {
  const lbl = label.toLowerCase();
  let score = 0;
  if (lbl === q) score = 1000;
  else if (lbl.startsWith(q)) score = 500;
  else if (lbl.includes(q)) score = 200;
  else {
    let qi = 0;
    for (let i = 0; i < lbl.length && qi < q.length; i++) {
      if (lbl[i] === q[qi]) qi++;
    }
    if (qi === q.length) score = 50;
  }
  for (const a of aliases ?? []) {
    const al = a.toLowerCase();
    let s = 0;
    if (al === q) s = 900;
    else if (al.startsWith(q)) s = 450;
    else if (al.includes(q)) s = 180;
    if (s > score) score = s;
  }
  return score;
}

// rankPaletteResults — ⌘K sonuç SIRASI (v0.10.126, operatör: "endpoint
// veya servisler en altta çıkıyor, en üstte çıkmalarını isterim").
//
// Eski akış: sayfalar + servisler skorlanıp sıralanıyor, endpoint'ler
// skorsuz EN SONA ekleniyordu; fuzzy (50) ile tutunan altı sayfa,
// operatörün aradığı endpoint'in üstüne biniyordu. Yeni sıra: servis →
// endpoint → sayfa (kendi içlerinde skora göre). Aksiyon ve trace-id
// kısayolu çağıranda hâlâ en üstte. Sayfalar rotaya göre tekilleşir —
// katalogda aynı rotanın navKey'li ve düz girişi birlikte duruyor ve
// ikisi de eşleşince "Problems" dört kez listeleniyordu.
export interface PaletteRankable {
  kind: string;
  to?: string;
  score?: number;
}
export function rankPaletteResults<T extends PaletteRankable>(scored: T[], endpoints: T[], cap = 50): T[] {
  const byScore = (a: T, b: T) => (b.score ?? 0) - (a.score ?? 0);
  const services = scored.filter(r => r.kind === 'service').sort(byScore);
  const seen = new Set<string>();
  const pages: T[] = [];
  for (const r of [...scored.filter(r => r.kind === 'page')].sort(byScore)) {
    const key = r.to ?? '';
    if (key && seen.has(key)) continue;
    if (key) seen.add(key);
    pages.push(r);
  }
  const other = scored.filter(r => r.kind !== 'service' && r.kind !== 'page');
  return [...services, ...endpoints, ...other, ...pages].slice(0, cap);
}
