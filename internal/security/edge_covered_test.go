package security

import "testing"

func TestNginxBansCovered(t *testing.T) {
	previous := NginxBansFile([]string{"203.0.113.1"})
	instant := "deny 203.0.113.2; # 1700000000\n"

	cases := []struct {
		name string
		want []string
		ok   bool
	}{
		{"an addition the instant file already denies", []string{"203.0.113.1", "203.0.113.2"}, true},
		{"an addition nothing denies yet", []string{"203.0.113.1", "203.0.113.3"}, false},
		{"an address no longer banned", []string{"203.0.113.2"}, false},
	}

	for _, c := range cases {
		if got := nginxBansCovered(previous, NginxBansFile(c.want), instant); got != c.ok {
			t.Errorf("%s: covered = %v, want %v", c.name, got, c.ok)
		}
	}
}

func TestWithNamesSkipsWhatIsNotAProgram(t *testing.T) {
	// A kernel thread has no program; /proc/self is not a process number.
	listing := "/proc/2\troot\t\n/proc/self\troot\t/usr/bin/x\nnot a line\n"

	if got := withNames(listing); got != "" {
		t.Fatalf("withNames kept a line it should not have: %q", got)
	}
}

func TestCloudflareWorkerIsNotCloudflare(t *testing.T) {
	if FromCloudflare(CloudflareWorker) || FromCloudflare("2a06:98c0:3600:0::103") {
		t.Fatal("the Workers address was treated as Cloudflare's proxy, so a ban on it is never enforced")
	}
	if !FromCloudflare("2a06:98c0:3600::104") || !FromCloudflare("162.158.1.2") {
		t.Fatal("Cloudflare's own addresses must still be trusted")
	}
	if NginxBansFile([]string{CloudflareWorker}) == "" {
		t.Fatal("nginx was not given the Workers address to refuse")
	}
}
