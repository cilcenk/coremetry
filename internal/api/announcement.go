package api

// Duyuru şeridi handler'ları (v0.8.486). Branding'in ikizi: GET tüm
// oturumlara açık (global auth middleware yeter), PUT admin + audit.
// GET serveCached'li (60s) — şerit anlık olmak zorunda değil; PUT
// prefix'i düşürür, yeni metin bir sonraki sayfa açılışında görünür.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
)

const (
	announcementTextMax  = 500
	announcementLabelMax = 80
)

func (s *Server) registerAnnouncementRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/announcement", s.getAnnouncement)
	mux.HandleFunc("PUT /api/admin/announcement", auth.RequireRole(auth.RoleAdmin, s.putAnnouncement))
}

func (s *Server) getAnnouncement(w http.ResponseWriter, r *http.Request) {
	s.serveCached(w, r, "announcement", 60*time.Second, func(ctx context.Context) (any, error) {
		a, err := s.store.GetAnnouncement(ctx)
		if err != nil {
			return nil, err
		}
		return a, nil
	})
}

func (s *Server) putAnnouncement(w http.ResponseWriter, r *http.Request) {
	var body chstore.Announcement
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	body.Text = strings.TrimSpace(body.Text)
	body.LinkURL = strings.TrimSpace(body.LinkURL)
	body.LinkLabel = strings.TrimSpace(body.LinkLabel)
	if body.Enabled && body.Text == "" {
		http.Error(w, `{"error":"duyuru metni boş olamaz"}`, http.StatusBadRequest)
		return
	}
	if len([]rune(body.Text)) > announcementTextMax {
		http.Error(w, fmt.Sprintf(`{"error":"metin en fazla %d karakter"}`, announcementTextMax), http.StatusBadRequest)
		return
	}
	if len([]rune(body.LinkLabel)) > announcementLabelMax {
		http.Error(w, fmt.Sprintf(`{"error":"link etiketi en fazla %d karakter"}`, announcementLabelMax), http.StatusBadRequest)
		return
	}
	// Serbest HTML yok; link de yalnız http(s) — XSS/javascript: yüzeyi
	// açılmaz (metin düz yazı olarak render edilir).
	if body.LinkURL != "" {
		u, err := url.Parse(body.LinkURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			http.Error(w, `{"error":"link http(s) olmalı"}`, http.StatusBadRequest)
			return
		}
	}
	switch body.Tone {
	case "", "info":
		body.Tone = "info"
	case "warn":
		// ok
	default:
		http.Error(w, `{"error":"ton info|warn olmalı"}`, http.StatusBadRequest)
		return
	}

	saved, err := s.store.SaveAnnouncement(r.Context(), body)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.cacheInvalidatePrefix(r.Context(), "announcement")
	details, _ := json.Marshal(map[string]any{
		"enabled": saved.Enabled, "tone": saved.Tone,
		"textLen": len([]rune(saved.Text)), "hasLink": saved.LinkURL != "",
	})
	s.audit(r, "settings.announcement.update", "settings", "announcement", string(details))
	writeJSON(w, saved)
}
