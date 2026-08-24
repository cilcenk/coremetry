package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.9.1278 — channel health badge. Settings → Channels now derives a
// per-channel verdict from notification_log so a dead SMTP relay or a
// rotated webhook token is visible where the channel is configured,
// instead of only in /events.
//
// The load-bearing arithmetic is channelHealthFromRows' "consecutive
// failures SINCE THE LAST SUCCESS" walk. The fixtures below are built
// so that dropping the stop-at-success break (counting every failure in
// the window instead) changes the answer — a fixture whose failures all
// postdate the last success would stay green under that mutation and
// prove nothing.

func mkSend(kind, name string, minsAgo int, ok bool, errMsg string) channelSend {
	base := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	return channelSend{
		Kind:   kind,
		Name:   name,
		SentAt: base.Add(-time.Duration(minsAgo) * time.Minute).UnixNano(),
		OK:     ok,
		Error:  errMsg,
	}
}

func TestChannelHealthFromRows(t *testing.T) {
	cases := []struct {
		name  string
		sends []channelSend
		limit int
		want  []ChannelHealthRow
	}{
		{
			name:  "empty window yields no rows",
			sends: nil,
			limit: channelHealthCap,
			want:  nil,
		},
		{
			// Healthy channel: one success, no failures behind it.
			name:  "single success",
			sends: []channelSend{mkSend("email", "oncall", 5, true, "")},
			limit: channelHealthCap,
			want: []ChannelHealthRow{{
				ChannelKind: "email", ChannelName: "oncall",
				LastAt: mkSend("email", "oncall", 5, true, "").SentAt,
				LastOK: true, ConsecFails: 0,
			}},
		},
		{
			// THE mutation fixture. Newest→oldest: F F F OK F.
			// Correct  = 3 (stop at the success).
			// Mutated  = 4 (keep counting past it).
			name: "three failures after a success, one failure before it",
			sends: []channelSend{
				mkSend("webhook", "pagerduty", 1, false, "dial tcp: connection refused"),
				mkSend("webhook", "pagerduty", 2, false, "dial tcp: connection refused"),
				mkSend("webhook", "pagerduty", 3, false, "dial tcp: connection refused"),
				mkSend("webhook", "pagerduty", 9, true, ""),
				mkSend("webhook", "pagerduty", 20, false, "older unrelated failure"),
			},
			limit: channelHealthCap,
			want: []ChannelHealthRow{{
				ChannelKind: "webhook", ChannelName: "pagerduty",
				LastAt:      mkSend("webhook", "pagerduty", 1, false, "").SentAt,
				LastOK:      false,
				LastError:   "dial tcp: connection refused",
				ConsecFails: 3,
			}},
		},
		{
			// No success at all in the window → every failure counts.
			name: "never succeeded",
			sends: []channelSend{
				mkSend("email", "smtp-relay", 4, false, "550 relay denied"),
				mkSend("email", "smtp-relay", 30, false, "550 relay denied"),
			},
			limit: channelHealthCap,
			want: []ChannelHealthRow{{
				ChannelKind: "email", ChannelName: "smtp-relay",
				LastAt:      mkSend("email", "smtp-relay", 4, false, "").SentAt,
				LastOK:      false,
				LastError:   "550 relay denied",
				ConsecFails: 2,
			}},
		},
		{
			// Two channels interleaved in the global sent_at DESC stream:
			// the fold must not let one channel's success reset the other's
			// counter. Output is sorted by (kind, name).
			name: "two channels interleaved",
			sends: []channelSend{
				mkSend("slack", "alerts", 1, false, "invalid_token"),
				mkSend("email", "oncall", 2, true, ""),
				mkSend("slack", "alerts", 3, false, "invalid_token"),
				mkSend("email", "oncall", 6, false, "timeout"),
				mkSend("slack", "alerts", 8, true, ""),
			},
			limit: channelHealthCap,
			want: []ChannelHealthRow{
				{
					ChannelKind: "email", ChannelName: "oncall",
					LastAt: mkSend("email", "oncall", 2, true, "").SentAt,
					LastOK: true, ConsecFails: 0,
				},
				{
					ChannelKind: "slack", ChannelName: "alerts",
					LastAt:      mkSend("slack", "alerts", 1, false, "").SentAt,
					LastOK:      false,
					LastError:   "invalid_token",
					ConsecFails: 2,
				},
			},
		},
		{
			// Rows arriving out of order (a future edit drops the SQL
			// ORDER BY, or CH returns a differently-merged page): the fold
			// re-sorts, so LastAt is still the NEWEST send.
			name: "out-of-order input still picks the newest send",
			sends: []channelSend{
				mkSend("teams", "sre", 40, true, ""),
				mkSend("teams", "sre", 2, false, "403 forbidden"),
				mkSend("teams", "sre", 15, false, "403 forbidden"),
			},
			limit: channelHealthCap,
			want: []ChannelHealthRow{{
				ChannelKind: "teams", ChannelName: "sre",
				LastAt:      mkSend("teams", "sre", 2, false, "").SentAt,
				LastOK:      false,
				LastError:   "403 forbidden",
				ConsecFails: 2,
			}},
		},
		{
			// Cover-fetch hit its cap → the counts are a lower bound and
			// EVERY row says so, including the healthy one.
			name: "cap reached stamps every row",
			sends: []channelSend{
				mkSend("email", "oncall", 1, true, ""),
				mkSend("slack", "alerts", 2, false, "invalid_token"),
			},
			limit: 2,
			want: []ChannelHealthRow{
				{
					ChannelKind: "email", ChannelName: "oncall",
					LastAt: mkSend("email", "oncall", 1, true, "").SentAt,
					LastOK: true, ConsecFails: 0, Capped: true,
				},
				{
					ChannelKind: "slack", ChannelName: "alerts",
					LastAt:      mkSend("slack", "alerts", 2, false, "").SentAt,
					LastOK:      false,
					LastError:   "invalid_token",
					ConsecFails: 1,
					Capped:      true,
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := channelHealthFromRows(tc.sends, tc.limit)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d rows, want %d (%+v)", len(got), len(tc.want), got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("row %d:\n got  %+v\n want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// The read bounds are a CLAUDE.md hard constraint: a 90-day
// notification_log with no time predicate, no LIMIT and no execution cap
// would full-scan every time an admin opens Settings. Pins the SQL shape
// without a live CH.
func TestBuildChannelHealthQuery_Bounds(t *testing.T) {
	sql, args := buildChannelHealthQuery(time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC), channelHealthCap)

	for _, frag := range []string{
		"sent_at >= ?",          // time bound on the ORDER BY prefix
		"ORDER BY sent_at DESC", // newest-first, prefix-aligned
		"LIMIT ?",               // bounded row count
		"max_execution_time",    // wall-clock cap
		"FROM notification_log", // plain MergeTree — never FINAL
	} {
		if !strings.Contains(sql, frag) {
			t.Errorf("query missing %q:\n%s", frag, sql)
		}
	}
	if strings.Contains(sql, "FINAL") {
		t.Errorf("notification_log is a plain MergeTree; FINAL must not appear:\n%s", sql)
	}
	// v0.9.1344 — args artık ÜÇ: (since, sentinel-kind, limit). Sentetik
	// "kimseye gitmedi" satırı bu okumadan eleniyor (aşağıdaki test).
	//
	// Limit SONDAN indeksleniyor: bu test v0.9.1344'te args[1] sabit
	// indeksi yüzünden KIRILDI ve kırılışı doğruydu — ama "son bağ
	// argümanı limittir" ifadesi sorgunun şeklinden gelen bir olgu,
	// pozisyon sayısı ise değil.
	if len(args) != 3 {
		t.Fatalf("args = %d, want 3 (since, sentinel kind, limit)", len(args))
	}
	if args[len(args)-1] != channelHealthCap {
		t.Errorf("limit arg = %v, want %d", args[len(args)-1], channelHealthCap)
	}
}

// TestBuildChannelHealthQuery_ExcludesUnmatchedSentinel — v0.9.1344.
//
// notification_log artık sentetik bir satır da taşıyor: bir problem
// hiçbir kanalla eşleşmediğinde (kind=none, name=unmatched, ok=0).
// O satır bir KANAL DEĞİL. Elenmezse Settings → Kanallar listesinde
// ardışık hatası sürekli artan hayalet bir "none/unmatched" kanalı
// belirir — ve kanal sağlığı ekranı tam da güvenilmesi gereken yerde
// uydurma bir arıza gösterir.
func TestBuildChannelHealthQuery_ExcludesUnmatchedSentinel(t *testing.T) {
	sql, args := buildChannelHealthQuery(time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC), channelHealthCap)

	if !strings.Contains(sql, "channel_kind != ?") {
		t.Errorf("sentetik işaret elemesi yok:\n%s", sql)
	}
	found := false
	for _, a := range args {
		if s, ok := a.(string); ok && s == NotifyUnmatchedChannelKind {
			found = true
		}
	}
	if !found {
		t.Errorf("eleme argümanı %q bağlanmamış: %v", NotifyUnmatchedChannelKind, args)
	}
	// Sentinel gerçek bir kanal türüyle çakışamaz: çakışsaydı eleme o
	// türdeki GERÇEK kanalları da sağlık ekranından silerdi — sessiz
	// bir körlük, gösterdiği hayaletten beter.
	for _, real := range []string{"email", "slack", "mattermost", "teams", "zoomchat", "webhook", "whatsapp"} {
		if NotifyUnmatchedChannelKind == real {
			t.Fatalf("sentinel %q gerçek bir kanal türü — eleme o kanalları da gizlerdi", real)
		}
	}
}

func TestBuildChannelHealthQuery_LimitClamp(t *testing.T) {
	since := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		limit int
		want  int
	}{
		{"zero -> cap", 0, channelHealthCap},
		{"negative -> cap", -1, channelHealthCap},
		{"oversized -> cap", channelHealthCap * 10, channelHealthCap},
		{"in-range kept", 250, 250},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, args := buildChannelHealthQuery(since, tc.limit)
			// Limit SON bağ argümanı (v0.9.1344 not'u yukarıda).
			if got := args[len(args)-1]; got != tc.want {
				t.Errorf("limit = %v, want %d", got, tc.want)
			}
		})
	}
}
