package monitor

// Pure decision cores for the tcp / ssl-cert / keyword synthetic-monitor
// types (v0.8.283). Kept free of I/O so they're table-tested exhaustively
// in checks_test.go; the probe wrappers in runner.go do the dial / handshake
// / GET and hand the raw observation to these.

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// evalCertExpiry decides a leaf certificate's health from its NotAfter, the
// current time, and the monitor's warn-days threshold. Returns the monitor
// status ("up"/"down"), the whole days remaining (negative once expired, so
// the UI can render "-3d" for a cert that lapsed 3 days ago), and a human
// message.
//
// DOWN when the cert is expired OR days-remaining < warnDays. The boundary
// (days == warnDays) is still UP — you're warned strictly *inside* the
// window. Days are floored whole days (truncating division), matching how an
// operator reads "37 days left".
func evalCertExpiry(notAfter, now time.Time, warnDays uint16) (status string, daysRemaining int64, msg string) {
	// Truncating integer division gives whole days; time.Duration.Hours()/24
	// would carry a fractional tail that makes "exactly N days" flaky.
	daysRemaining = int64(notAfter.Sub(now) / (24 * time.Hour))

	switch {
	case !notAfter.After(now):
		return "down", daysRemaining, fmt.Sprintf("certificate expired %d day(s) ago", -daysRemaining)
	case daysRemaining < int64(warnDays):
		return "down", daysRemaining, fmt.Sprintf("certificate expires in %d day(s) (warn threshold %d)", daysRemaining, warnDays)
	default:
		return "up", daysRemaining, fmt.Sprintf("certificate valid, %d day(s) remaining", daysRemaining)
	}
}

// evalKeyword decides a keyword monitor from the (already size-capped)
// response body. Default posture: UP when the body contains the keyword.
// invert flips it to a "must NOT contain" assertion (e.g. alert when the
// body starts showing the string "maintenance" or "error"). Case-sensitive
// on purpose — operators pin an exact marker string.
func evalKeyword(body, keyword string, invert bool) (up bool, msg string) {
	contains := strings.Contains(body, keyword)
	if invert {
		if contains {
			return false, fmt.Sprintf("keyword %q present (expected absent)", keyword)
		}
		return true, fmt.Sprintf("keyword %q absent as expected", keyword)
	}
	if contains {
		return true, fmt.Sprintf("keyword %q found", keyword)
	}
	return false, fmt.Sprintf("keyword %q not found in response body", keyword)
}

// NormalizeAddr turns an operator-typed target into a canonical host:port for
// net.Dial / tls.Dial. Tolerates a pasted URL (scheme + path stripped) and
// appends defaultPort when no port is present. A defaultPort of "" means the
// port is mandatory (tcp monitors — there is no sane default port to guess).
// Validates the port is a number in 1..65535 so a typo fails at create/update
// time rather than every 60s in the probe loop.
func NormalizeAddr(target, defaultPort string) (string, error) {
	t := strings.TrimSpace(target)
	if t == "" {
		return "", fmt.Errorf("target required")
	}
	// Strip a pasted URL scheme (https://host:443/path → host:443/path).
	if i := strings.Index(t, "://"); i >= 0 {
		t = t[i+3:]
	}
	// Drop any path / query after the authority.
	if i := strings.IndexAny(t, "/?"); i >= 0 {
		t = t[:i]
	}
	if t == "" {
		return "", fmt.Errorf("host required in target %q", target)
	}

	host, port, err := net.SplitHostPort(t)
	if err != nil {
		// No port present (or a bare IPv6 without brackets) — apply the
		// default if the caller provided one.
		if defaultPort == "" {
			return "", fmt.Errorf("port required in target %q (expected host:port)", target)
		}
		host, port = t, defaultPort
	}
	if host == "" {
		return "", fmt.Errorf("host required in target %q", target)
	}
	pn, err := strconv.Atoi(port)
	if err != nil || pn < 1 || pn > 65535 {
		return "", fmt.Errorf("invalid port %q in target %q (want 1..65535)", port, target)
	}
	return net.JoinHostPort(host, port), nil
}
