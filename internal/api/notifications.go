package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// GET /api/notifications/log — the notification dispatch history:
// every email / slack / teams / zoom / webhook / whatsapp send fanned
// out by internal/notify, success AND failure. Powers the /events
// "Notifications sent" surface (v0.8.241).
//
// Open to any signed-in role (viewer included): the destination is
// already MASKED at write time and the content went to operators
// anyway — there is nothing here a viewer shouldn't see.
//
// Params: from, to (RFC3339 / unix — parseTime), kind (channel_kind
// exact filter, "" = all), limit, offset.
func (s *Server) listNotificationLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from := parseTime(q.Get("from"))
	to := parseTime(q.Get("to"))
	kind := q.Get("kind")
	limit := parseInt(q.Get("limit"), 100)
	offset := parseInt(q.Get("offset"), 0)

	// Cache key hashes ALL inputs (v0.5.187). Minute-bucket the time
	// bounds so a scrolling "now" doesn't bust the key every second
	// while the 15s TTL keeps it near-live.
	key := fmt.Sprintf("notiflog:%d:%d:%s:%d:%d",
		from.Unix()/60, to.Unix()/60, kind, limit, offset)
	s.serveCached(w, r, key, 15*time.Second, func() (any, error) {
		logs, err := s.store.ListNotificationLog(r.Context(), from, to, kind, limit, offset)
		if err != nil {
			return nil, err
		}
		if logs == nil {
			logs = []chstore.NotificationLog{}
		}
		return logs, nil
	})
}
