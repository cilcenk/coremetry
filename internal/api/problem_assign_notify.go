package api

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// assigneeNotifyEmail is the pure gate deciding whether a Problem (re)assignment
// should email the assignee, and to which address (v0.8.289 — operator asked
// for a notification when a Problem is assigned to a person). Notify only when
// the new assignee is a person (an email — a team name auto-set from
// service_metadata like "payments" is not) AND the assignee actually changed
// (case-insensitive), so re-saving the same person doesn't re-send. Trimmed.
func assigneeNotifyEmail(newAssignee, oldAssignee string) (string, bool) {
	newAssignee = strings.TrimSpace(newAssignee)
	if newAssignee == "" || !strings.Contains(newAssignee, "@") {
		return "", false
	}
	if strings.EqualFold(newAssignee, strings.TrimSpace(oldAssignee)) {
		return "", false
	}
	return newAssignee, true
}

// assignEmailContent builds the assignment notification subject + body. link is
// the deep link into the Problems drawer for this problem (may be empty).
func assignEmailContent(p chstore.Problem, link string) (subject, body string) {
	title := strings.TrimSpace(p.RuleName)
	if title == "" {
		title = strings.TrimSpace(p.Description)
	}
	if title == "" {
		title = "a problem"
	}
	subject = fmt.Sprintf("[Coremetry] Assigned to you: %s", title)

	var b strings.Builder
	fmt.Fprintf(&b, "You've been assigned a problem in Coremetry.\n\n")
	fmt.Fprintf(&b, "  %s\n", title)
	if p.Service != "" {
		fmt.Fprintf(&b, "  Service:  %s\n", p.Service)
	}
	if p.Severity != "" {
		fmt.Fprintf(&b, "  Severity: %s\n", p.Severity)
	}
	if p.Metric != "" {
		fmt.Fprintf(&b, "  Metric:   %s = %.2f (threshold %.2f)\n", p.Metric, p.Value, p.Threshold)
	}
	if p.Description != "" && !strings.EqualFold(p.Description, title) {
		fmt.Fprintf(&b, "\n%s\n", p.Description)
	}
	if link != "" {
		fmt.Fprintf(&b, "\nOpen it: %s\n", link)
	}
	return subject, b.String()
}

// notifyAssignee sends the assignment email in the background (fire-and-forget,
// like SendRunbookComplete) so a slow/misconfigured SMTP never blocks the
// assignee PATCH. A nil notifier or send error is logged, not surfaced.
func (s *Server) notifyAssignee(email string, p chstore.Problem, link string) {
	if s.notify == nil {
		return
	}
	subject, body := assignEmailContent(p, link)
	go func() {
		if err := s.notify.SendMail(context.Background(), []string{email}, subject, body); err != nil {
			log.Printf("[assign] notify %s of problem %s failed: %v", email, p.ID, err)
		}
	}()
}
