package notfound

import (
	"testing"
	"time"
)

func TestParseLine(t *testing.T) {
	hit, ok := ParseLine("203.0.113.9\tExample.com\t/wp-login.php\tMozilla/5.0 (X11)\n")
	if !ok || hit.IP != "203.0.113.9" || hit.Host != "example.com" || hit.Path != "/wp-login.php" || hit.Agent != "Mozilla/5.0 (X11)" {
		t.Fatalf("parsed %+v, %v", hit, ok)
	}

	for _, bad := range []string{"", "nonsense", "not-an-ip\texample.com\t/x", "203.0.113.9\t_\t/x"} {
		if _, ok := ParseLine(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestKey(t *testing.T) {
	if target, value := Key("203.0.113.9"); target != "ip" || value != "203.0.113.9" {
		t.Errorf("v4: %s %s", target, value)
	}

	if target, value := Key("2001:db8:1:2:3:4:5:6"); target != "ip_range" || value != "2001:db8:1:2::/64" {
		t.Errorf("v6: %s %s", target, value)
	}
}

func TestIgnored(t *testing.T) {
	never := func(string) bool { return false }

	for _, ip := range []string{"127.0.0.1", "10.0.0.4", "192.168.1.2", "::1", "198.51.100.1"} {
		if !Ignored(ip, []string{"198.51.100.1"}, never) {
			t.Errorf("%s should be ignored", ip)
		}
	}

	if Ignored("203.0.113.9", nil, never) {
		t.Error("a public address was ignored")
	}

	if !Ignored("203.0.113.9", nil, func(string) bool { return true }) {
		t.Error("a Cloudflare address was not ignored")
	}
}

func TestCounterWindow(t *testing.T) {
	var c Counter
	start := time.Unix(1_000_000, 0)

	if n := c.Add("ip", "a.com", start); n != 1 {
		t.Fatalf("first: %d", n)
	}
	if n := c.Add("ip", "a.com", start.Add(time.Minute)); n != 2 {
		t.Fatalf("second: %d", n)
	}
	if n := c.Add("ip", "b.com", start.Add(time.Minute)); n != 1 {
		t.Fatalf("other host: %d", n)
	}
	if n := c.Add("ip", "a.com", start.Add(Window+2*time.Minute)); n != 1 {
		t.Fatalf("after the window: %d", n)
	}

	c.Forget("ip", "a.com")
	if n := c.Add("ip", "a.com", start.Add(Window+3*time.Minute)); n != 1 {
		t.Fatalf("after forgetting: %d", n)
	}
}
