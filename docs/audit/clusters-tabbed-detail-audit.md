# Audit — /clusters detayını sekmeli yapıya çevirme (OpenShift konsolu tarzı)

**Tarih:** 2026-07-17 · **Durum:** ONAY BEKLİYOR — implementasyon yok
**Onaylı tasarım (A):** geri linki + ad + rozet; sekme şeridi
Overview | Nodes (N) | Namespaces (N) | Pods (N); varsayılan Overview
(4 özet kartı + network throughput grafiği).

## 1. Bekleyen işlerin GERÇEK durumu (bugün itibarıyla)

| İş | Durum |
|---|---|
| Sparkline→MultiLineChart yükseltmesi | ✅ **BİTTİ** (v0.9.3-9.5): ThanosTrendPanel yerleşim-bağımsız tasarlandı — sekme gövdesine taşıma SIFIR iş; trendSeries.ts saf dönüştürücüleri Overview network grafiğine de hizmet eder (ayrı grafik yolu AÇILMAZ). T4 (deploy marker) hâlâ pod↔servis korelasyon audit'inin onayına kapılı. |
| Node CPU/mem | ✅ BİTTİ (v0.8.582-584) — Nodes sekmesi hazır veriyle açılır. |
| **Node/pod NETWORK** | ❌ **HİÇ YOK** — S4 probe'ları (container_network_* / node_network_*) hiç koşulmadı (sende). ClusterSummary'de net alanı YOK (yalnız nodes/pods/cpuUsedCores/memUsedBytes — v0.8.586 skaler seti). Overview'un Net in/out kartları + throughput grafiği BUNA bağımlı → probe-kapılı dilimler. |
| Katlanabilir paneller (v0.9.6) | ⚠️ Sekmeler bu özelliği GEÇERSİZ KILAR (bölümler artık sekme — katlama anlamsız): secOpen/chevron kalkar, localStorage 'clusters-sections' anahtarı öksüz kalır (zararsız). İki saat önce çıktı ama operatörün daha yeni tasarımı üstün — bilinçli geri alma. Kazanılan fetch-gating deseni sekme-aktifliğine birebir taşınır. |
| ?q= süzgeci (v0.9.7) | ✅ KALIR — aktif sekmenin tablosunu süzer (davranış değişmez). |
| Legacy ?tab= | v0.8.584'te girip S2'de kalkmıştı; ?tab=nodes gelen eski link `?section=nodes`'a MAP edilir (tek satır migrasyon, sonra parametre silinir). |
| storageKey'ler | clusterpods/clusternamespaces/clusternodes — sort URL'de (s_<key>) + genişlik localStorage'da: sekme UNMOUNT edilse bile state yapısal olarak korunur (aşağıda §3 kararının dayanağı). |

## 2. Sekme yönlendirmesi — ?section= (URL kaynak-of-truth)

- `?section=overview|nodes|namespaces|pods`; yokluğu = overview.
  ?cluster/?namespace ile composable; replace:true.
- **Deep-link kuralı:** `?cluster=X&namespace=Y` gelip `section` YOKSA
  → **Pods sekmesi** açılır (namespace parametresi pod-listesi niyeti
  taşır — Servis→Cluster pivotu değişmeden filtrelenmiş pod'lara
  düşer; scroll da kalmadığı için pivot deneyimi düzelir).
- **Namespaces satır tıklaması:** ?namespace='i set eder VE
  ?section=pods yazar (tek setParams çağrısı, tek history girdisi) —
  onaylı tasarımın "direkt filtrelenmiş pod listesi" akışı. Satır
  sonundaki trend-drawer ikonu (v0.9.5) davranışını korur
  (stopPropagation; drawer ?ns= paramı sekmeden bağımsız).
- Rozet (reachable/unreachable/not in telemetry) başlık satırına
  taşınır — kart grid'indeki üçlü dilin aynısı (summary sorgusu
  detayda da koşar: skaler, ucuz; rozet + Overview kartları aynı
  veriden).

## 3. DOM / fetch dengesi — karar: aktif olmayan sekme UNMOUNT

- Görünmeyen sekmenin DOM'u KALDIRILIR (display:none değil):
  2000-satırlık pod tablosunun gizli DOM'u contentVisibility'ye
  rağmen bellek/ilk-render maliyeti taşır; unmount temiz.
  Güvenli çünkü: sort URL'de, genişlikler localStorage'da (§1) —
  sekmeye dönüşte useDataTable aynı state'le kurulur; React Query
  cache'i (60s staleTime) veri anını korur → geçişler anlık.
- **Fetch-on-active-tab:** v0.9.6'nın enabled-gating deseni sekme
  aktifliğine bağlanır: nodes sorgusu yalnız section=nodes'ta,
  namespaces yalnız namespaces'ta, pods yalnız pods'ta (Pods sayacı
  sekme etiketinde yalnız yüklendiyse görünür — sayaç için önden
  fetch YAPILMAZ; OpenShift konsolu da böyle davranır). Overview
  yalnız summary (zaten skaler) + (L3'te) network grafiği.

## 4. Overview sekmesi

- **4 kart:** CPU used cores + Memory (ClusterSummary'den — skaler
  sözleşme DOKUNULMAZ) + Net in + Net out. Network alanları backend'e
  L2'de gelene kadar kart SLOT'ları render edilmez (yanlış "0" veya
  "—" kalabalığı yerine iki kart — v0.8.587 "alan yokluğu yanlış
  sıfır okutmaz" kuralı).
- **Network throughput grafiği (L3):** in/out iki seri, MultiLineChart
  + trendSeries yardımcıları (thanosTrendToSeries'in iki-seri
  bileşimi — yeni grafik yolu yok). Veri: yeni
  `GET /api/clusters/network-trend?cluster=` (cluster toplamı,
  `sum(rate(container_network_{receive,transmit}_bytes_total[5m]))`
  range step=60 — probe hangi metrik ailesini onaylarsa). Pencere:
  **sayfa range'i** (Topbar) — ?tw= çipleri drawer-yerel kalır
  (Overview bir "sayfa görünümü", drawer değil; iki pencere
  mekanizması aynı yüzeyde kafa karıştırır). fetch-on-active-tab +
  serveCached 60s + cacheBucket.

## 5. Boş/hata durumları — sekme başına korunur

| Durum | Görünüm |
|---|---|
| Cluster tümden erişilemez (summary+aktif sekme sorgusu düşer) | Sekme şeridi kalır, gövdede mevcut "X is unreachable" Empty'si + Settings linki |
| node-exporter boş (tenancy) | Nodes sekmesinde mevcut runbook-işaretli mesaj |
| Namespace'te pod yok / q eşleşmedi | Pods sekmesinde mevcut çip-kaldır ipucu |
| Network probe'u inmemiş | Overview'da net kartları/grafiği HİÇ render edilmez (L2/L3 gelene dek iki kartlı Overview) |

## 6. Dilim/tag planı (onaya sunulan — bağımlılık sırasıyla)

| Dilim | İçerik | Bağımlılık | Tahmin |
|---|---|---|---|
| L1 | Sekmeli layout: ?section= + legacy ?tab map + ns-satırı→Pods geçişi + fetch-on-active + secOpen/katlama kaldırma + Overview (2 kart: CPU/Mem) | YOK — bugün çıkabilir | ~1.5 saat |
| L2 | Network backend: probe onaylı metrik ailesiyle summary net alanları + /api/clusters/network-trend + pod/node net kolon verisi + testler | **§7 PROBE (sende)** | ~1.5 saat |
| L3 | Overview: Net in/out kartları + throughput grafiği (MultiLineChart) + Pods/Nodes tablolarına net kolonları | L2 | ~1 saat |
| T4 | Deploy marker'lar (trend-upgrade audit'inden taşınan) | pod↔servis korelasyon audit ONAYI | ~30 dk |

## 7. Probe (L2 ön şartı — değişmedi, hâlâ sende)

```bash
probe 'count(container_network_receive_bytes_total)'
probe 'count(node_network_receive_bytes_total{device!="lo"})'
```

## 8. Kısıt teyitleri

- ?cluster/?namespace sözleşmesi ve Servis→Cluster deep-link'leri
  bozulmaz (§2 kuralıyla İYİLEŞİR: doğru sekmeye düşer).
- useDataTable sort/resize kalıcılığı unmount'a dayanıklı (§3).
- ClusterSummary skaler kalır; Overview grafiği ayrı uç,
  fetch-on-active-tab.
- ThanosTrendPanel/trendSeries tek grafik yolu olarak kalır.
