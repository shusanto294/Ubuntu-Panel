package mail

import (
	"strings"
	"testing"
)

// The reason the file exists: past 100 connections Dovecot drops new ones and
// clients see a reset TLS handshake. Both tiers have to lift that ceiling.
func TestBothTiersLiftTheHundredConnectionCeiling(t *testing.T) {
	for _, full := range []bool{true, false} {
		conf := dovecotCapacityConfig(4, full)

		for _, want := range []string{
			"default_client_limit = 20000\n",
			"protocol imap {\n  mail_max_userip_connections = 1000000\n}\n",
			"service imap {\n  process_limit = 20000\n",
		} {
			if !strings.Contains(conf, want) {
				t.Errorf("%s tier is missing %q:\n%s", tierName(full), want, conf)
			}
		}

		if strings.Contains(conf, "default_process_limit") {
			t.Errorf("%s tier raises every service's process limit, not just IMAP's:\n%s", tierName(full), conf)
		}
	}
}

// Full tier: a few login processes each proxy thousands of TLS sessions, and
// idle sessions are hibernated with the sockets that needs.
func TestFullTierUsesPerformanceModeAndHibernation(t *testing.T) {
	conf := dovecotCapacityConfig(4, true)

	for _, want := range []string{
		"  service_count = 0\n",
		"  process_min_avail = 4\n",
		"  process_limit = 8\n",
		"  client_limit = 5000\n",
		"imap_hibernate_timeout = 30s\n",
		"unix_listener imap-master {\n    user = $default_internal_user\n  }",
		"unix_listener imap-hibernate {\n    mode = 0660\n    group = $default_internal_group\n  }",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("full tier is missing %q:\n%s", want, conf)
		}
	}
}

// The minimal tier is the fallback for a Dovecot that refuses the full one
// (2.4 renamed service_count): it may only use settings both versions know.
func TestMinimalTierAvoidsRenamedSettings(t *testing.T) {
	conf := dovecotCapacityConfig(4, false)

	for _, renamed := range []string{"service_count", "$default_internal", "imap_hibernate", "vsz_limit"} {
		if strings.Contains(conf, renamed) {
			t.Errorf("minimal tier uses %q, which Dovecot 2.4 renamed or may not accept:\n%s", renamed, conf)
		}
	}

	if !strings.Contains(conf, "service imap-login {\n  process_limit = 20000\n}") {
		t.Errorf("minimal tier does not raise the login process limit:\n%s", conf)
	}
}

// Small machines still get enough login processes, and blocks stay balanced.
func TestSmallMachinesAndBalancedBraces(t *testing.T) {
	conf := dovecotCapacityConfig(0, true)
	if !strings.Contains(conf, "  process_min_avail = 1\n") || !strings.Contains(conf, "  process_limit = 4\n") {
		t.Errorf("a single-core machine should keep 1 login process ready and allow 4:\n%s", conf)
	}

	for _, full := range []bool{true, false} {
		c := dovecotCapacityConfig(2, full)
		if strings.Count(c, "{") != strings.Count(c, "}") {
			t.Errorf("%s tier has unbalanced braces:\n%s", tierName(full), c)
		}
	}
}

func TestDovecotGetsEnoughOpenFiles(t *testing.T) {
	if !strings.Contains(dovecotLimitsConfig(), "[Service]\nLimitNOFILE=1048576\n") {
		t.Errorf("systemd drop-in is wrong:\n%s", dovecotLimitsConfig())
	}
}

// Sending from many mailboxes through one server must not hit Postfix's
// default of 50 connections per client address.
func TestPostfixHasNoPerClientConnectionCap(t *testing.T) {
	want := map[string]string{
		"smtpd_client_connection_count_limit":     "0",
		"smtpd_client_connection_rate_limit":      "0",
		"smtpd_client_message_rate_limit":         "0",
		"smtpd_client_new_tls_session_rate_limit": "0",
		"default_process_limit":                   "1000",
	}

	got := map[string]string{}
	for _, kv := range postfixLimits {
		got[kv[0]] = kv[1]
	}

	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}
