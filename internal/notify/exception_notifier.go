package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/cilcenk/coremetry/internal/cache"
	"github.com/cilcenk/coremetry/internal/chstore"
)

// exception_notifier.go — P1 exception anonsu (v0.9.437, öneri #2).
// Operatör diliyle "anonslu exception'lar" şimdiye dek yalnız inbox'ta
// yaşıyordu — notify paketinde exception hook'u HİÇ yoktu; ekip sayfaya
// bakmadan haberdar olamıyordu. Bu işçi, inbox'ın P1 formülünü
// (exceptionPriority, internal/api/inbox.go: last_seen ≤5dk VE
// occurrences ≥500) geçen grupları takımlara mailler.
//
// Desen team-routing'in (v0.8.429) ikizi:
//   - Alıcılar servis kataloğunun owner+SRE takımlarından
//     (resolveTeamRecipients + team_contacts blob'u); tc.Enabled ve
//     MinSeverity kapıları AYNEN geçerli — ayrı bir toggle yok, team
//     routing kapalıysa bu anons da kapalıdır (bilinçli).
//   - Gönderim sentetik e-mail kanalı + sendOne — şablon, SMTP ve
//     notification_log kaydı elle kurulmuş kanalla bayt-bayt aynı
//     (SendRunbookComplete'in sentetik-Problem emsali).
//   - Dedup: notification_log related_kind="exception", related_id=
//     fingerprint — grup ömrü başına TEK anons (90g pencere).
//     Regresyon bilinçli olarak YENİDEN anons ETMEZ (v1; spam riski >
//     değer — gerekirse dedup anahtarına dönem eklenir).
//   - Leader-lock'lu 60s tik; her şey soft-fail, evaluator/ingest
//     yolunu asla bloklamaz.
const exceptionNotifierLockKey = "exception-notifier:lock"

// P1 eşiği — inbox.go exceptionPriority ile BİREBİR (oradaki formül
// değişirse burası da değişmeli; pin: exception_notifier_test.go).
const exNotifyFreshWindow = 5 * time.Minute
const exNotifyMinOccurrences = 500

type ExceptionNotifier struct {
	n        *Notifier
	store    *chstore.Store
	leader   *cache.LeaderHolder
	interval time.Duration
}

func NewExceptionNotifier(store *chstore.Store, n *Notifier, lock cache.Lock) *ExceptionNotifier {
	interval := 60 * time.Second
	return &ExceptionNotifier{
		n: n, store: store,
		leader:   cache.NewLeaderHolder(lock, exceptionNotifierLockKey, cache.LeaderTTL(interval)),
		interval: interval,
	}
}

func (e *ExceptionNotifier) Start(ctx context.Context) {
	if e == nil || e.n == nil {
		return
	}
	e.leader.Start(ctx)
	t := time.NewTicker(e.interval)
	defer t.Stop()
	e.tickIfLeader(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.tickIfLeader(ctx)
		}
	}
}

func (e *ExceptionNotifier) tickIfLeader(ctx context.Context) {
	if !e.leader.IsLeader() {
		return
	}
	tc, err := e.store.GetTeamContacts(ctx)
	if err != nil || !tc.Enabled {
		return // team routing kapalı → anons kapalı (bilinçli tek kapı)
	}
	e.run(ctx, tc)
}

func (e *ExceptionNotifier) run(ctx context.Context, tc chstore.TeamContacts) {
	now := time.Now()
	sent := 0
	for _, state := range []string{chstore.ExStateNew, chstore.ExStateRegressed} {
		groups, err := e.store.ListExceptionGroups(ctx, chstore.ExceptionGroupFilter{
			State: state, Limit: 100, MinOccurrences: exNotifyMinOccurrences,
		})
		if err != nil {
			log.Printf("[exception-notifier] list %s: %v", state, err)
			continue
		}
		for _, g := range groups {
			if !isP1ExceptionCandidate(g, now) {
				continue
			}
			if seen, herr := e.store.HasNotification(ctx, "exception", g.Fingerprint, teamRoutingChannelName); herr != nil || seen {
				continue
			}
			if e.sendExceptionMail(ctx, tc, g) {
				sent++
			}
		}
	}
	if sent > 0 {
		log.Printf("[exception-notifier] %d P1 exception anonsu gönderildi", sent)
	}
}

// isP1ExceptionCandidate — inbox P1 formülünün saf ikizi (tablo-testli).
func isP1ExceptionCandidate(g chstore.ExceptionGroup, now time.Time) bool {
	age := now.UnixNano() - g.LastSeen
	return time.Duration(age) <= exNotifyFreshWindow && g.Occurrences >= exNotifyMinOccurrences
}

// exceptionAsProblem — sentetik Problem: kanal şablonu Severity/Service/
// RuleName/Description alanlarını basar (SendRunbookComplete emsali).
// Saf — tablo-testli.
func exceptionAsProblem(g chstore.ExceptionGroup) chstore.Problem {
	return chstore.Problem{
		ID:          "exception:" + g.Fingerprint,
		Service:     g.Service,
		RuleName:    "Exception · " + g.Type,
		Severity:    "warning", // inbox exception satırlarıyla aynı (severity alanı yok)
		Metric:      "exception",
		Description: fmt.Sprintf("%s — %d occurrence (P1: son 5dk içinde aktif). %s", g.Type, g.Occurrences, g.Message),
		StartedAt:   g.FirstSeen,
	}
}

func (e *ExceptionNotifier) sendExceptionMail(ctx context.Context, tc chstore.TeamContacts, g chstore.ExceptionGroup) bool {
	if !tc.SeverityAllows("warning") {
		return false
	}
	var md *chstore.ServiceMetadata
	if md2, err := e.store.GetServiceMetadata(ctx, g.Service); err == nil {
		md = md2
	}
	to := resolveTeamRecipients(md, tc)
	if len(to) == 0 {
		return false
	}
	cfg, err := json.Marshal(EmailChannelConfig{Recipients: to})
	if err != nil {
		return false
	}
	ch := chstore.NotificationChannel{
		Name:   teamRoutingChannelName,
		Type:   "email",
		Config: cfg,
	}
	if err := e.n.sendOne(ctx, ch, exceptionAsProblem(g), "exception", g.Fingerprint); err != nil {
		log.Printf("[exception-notifier] %s (%s → %d rcpt): %v", g.Type, g.Service, len(to), err)
		return false
	}
	return true
}
