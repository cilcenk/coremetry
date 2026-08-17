package api

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/mcptools"
)

// guidedShiftProblemRows — vardiya bloğundaki problem satırı tavanı.
// Gösterim kararı: sayaçlar (açıldı/çözüldü/hâlâ açık) her zaman TÜM
// pencereyi anlatır, kesilen yalnız satır listesidir.
const guidedShiftProblemRows = 12

// renderShiftProblemsTR — SAF: pencere olayları → vardiya özetinin
// PROBLEMLER bloğu. Metin bayt-bayt v0.9.416'nın hâli; tek ekleme, okuma
// 500 satır tavanına dayandığında düşen alt-sınır uyarısı (eski hâl
// sayaçları kesin gibi gösteriyordu).
func renderShiftProblemsTR(data mcptools.ProblemWindowData, to time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\nPROBLEMLER: %d açıldı, %d çözüldü, %d hâlâ açık.\n",
		data.Opened, data.Resolved, data.StillOpen)
	if data.StoreCapped {
		fmt.Fprintf(&b, "(okuma %d satır tavanına dayandı — sayılar ALT SINIR)\n", data.StoreRowLimit)
	}
	if len(data.Rows) > 0 {
		if data.Truncated {
			fmt.Fprintf(&b, "(en yeni %d satır gösteriliyor, toplam %d)\n", len(data.Rows), data.Total)
		}
		for _, p := range data.Rows {
			state := p.Status
			if p.ResolvedAtNs > 0 {
				state = fmt.Sprintf("çözüldü %s önce", fmtAgoTR((to.UnixNano()-p.ResolvedAtNs)/1e9))
			}
			fmt.Fprintf(&b, "- [%s/%s] %s · %s · %s (açılış: %s önce)\n",
				p.Priority, p.Severity, p.Service, p.RuleName, state,
				fmtAgoTR((to.UnixNano()-p.StartedAtNs)/1e9))
		}
	}
	return b.String()
}

// copilot_shift.go — vardiya özeti bundle'ı (v0.9.416, CoSRE fikir #2).
// "Dün gece neler oldu?" tek cevapta: pencere içinde açılan/çözülen
// problemler + anomali olayları + deploy'lar + yeni P1-aday exception
// grupları. Tüm okumalar bounded state/MV okumaları — spans taraması YOK.
// Servisli soru ("checkout'ta dün gece ne oldu") tüm blokları o servise
// daraltır.
func (s *Server) guidedShiftSummaryBundle(ctx context.Context, emit func(string, any), service string, from, to time.Time, rangeS int64) (string, string, error) {
	var b strings.Builder
	scope := "filo geneli"
	if service != "" {
		scope = service + " servisi"
	}
	fmt.Fprintf(&b, "Vardiya penceresi: son %s (%s → %s UTC), kapsam: %s.\n",
		fmtAgoTR(rangeS), from.UTC().Format("15:04"), to.UTC().Format("15:04"), scope)

	// ── Problemler: pencerede açılan + çözülen (v0.9.394 pencere sorgusu) ─
	//
	// v0.9.1147 (AI Faz 3.4) — okuma + zenginleştirme + sınıflama ortak
	// katmanda (mcptools.ReadProblemWindowEvents); list_problem_window_events
	// tool'u AYNI şekli döndürüyor. Zincir orada sabit: pencere okuması →
	// deploy ÖNCE, öncelik SONRA (v0.9.553) → yapısal veri.
	emitGuidedStep(emit, "problem_window_events", "")
	pw, perr := mcptools.ReadProblemWindowEvents(ctx, s.mcpDeps(), service, from, to, guidedShiftProblemRows)
	if perr != nil {
		return "", "", perr
	}
	b.WriteString(renderShiftProblemsTR(pw, to))

	// ── Anomali olayları (v0.9.394 FromNs/ToNs penceresi) ────────────────
	emitGuidedStep(emit, "anomaly_window_events", "")
	var svcFilter []string
	if service != "" {
		svcFilter = []string{service}
	}
	evs, aerr := s.store.ListAnomalyEvents(ctx, chstore.ListAnomalyEventsFilter{
		FromNs: from.UnixNano(), ToNs: to.UnixNano(), Limit: 50, Services: svcFilter,
	})
	if aerr == nil {
		if len(evs) == 0 {
			b.WriteString("\nANOMALİLER: pencerede anomali olayı yok.\n")
		} else {
			sort.SliceStable(evs, func(i, j int) bool { return evs[i].PeakRatio > evs[j].PeakRatio })
			fmt.Fprintf(&b, "\nANOMALİLER: %d olay (tepe orana göre ilk 8):\n", len(evs))
			for i, e := range evs {
				if i >= 8 {
					break
				}
				fmt.Fprintf(&b, "- %s · %s · tepe %.1fx baseline · %s\n", e.Service, e.Pattern, e.PeakRatio, e.Status)
			}
		}
	}

	// ── Deploy'lar ───────────────────────────────────────────────────────
	emitGuidedStep(emit, "recent_deploys", "")
	if deps, derr := s.store.GetRecentDeploys(ctx, time.Since(from), 100); derr == nil {
		inWin := make([]chstore.RecentDeployEntry, 0, 10)
		for _, d := range deps {
			if d.FirstSeenNs >= from.UnixNano() && d.FirstSeenNs <= to.UnixNano() &&
				(service == "" || d.Service == service) {
				inWin = append(inWin, d)
			}
		}
		if len(inWin) == 0 {
			b.WriteString("\nDEPLOY'LAR: pencerede deploy yok.\n")
		} else {
			if len(inWin) > 10 {
				inWin = inWin[:10]
			}
			fmt.Fprintf(&b, "\nDEPLOY'LAR (%d):\n", len(inWin))
			for _, d := range inWin {
				fmt.Fprintf(&b, "- %s → %s (%s önce)\n", d.Service, d.Version,
					fmtAgoTR((to.UnixNano()-d.FirstSeenNs)/1e9))
			}
		}
	}

	// ── Pencerede DOĞAN exception grupları ───────────────────────────────
	// ListExceptionGroups'ta first_seen penceresi yok — state=new + yoğunluk
	// ön-süzgeciyle bounded liste çekilir, pencere Go'da uygulanır ve kesme
	// İFŞA edilir.
	emitGuidedStep(emit, "new_exception_groups", "")
	if groups, gerr := s.store.ListExceptionGroups(ctx, chstore.ExceptionGroupFilter{
		State: chstore.ExStateNew, Limit: 100, MinOccurrences: 50,
	}); gerr == nil {
		born := make([]chstore.ExceptionGroup, 0, 8)
		for _, g := range groups {
			if g.FirstSeen >= from.UnixNano() && (service == "" || g.Service == service) {
				born = append(born, g)
			}
		}
		if len(born) == 0 {
			b.WriteString("\nYENİ EXCEPTION GRUPLARI: pencerede (≥50 occurrence) yeni grup yok.\n")
		} else {
			sort.SliceStable(born, func(i, j int) bool { return born[i].Occurrences > born[j].Occurrences })
			if len(born) > 8 {
				fmt.Fprintf(&b, "\nYENİ EXCEPTION GRUPLARI (%d, en yoğun 8):\n", len(born))
				born = born[:8]
			} else {
				fmt.Fprintf(&b, "\nYENİ EXCEPTION GRUPLARI (%d):\n", len(born))
			}
			for _, g := range born {
				fmt.Fprintf(&b, "- %s · %s · %d occurrence (ilk: %s önce)\n",
					g.Service, g.Type, g.Occurrences, fmtAgoTR((to.UnixNano()-g.FirstSeen)/1e9))
			}
		}
	}

	src := fmt.Sprintf("problem pencere olayları + anomali olayları + deploy'lar + yeni exception grupları (%s, %s)",
		fmtAgoTR(rangeS), scope)
	return b.String(), src, nil
}
