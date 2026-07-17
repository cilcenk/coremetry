// promQuote — PromQL etiket değeri kaçışlama (v0.9.44, adversarial
// review). Gösterim-amaçlı sorgu şablonlarına ham interpolasyon,
// içinde " veya \ olan bir değerle (cluster adı operatör serbest
// metni; namespace/pod ?pod= deep-link'inden gelir) bozuk ya da
// matcher'ı sessizce değişmiş bir sorgu ürettiriyordu — operatör
// bunu Prometheus'a yapıştırır. Backend karşılığı:
// internal/thanos/promql.go escapeLabelValue.
export function promQuote(v: string): string {
  return v.replace(/\\/g, '\\\\').replace(/"/g, '\\"').replace(/\n/g, '\\n');
}
