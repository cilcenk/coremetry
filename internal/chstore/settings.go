package chstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ── system_settings ─────────────────────────────────────────────────────────
//
// Key/value store for global configuration that has to outlive a process.
// SMTP credentials live here today; future global toggles (signup
// allowed?, default retention overrides…) can reuse it.

// GetSetting returns the JSON-encoded value for key, or nil if missing.
//
// v0.10.259 — ctx'te SettingsSnapshot varsa (settings_refresh.go, periyodik
// tazeleme) cevap oradan gelir: 14 yenileyici × 30 s tekil FINAL okuma
// yerine tik başına tek sorgu. Anlık görüntüde olmayan anahtar = yok
// (nil, nil) — tablo tek sorguyla bütün okunduğu için doğru.
func (s *Store) GetSetting(ctx context.Context, key string) ([]byte, error) {
	if snap := settingsSnapshotFrom(ctx); snap != nil {
		v, _ := snap.get(key)
		return v, nil
	}
	row := s.conn.QueryRow(ctx, `
		SELECT value FROM system_settings FINAL WHERE key = ? LIMIT 1`, key)
	var v string
	if err := row.Scan(&v); err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	return []byte(v), nil
}

// settingsInsertSQL — v0.10.129: updated_at + version İSTEMCİ damgasıyla
// AÇIKÇA yazılır. Operator-reported: "modeli Settings'ten kaydediyorum,
// yine eski modele dönüyor". system_settings ReplicatedReplacingMergeTree;
// yalnız (key, value) yazılınca blok özeti o iki kolondan çıkıyor ve daha
// önce yazılmış birebir aynı blob (A → B → A) Replicated INSERT dedup'uyla
// SESSİZCE düşüyordu (lokalde ölçüldü: iki özdeş INSERT → 1 satır); FINAL
// en yüksek version'lı eski değeri döndürüyor, 30 s poll bellekteki yeniyi
// geri alıyordu. Damga her bloğu benzersiz kılar; dedup tetiklenmez.
const settingsInsertSQL = "INSERT INTO system_settings (key, value, updated_at, version)"

// settingsVersion — sunucu DEFAULT'uyla aynı birim (UnixNano). Saf; testli.
func settingsVersion(now time.Time) uint64 { return uint64(now.UnixNano()) }

// PutSetting upserts the JSON-encoded value at key.
func (s *Store) PutSetting(ctx context.Context, key string, value []byte) error {
	batch, err := s.conn.PrepareBatch(ctx, settingsInsertSQL)
	if err != nil {
		return fmt.Errorf("prepare settings: %w", err)
	}
	now := time.Now()
	if err := batch.Append(key, string(value), now, settingsVersion(now)); err != nil {
		return fmt.Errorf("append setting: %w", err)
	}
	return batch.Send()
}

// ── notification_channels ───────────────────────────────────────────────────

type NotificationChannel struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Type        string          `json:"type"`   // email | slack | webhook
	Config      json.RawMessage `json:"config"` // type-specific
	Enabled     bool            `json:"enabled"`
	MinSeverity string          `json:"minSeverity"` // info | warning | critical
	// MatchRules — routing predicates. Empty / zero-value
	// fields mean "match anything" so the default channel
	// stays a catch-all. Populated arrays AND together: a
	// channel only fires when its services / sreTeams /
	// ownerTeams ALL match the problem's service catalog.
	MatchRules ChannelMatchRules `json:"matchRules,omitempty"`
	CreatedAt  int64             `json:"createdAt"` // unix ns
}

// ChannelMatchRules — small predicate set that gates
// delivery per channel. Each list is "OR within, AND between
// lists":
//   - services    = []string of literal service names
//   - sreTeams    = []string of catalog SRE team names
//   - ownerTeams  = []string of catalog product owner team names
//   - clusters    = []string of k8s/openshift cluster names —
//     matches against the problem's enriched cluster list
//     (typically populated by EnrichProblemsWithClusters
//     before the channel fan-out)
//   - quietHours  = "HH:MM-HH:MM" window during which the
//     channel does NOT fire. Empty = always-on. The window
//     may cross midnight (e.g. "22:00-07:00"); evaluated in
//     QuietHoursTz which defaults to UTC.
//   - quietHoursTz = IANA timezone for quietHours (e.g.
//     "Europe/Istanbul"). Empty = UTC.
//
// Common operator patterns this supports:
//   - "Pager rota only for prod-eu-west during business hrs":
//     clusters=[prod-eu-west], quietHours="00:00-08:00",
//     quietHoursTz="Europe/Istanbul"
//   - "Staging channel — staging cluster only":
//     clusters=[prod-staging]
//   - "Weekend on-call inbox":
//     ownerTeams=[payments], quietHours empty
//   - minPriority = "P1" | "P2" | "P3" (boş = hepsi). Kanal YALNIZ bu
//     triyaj basamağında ya da ÜSTÜNDE olan problemleri alır (v0.9.828):
//     "P2" seçilen bir kanal P1 ve P2 alır, P3 almaz.
type ChannelMatchRules struct {
	Services     []string `json:"services,omitempty"`
	SRETeams     []string `json:"sreTeams,omitempty"`
	OwnerTeams   []string `json:"ownerTeams,omitempty"`
	Clusters     []string `json:"clusters,omitempty"`
	QuietHours   string   `json:"quietHours,omitempty"`
	QuietHoursTz string   `json:"quietHoursTz,omitempty"`
	// MinPriority — en düşük triyaj basamağı (v0.9.828). Boş = hepsi.
	//
	// DDL GEREKMEZ: match_rules zaten notification_channels üzerinde tek
	// bir JSON kolonu, yani yeni alan mevcut satırlara "yok" olarak
	// gelir ve "yok" = filtre yok = bugünkü davranış.
	//
	// Ciddiyet süzgeciyle (MinSeverity) NEDEN AYRI: ciddiyet "ne kadar
	// kötü", öncelik "ne kadar acil" diyor ve ikisi ayrışabiliyor. Bir
	// critical problem P2 olabilir (eşiğin 2 katına çıkmamış, 4 saattir
	// açık değil); bir monitor DOWN ise tam kayıp olduğu için P1'dir.
	// Çağrı cihazına yalnız "şimdi kalk" olanları göndermek isteyen
	// operatörün ihtiyacı olan kapı budur.
	MinPriority string `json:"minPriority,omitempty"`
}

// priorityRank — triyaj basamağının sayısal karşılığı; YALNIZ
// karşılaştırma için. Tanınmayan/boş değer 0 alır ve aşağıdaki yüklem
// onu "ölçemedim" sayıp AÇIK GEÇER.
func priorityRank(p string) int {
	switch strings.ToUpper(strings.TrimSpace(p)) {
	case "P1":
		return 3
	case "P2":
		return 2
	case "P3":
		return 1
	}
	return 0
}

// allowsPriority — kanal bu önceliği alır mı?
//
// İKİ HALDE AÇIK GEÇER ve ikisi de bilinçli:
//   - kural boş (operatör süzgeç kurmamış);
//   - problemin önceliği HESAPLANMAMIŞ (bu funnel'dan geçmeyen bir
//     çağrı, ya da tanınmayan bir etiket).
//
// İkincisi kritik: ölçemediğimiz bir alan yüzünden bir sayfayı YEMEK,
// bu kapının çözmeye çalıştığı sorundan çok daha pahalı. Süzgecin işi
// gürültüyü kırpmak, haber kaybetmek değil.
func (m ChannelMatchRules) allowsPriority(problemPriority string) bool {
	want := priorityRank(m.MinPriority)
	if want == 0 {
		return true
	}
	got := priorityRank(problemPriority)
	if got == 0 {
		return true
	}
	return got >= want
}

// MatchInput bundles the runtime signals Matches needs. We
// pass a struct instead of growing the arg list every time we
// add a predicate; existing call sites switched to use it via
// MatchesProblem below.
type MatchInput struct {
	Service  string
	Metadata *ServiceMetadata
	Clusters []string  // problem.Clusters after enrichment
	Now      time.Time // override for tests; zero = time.Now()
	// Priority — problemin triyaj basamağı (P1/P2/P3), bildirim anında
	// hesaplanmış (v0.9.828). Boş = hesaplanmamış; minPriority yüklemi
	// o durumda AÇIK GEÇER (bkz. allowsPriority).
	Priority string
}

// MatchesProblem evaluates every predicate against a Problem's
// runtime signals. Empty / zero-value rules mean catch-all
// (always true); the predicate's job is to PROVE the channel
// should be silenced, otherwise we fire.
func (m ChannelMatchRules) MatchesProblem(in MatchInput) bool {
	if !m.Matches(in.Service, in.Metadata) {
		return false
	}
	if len(m.Clusters) > 0 {
		if len(in.Clusters) == 0 {
			return false
		}
		hit := false
		for _, c := range m.Clusters {
			for _, pc := range in.Clusters {
				if c == pc {
					hit = true
					break
				}
			}
			if hit {
				break
			}
		}
		if !hit {
			return false
		}
	}
	if m.QuietHours != "" {
		now := in.Now
		if now.IsZero() {
			now = time.Now()
		}
		if isInQuietWindow(now, m.QuietHours, m.QuietHoursTz) {
			return false
		}
	}
	// v0.9.828 — triyaj basamağı kapısı. En sonda çünkü en yeni ve
	// diğerlerinden bağımsız; sırası sonucu değiştirmiyor (hepsi AND).
	if !m.allowsPriority(in.Priority) {
		return false
	}
	return true
}

// Matches retains the pre-v0.5.63 signature so existing
// callers that only need service / catalog matching keep
// compiling. New code paths thread MatchInput through
// MatchesProblem.
func (m ChannelMatchRules) Matches(service string, md *ServiceMetadata) bool {
	if len(m.Services) > 0 {
		hit := false
		for _, s := range m.Services {
			if s == service {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	if len(m.SRETeams) > 0 {
		if md == nil {
			return false
		}
		hit := false
		for _, t := range m.SRETeams {
			if t == md.SRETeam {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	if len(m.OwnerTeams) > 0 {
		if md == nil {
			return false
		}
		hit := false
		for _, t := range m.OwnerTeams {
			if t == md.OwnerTeam {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// isInQuietWindow parses "HH:MM-HH:MM" and returns true when
// `now` (in the configured timezone) falls inside the
// window. Windows that span midnight (start > end) are
// supported — operators with after-hours rotas need them.
// Malformed input returns false so a typo doesn't silence
// the channel forever.
func isInQuietWindow(now time.Time, window, tz string) bool {
	loc := time.UTC
	if tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	now = now.In(loc)
	parts := strings.SplitN(window, "-", 2)
	if len(parts) != 2 {
		return false
	}
	startH, startM, ok1 := parseHHMM(parts[0])
	endH, endM, ok2 := parseHHMM(parts[1])
	if !ok1 || !ok2 {
		return false
	}
	nowMin := now.Hour()*60 + now.Minute()
	startMin := startH*60 + startM
	endMin := endH*60 + endM
	if startMin <= endMin {
		return nowMin >= startMin && nowMin < endMin
	}
	// Crosses midnight: in-window if now ≥ start OR now < end.
	return nowMin >= startMin || nowMin < endMin
}

func parseHHMM(s string) (int, int, bool) {
	s = strings.TrimSpace(s)
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return 0, 0, false
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

func (s *Store) ListChannels(ctx context.Context) ([]NotificationChannel, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT id, name, type, config, enabled, min_severity,
		       match_rules,
		       toUnixTimestamp64Nano(created_at)
		FROM notification_channels FINAL
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NotificationChannel
	for rows.Next() {
		var c NotificationChannel
		var config, matchRules string
		var enabled uint8
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &config, &enabled, &c.MinSeverity,
			&matchRules, &c.CreatedAt); err != nil {
			return nil, err
		}
		if config == "" {
			config = "{}"
		}
		c.Config = json.RawMessage(config)
		c.Enabled = enabled != 0
		// Match rules are stored as a JSON blob in the column;
		// errors (malformed legacy data) collapse to the
		// empty / catch-all value rather than dropping the
		// whole channel.
		if matchRules != "" {
			_ = json.Unmarshal([]byte(matchRules), &c.MatchRules)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// EnabledChannelsForSeverity is what the notifier calls when a Problem
// opens. Returns only enabled channels whose min_severity ≤ the problem's
// severity (so a "critical" problem fires every channel; "info" fires
// only the ones explicitly subscribed at info level).
func (s *Store) EnabledChannelsForSeverity(ctx context.Context, severity string) ([]NotificationChannel, error) {
	all, err := s.ListChannels(ctx)
	if err != nil {
		return nil, err
	}
	threshold := severityRank(severity)
	out := make([]NotificationChannel, 0, len(all))
	for _, c := range all {
		if !c.Enabled {
			continue
		}
		if severityRank(c.MinSeverity) > threshold {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func (s *Store) GetChannel(ctx context.Context, id string) (*NotificationChannel, error) {
	row := s.conn.QueryRow(ctx, `
		SELECT id, name, type, config, enabled, min_severity,
		       match_rules,
		       toUnixTimestamp64Nano(created_at)
		FROM notification_channels FINAL
		WHERE id = ? LIMIT 1`, id)
	var c NotificationChannel
	var config, matchRules string
	var enabled uint8
	if err := row.Scan(&c.ID, &c.Name, &c.Type, &config, &enabled, &c.MinSeverity,
		&matchRules, &c.CreatedAt); err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	if config == "" {
		config = "{}"
	}
	c.Config = json.RawMessage(config)
	c.Enabled = enabled != 0
	if matchRules != "" {
		_ = json.Unmarshal([]byte(matchRules), &c.MatchRules)
	}
	return &c, nil
}

func (s *Store) UpsertChannel(ctx context.Context, c NotificationChannel) error {
	if c.ID == "" {
		return fmt.Errorf("channel id required")
	}
	if c.MinSeverity == "" {
		c.MinSeverity = "warning"
	}
	if len(c.Config) == 0 {
		c.Config = json.RawMessage("{}")
	}
	if c.CreatedAt == 0 {
		c.CreatedAt = time.Now().UnixNano()
	}
	// Marshal match rules into the column. Always populate
	// the column so the read path doesn't have to handle a
	// "missing argument" form for the legacy rows that pre-
	// date the column — the migration ALTER + the always-
	// populated insert keep the shape stable.
	mr, err := json.Marshal(c.MatchRules)
	if err != nil {
		return fmt.Errorf("marshal match rules: %w", err)
	}
	batch, err := s.conn.PrepareBatch(ctx,
		"INSERT INTO notification_channels (id, name, type, config, enabled, min_severity, match_rules)")
	if err != nil {
		return fmt.Errorf("prepare channels: %w", err)
	}
	var en uint8
	if c.Enabled {
		en = 1
	}
	if err := batch.Append(c.ID, c.Name, c.Type, string(c.Config), en, c.MinSeverity, string(mr)); err != nil {
		return fmt.Errorf("append channel: %w", err)
	}
	return batch.Send()
}

func (s *Store) DeleteChannel(ctx context.Context, id string) error {
	return s.conn.Exec(ctx, `ALTER TABLE notification_channels DELETE WHERE id = ?`, id)
}

func severityRank(s string) int {
	switch s {
	case "critical":
		return 3
	case "warning":
		return 2
	case "info":
		return 1
	}
	return 2 // unknown → treat as warning
}

// ── Team routing (v0.8.429) ─────────────────────────────────────────────────
//
// Operator ask: "yeni bir problem ilk defa geldiğinde ilgili sy ve ug
// team'e bildirim gönderilsin — mailleri katalogdan alsın." The catalog
// carries team NAMES (service_metadata owner_team / sre_team); this
// system_settings blob maps those names to e-mail addresses and gates
// the automatic problem-open → team-mail path in internal/notify.
// One settings key, no new table (invariant #6).

const TeamContactsKey = "team_contacts"

// TeamContacts is the "team_contacts" system_settings value.
type TeamContacts struct {
	Enabled bool `json:"enabled"`
	// MinSeverity — info | warning | critical; "" defaults to warning
	// so a fresh install doesn't mail every info-level blip.
	MinSeverity string `json:"minSeverity,omitempty"`
	// Contacts maps a catalog team name → e-mail address(es). Values
	// may be comma-separated for multi-recipient teams. Lookup is
	// case-insensitive (mixed-casing team attrs, v0.8.330 lesson).
	Contacts map[string]string `json:"contacts"`
}

// SeverityAllows reports whether a problem of severity sev clears the
// blob's MinSeverity floor (default warning).
func (tc TeamContacts) SeverityAllows(sev string) bool {
	min := tc.MinSeverity
	if min == "" {
		min = "warning"
	}
	return severityRank(sev) >= severityRank(min)
}

// EmailsForTeam resolves one catalog team name to its configured
// addresses — case-insensitive key match, comma-split, trimmed.
// Missing / empty team or contact yields nil (callers skip silently;
// the Settings UI surfaces which catalog teams lack an address).
func (tc TeamContacts) EmailsForTeam(team string) []string {
	team = strings.TrimSpace(team)
	if team == "" {
		return nil
	}
	var raw string
	for k, v := range tc.Contacts {
		if strings.EqualFold(strings.TrimSpace(k), team) {
			raw = v
			break
		}
	}
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// GetTeamContacts loads the blob; a missing key returns the zero value
// (disabled, empty map) — never an error the caller must special-case.
func (s *Store) GetTeamContacts(ctx context.Context) (TeamContacts, error) {
	var tc TeamContacts
	raw, err := s.GetSetting(ctx, TeamContactsKey)
	if err != nil || len(raw) == 0 {
		return tc, err
	}
	if err := json.Unmarshal(raw, &tc); err != nil {
		return TeamContacts{}, err
	}
	return tc, nil
}

// PutTeamContacts persists the blob (admin PUT path; audited at the
// API layer like every settings write).
func (s *Store) PutTeamContacts(ctx context.Context, tc TeamContacts) error {
	raw, err := json.Marshal(tc)
	if err != nil {
		return err
	}
	return s.PutSetting(ctx, TeamContactsKey, raw)
}

// ── Team aliases (v0.9.427, operatör istegi) ────────────────────────────────
// LDAP takım adı ("SY-Dijital Bankacılık") ile telemetri metadata'sındaki
// takım adları ("dijitalsy", "avengersy"…) aynı takımın farklı yazımları
// olabiliyor; hiçbir algoritma "avengersy → SY-Krediler ve Sigorta"yı
// bilemez — eşleme OPERATÖR tablosudur. Tek settings anahtarı,
// invariant #6.

const TeamAliasesKey = "team_aliases"

// TeamAliases — alias → kanonik ad. Anahtarlar da değerler de serbest
// yazımdır; TÜM karşılaştırmalar CanonTeam üzerinden normalize edilir
// (küçük harf + trim + Türkçe İ'nin combining-dot artığı temizliği).
type TeamAliases struct {
	Aliases map[string]string `json:"aliases"`
}

// NormTeamName — takım adı karşılaştırma anahtarı. İki Türkçe I tuzağı
// birden: (1) Go'nun ToLower'ı 'İ'yi "i"+U+0307 yapar → combining dot
// atılır; (2) ASCII 'I', Türkçede 'ı'ya inmeliyken Go 'i' verir
// ("BANKACILIK"→"bankacilik" ama "Bankacılık"→"bankacılık") → noktalı/
// noktasız i TEK forma katlanır. Yalnız eşleşme anahtarıdır; gösterim
// orijinal yazımı korur.
//
// v0.9.1134 — DIŞA AÇIK. Guided chat'in takım-adı çıkarıcısı
// (extractTeamEntity, internal/api/copilot_guided.go) mesajı ve katalog
// takım adlarını AYNI kuralla katlamak zorunda; kuralı orada yeniden
// yazmak "bir kural iki yere bölünüp zamanla ayrışıyor" hata sınıfıydı.
// Tek yazım kalsın diye export edildi.
func NormTeamName(s string) string {
	n := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "̇", "")
	return strings.ReplaceAll(n, "ı", "i")
}

// CanonTeam — adı kanonik forma indirir: alias tablosunda varsa hedefin
// normali, yoksa kendi normali. Tek seviyedir (alias'ın alias'ı yok —
// tablo zaten kanonik hedefe yazılır).
func (ta TeamAliases) CanonTeam(name string) string {
	n := NormTeamName(name)
	if n == "" {
		return ""
	}
	for k, v := range ta.Aliases {
		if NormTeamName(k) == n {
			return NormTeamName(v)
		}
	}
	return n
}

// TeamEqual — iki takım adı aynı takım mı (alias + normalizasyon).
func (ta TeamAliases) TeamEqual(a, b string) bool {
	ca, cb := ta.CanonTeam(a), ta.CanonTeam(b)
	return ca != "" && ca == cb
}

func (s *Store) GetTeamAliases(ctx context.Context) (TeamAliases, error) {
	var ta TeamAliases
	raw, err := s.GetSetting(ctx, TeamAliasesKey)
	if err != nil || len(raw) == 0 {
		return ta, err
	}
	if err := json.Unmarshal(raw, &ta); err != nil {
		return TeamAliases{}, err
	}
	return ta, nil
}

func (s *Store) PutTeamAliases(ctx context.Context, ta TeamAliases) error {
	raw, err := json.Marshal(ta)
	if err != nil {
		return err
	}
	return s.PutSetting(ctx, TeamAliasesKey, raw)
}
