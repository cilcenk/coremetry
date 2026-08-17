package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/mcptools"
)

// copilot_pods.go — pod/JVM sağlığı bundle'ı (v0.9.376, operatör istegi —
// SRE perspektifi). Servisliyse pod envanteri (OTel host_name kimliği:
// up/CPU/bellek/son görülme) + o servisin JVM heap doluluğu; servissizse
// filo-geneli heap doluluk sıralaması. Restart/faz KSM ister — kanıt
// bunu açıkça söyler, Pods sekmesine yönlendirir. Veri tamamen CH'den;
// Thanos şartı yok.
//
// v0.9.1147 (AI Faz 3.4, D6) — copilot_guided.go'dan BURAYA taşındı ve
// okuma mcptools'un ortak katmanına indi (mcptools.ReadPodHealth), yani
// get_pod_health tool'u ile guided AYNI iki okumadan ve AYNI satır
// sırasından besleniyor. copilot_guided.go'da kopya KALMADI.
//
// İki sapma bilinçli ve ikisi de DÜRÜSTLÜK yönünde:
//
//  1. Servis modundaki heap listesi artık DOLULUK sırasında. Eski hâl
//     store sırasını basıyordu ve o sorgunun ORDER BY'ı YOK
//     (runtime_pods.go) — yani "en dolu pod hangisi" sorusunun cevabı
//     CH'nin keyfine bağlıydı. Filo dalı zaten sıralıyordu.
//  2. Kesilen heap listesi artık kaç pod'un düştüğünü söylüyor. Eski
//     döngü 13. eşleşmede sessizce çıkıyordu (envanter dalı ise "… ve N
//     pod daha" diyordu) — aynı blokta iki farklı dürüstlük seviyesi.

// guidedPodRows / guidedFleetHeapRows — kanıt bloğundaki satır tavanları
// (eski döngülerin 12 ve 10'u). Gösterim kararı, okuma kararı değil.
const (
	guidedPodRows       = 12
	guidedFleetHeapRows = 10
)

func (s *Server) guidedPodHealthBundle(ctx context.Context, emit func(string, any), service string, from, to time.Time) (string, string, error) {
	emitGuidedStep(emit, "jvm_heap_usage", "")
	heapRows := guidedPodRows
	if service == "" {
		heapRows = guidedFleetHeapRows
	} else {
		emitGuidedStep(emit, "service_instances", `{"service":`+jsonStr(service)+`}`)
	}
	// Pencere BÖLÜNMÜŞ ve bu ortak katmanda sabit: heap DAİMA canlı 10 dk
	// (chstore.RuntimePodWindow — sustained ortalama, v0.9.1053), envanter
	// ise sohbetin penceresi. Servis modunda heap okuması düşerse envanter
	// tek başına cevap sayılır (HeapUnavailable) — eski davranış.
	data, err := mcptools.ReadPodHealth(ctx, s.mcpDeps(), service, from, to, guidedPodRows, heapRows)
	if err != nil {
		return "", "", err
	}
	return renderPodHealthEvidenceTR(data)
}

// renderPodHealthEvidenceTR — SAF (veri → metin). İki mod tek yerde:
// Service boşsa filo heap sıralaması, doluysa envanter + o servisin heap'i.
func renderPodHealthEvidenceTR(data mcptools.PodHealthData) (string, string, error) {
	var b strings.Builder
	if data.Service == "" {
		if data.HeapTotal == 0 {
			b.WriteString("Filoda JVM heap metriği (jvm.memory.used/limit, OTel runtime) yayınlayan pod yok — JVM olmayan servislerde bu normaldir.\n")
		} else {
			fmt.Fprintf(&b, "Filo-geneli JVM heap doluluğu (son 10 dk ortalaması, %d pod), en dolu önce:\n", data.HeapTotal)
			for _, h := range data.Heap {
				fmt.Fprintf(&b, "- %s / %s: heap %%%.0f\n", h.Service, h.Pod, h.HeapPct)
			}
			if data.HeapTruncated {
				fmt.Fprintf(&b, "… ve %d pod daha (hepsi daha az dolu).\n", data.HeapTotal-len(data.Heap))
			}
		}
		b.WriteString(guidedPodRestartNoteTR)
		return b.String(), "filo JVM heap doluluğu (OTel runtime, canlı)", nil
	}

	fmt.Fprintf(&b, "%s pod/instance envanteri (pencere içinde metrik yayınlayanlar): %d pod, %d canlı (2dk tazelik).\n",
		data.Service, data.InstanceTotal, data.UpCount)
	// Sıra ortak katmanda: önce düşenler (SRE ilk onlara bakar), sonra CPU desc.
	for _, r := range data.Instances {
		state := "CANLI"
		if !r.Up {
			state = "SESSİZ (örnek yok — düşmüş ya da drene olmuş olabilir)"
		}
		fmt.Fprintf(&b, "- %s: %s, cpu %%%.0f, bellek %.0fMB\n", r.ID, state, r.CPUPct, r.MemBytes/1e6)
	}
	if data.InstancesTruncated {
		fmt.Fprintf(&b, "… ve %d pod daha.\n", data.InstanceTotal-len(data.Instances))
	}
	if data.InstanceTotal == 0 {
		b.WriteString("Bu pencerede metrik yayınlayan pod yok.\n")
	}
	if !data.HeapUnavailable {
		if len(data.Heap) == 0 {
			b.WriteString("Bu servis JVM heap metriği yayınlamıyor (JVM değilse normal).\n")
		} else {
			b.WriteString("JVM heap doluluğu (son 10 dk ortalaması):\n")
			for _, h := range data.Heap {
				fmt.Fprintf(&b, "- %s: heap %%%.0f (%.0fMB / %.0fMB -Xmx)\n",
					h.Pod, h.HeapPct, h.UsedBytes/1e6, h.LimitBytes/1e6)
			}
			if data.HeapTruncated {
				fmt.Fprintf(&b, "… ve %d pod daha.\n", data.HeapTotal-len(data.Heap))
			}
		}
	}
	b.WriteString(guidedPodRestartNoteTR)
	return b.String(), fmt.Sprintf("%s pod envanteri + JVM heap (OTel runtime, canlı)", data.Service), nil
}

// guidedPodRestartNoteTR — iki modun paylaştığı dürüstlük altbilgisi:
// restart sayısı ve pod fazı OTel runtime metriklerinde YOK (KSM/Thanos
// ister), model bunu bilmezse uydurur.
const guidedPodRestartNoteTR = "Not: restart sayıları ve pod fazı kube-state-metrics/Thanos ister — servis sayfasının Pods sekmesinde.\n"
