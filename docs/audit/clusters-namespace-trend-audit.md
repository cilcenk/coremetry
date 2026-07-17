# Audit — /clusters detayında namespace (ve koşullu deployment) trend grafiği

**Tarih:** 2026-07-17 · **Durum:** ONAY BEKLİYOR — implementasyon yok
**Hedef:** namespace satırından açılan, pod drawer'ıyla aynı dilde
trend drawer'ı; Grafana/OpenShift embedded grafik hissi. Genel
görünüm kartlarına DOKUNULMAZ (skaler kalır — v0.8.587 kararı).

## 1. Mevcut durum — teyit

| İddia | Durum |
|---|---|
| NS satırı onClick yalnız ?namespace= filtresi | ✅ Clusters.tsx:362+ — drawer yok |
| PodTrend + singlePod sorguları şablon | ✅ client.go PodTrend (range, step=60) + singlePodCPUQuery/MemQuery — namespace-scoped'a birebir uyarlanabilir (pod pini kalkar) |
| **BAĞIMLILIK: pod-drawer grafik iyileştirmesi (Sparkline onClick + adaptif pencere)** | ⚠️ **REPOYA İNMEMİŞ**: docs/audit'te dokümanı yok, kodda izi yok (PodDrawer düz DrawerTrendRow; Drawer.tsx'te chartRange yok). `chartRange` deseni yalnız AnomalyDetailDrawer.tsx:69'da. O audit-prompt muhtemelen başka oturumda yazıldı ve buraya ulaşmadı → talimat gereği SIRALI plan: önce paylaşılan drawer-grafik iyileştirmesi (Dilim A), sonra NamespaceDrawer aynı bileşeni kullanır (Dilim B). İki ayrı trend implementasyonu OLUŞMAZ — yapısal garanti: ikisi de aynı DrawerTrendRow/expanded-chart bileşenini tüketir. |
| Deployment kavramı repoda yok | ✅ kube_pod_owner / k8s.deployment.name / replicaset — internal/thanos + chstore'da sıfır iz. Probe'a tabi (§4). |
| Kartlar skaler-only | ✅ dokunulmuyor |

Not: prod'daki "pods/namespaces boş" konusu hâlâ açık — bu iş veri
aktığı varsayımıyla planlandı; teşhis (namespace filter regex şüphesi)
ayrıca sürüyor ve bu audit'i bloklamıyor (kod yolu aynı).

## 2. Dilim A — paylaşılan drawer-grafik iyileştirmesi (önce bu)

Kayıp bağımlılığın bu repo için yorumlanmış kapsamı (onayına tabi):

- **Adaptif pencere:** drawer'a kendi mini pencere çipleri (15m /
  1h / 6h) — AnomalyDetailDrawer'ın chartRange deseninin drawer'a
  uyarlanması: sayfa range'inden BAĞIMSIZ, drawer-yerel useMemo
  penceresi; pencere değişimi yalnız o drawer'ın trend sorgusunu
  yeniler. Değerler sınırlı rung'lar → cache-key kardinalitesi
  bounded (ES-cost/v0.8.270 disiplini; sunucu zaten ≤6h clamp'li).
- **Tıkla-büyüt:** DrawerTrendRow'un Sparkline'ına opsiyonel
  onClick — tıklayınca satırın altında büyük uPlot grafiği
  (MultiLineChart, evin tek grafik kütüphanesi) açılır/kapanır;
  eksen + hover değeri okunur (Grafana hissinin asıl kaynağı).
  DrawerTrendRow'a `expandable` prop'u — mevcut çağıranlar (Hosts
  drawer'ı dahil) davranış değişmeden kalır (prop'suz = bugünkü).
- Dokunulan: components/ui/Drawer.tsx (DrawerTrendRow),
  Clusters.tsx PodDrawer (çipler + expandable). Hosts drawer'ına
  bilerek DOKUNULMAZ (ayrı yüzey, istenirse tek satır follow-up).

## 3. Dilim B — NamespaceTrend + NamespaceDrawer

- **Backend:** `NamespaceTrend(ctx, c, namespace, from, to)` —
  PodTrend'in birebir aynası; sorgular pod pinsiz:
  `sum(rate(container_cpu_usage_seconds_total{container!="",pod!="",namespace="X"}[5m]))`
  + working_set karşılığı, query_range step=60. Uç:
  `GET /api/clusters/namespaces/detail?cluster=&namespace=&from=&to=`
  — pods/detail'in aynası (serveCached 60s, cacheBucket, ≤6h clamp,
  10s deadline). Testler mevcut fakeQuerier'la.
- **Frontend:** `NamespaceDrawer` — PodDrawer'ın aynası (başlıkta
  namespace+cluster rozetleri, Dilim A'nın çipli/büyütülebilir
  trend bileşeni). Drawer kimliği `?ns=<cluster>|<namespace>`
  (?pod= simetriği; ?namespace= FİLTRESİNDEN ayrı param — ikisi
  bağımsız yaşar).
- **Affordance — filtre davranışı BOZULMAZ:** satır tıklaması bugünkü
  gibi ?namespace= filtresini toggle'lar; satır SONUNA sabit-genişlik
  ikon hücresi (ChartSpline, `stopPropagation`) → drawer'ı açar.
  NS_COLS'a 40px sortValue'suz kolon; başlık boş.
- Fetch-on-open: rollup tablosu için prefetch YOK — sorgu yalnız
  drawer açılınca (PodDrawer sözleşmesinin aynısı).

## 4. Deployment seviyesi — PROBE'A KOŞULLU (kapsam kararı probe'da)

Repoda kavram yok; tenancy'de varlığı doğrulanmadan plana giremez:

```bash
probe 'count(kube_pod_owner{owner_kind="ReplicaSet"})'
probe 'count(kube_replicaset_owner{owner_kind="Deployment"})'
```

- **İkisi de ✓** → follow-up dilim (C): namespace drawer'ına
  "Deployments" rollup bölümü — iki adımlı owner join'i
  (pod→ReplicaSet→Deployment, `label_replace` ile rs-hash kırpma
  YERİNE kube_replicaset_owner join'i; tek sorguda
  `sum by (owner_name)` + rs→deploy eşlemesi Go'da). Ayrı audit-ek
  + kendi tag'i — bu iterasyona ALINMADI, plan iskeleti hazır.
- **Herhangi biri ✗** → deployment seviyesi ŞİMDİLİK YOK; runbook'a
  "kube-state-metrics owner serileri tenancy'de kapalı" notu düşülür,
  namespace→pod iki seviyesiyle yaşanır.

## 5. Dilim/tag planı (onaya sunulan)

| Dilim | İçerik | Tahmin |
|---|---|---|
| A | Paylaşılan drawer-grafik iyileştirmesi (pencere çipleri + tıkla-büyüt uPlot; PodDrawer'a uygulanır) | ~1 saat |
| B1 | NamespaceTrend + /api/clusters/namespaces/detail + testler | ~45 dk |
| B2 | NamespaceDrawer + ikon affordance (?ns= param) | ~45 dk |
| C | (probe ✓ ise, ayrı onayla) Deployments rollup bölümü | ~1 saat |

A → B sıralı (aynı bileşen); C probe + ayrı onay bekler.

## 6. Kısıt teyitleri

- Kartlar (ClusterSummary) grafiksiz/skaler kalır.
- ?namespace= filtre davranışı birebir korunur (drawer ayrı param).
- İki trend implementasyonu oluşmaz — tek paylaşılan bileşen.
- Fetch-on-open her drawer'da; rollup prefetch'i yok.
