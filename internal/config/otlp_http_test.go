package config

import "testing"

// TestResolveOTLPHTTPAddr guards the v0.9.168 dedicated OTLP/HTTP :4318 listener
// address resolution. The listener defaults to :4318 (the OTel-convention port)
// so external collectors reach OTLP/HTTP on the standard port without hitting
// the login-gated UI on :8088; an explicit env sets a custom address;
// "off"/"none"/"-" disables the listener entirely (an env var can't express an
// empty-string override otherwise). Unset MUST keep the current/default — never
// silently disable, which would drop ingest for installs that never set it.
func TestResolveOTLPHTTPAddr(t *testing.T) {
	cases := []struct {
		env     string
		current string
		want    string
	}{
		{"", ":4318", ":4318"},                    // unset → keep default
		{"", "", ""},                              // unset → keep an already-disabled value
		{":5318", ":4318", ":5318"},               // explicit custom port
		{"0.0.0.0:4318", ":4318", "0.0.0.0:4318"}, // explicit bind address
		{"off", ":4318", ""},                      // explicit disable
		{"none", ":4318", ""},                     // explicit disable (alias)
		{"-", ":4318", ""},                        // explicit disable (alias)
		{"OFF", ":4318", ""},                      // disable tokens are case-insensitive
		{" off ", ":4318", ""},                    // ...and whitespace-tolerant
		{":4318\n", ":4318", ":4318"},             // trailing newline (k8s Secret) trimmed, not fatal
		{" 0.0.0.0:4318 ", ":4318", "0.0.0.0:4318"}, // surrounding space trimmed off the address
		{"   ", ":4318", ":4318"},                 // whitespace-only → treated as unset (keep default)
	}
	for _, c := range cases {
		if got := resolveOTLPHTTPAddr(c.env, c.current); got != c.want {
			t.Errorf("resolveOTLPHTTPAddr(%q, %q) = %q, want %q", c.env, c.current, got, c.want)
		}
	}
}

// TestOTLPHTTPDefault locks the :4318 default so a future defaults-struct edit
// that drops it (silently regressing external OTLP/HTTP clients back to the
// shared :8088 UI port) fails loud.
func TestOTLPHTTPDefault(t *testing.T) {
	if defaults.Listen.OTLPHTTP != ":4318" {
		t.Errorf("defaults.Listen.OTLPHTTP = %q, want :4318", defaults.Listen.OTLPHTTP)
	}
}
