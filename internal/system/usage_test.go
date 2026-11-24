package system

import "testing"

func TestServiceFromCgroup(t *testing.T) {
	cases := map[string]string{
		"0::/system.slice/nginx.service\n":                         "nginx",
		"0::/system.slice/ubuntu-panel-shop-example-com.service\n": "ubuntu-panel-shop-example-com",
		"0::/user.slice/user-1000.slice/session-3.scope\n":         "",
		"0::/\n": "",
		"12:pids:/system.slice/mysql.service\n1:name=systemd:/system.slice/mysql.service\n": "mysql",
	}
	for in, want := range cases {
		if got := serviceFromCgroup(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestUsageLabel(t *testing.T) {
	sites := map[string]string{"ubuntu-panel-shop-example-com": "shop.example.com"}
	cases := []struct {
		p    usageProc
		want string
		site bool
	}{
		{usageProc{name: "node", unit: "ubuntu-panel-shop-example-com"}, "shop.example.com", true},
		{usageProc{name: "php-fpm8.3", unit: "php8.3-fpm"}, "PHP 8.3 (PHP sites)", false},
		{usageProc{name: "mysqld", unit: "mysql"}, "MySQL", false},
		{usageProc{name: "ubuntu-panel", unit: "ubuntu-panel"}, "This panel", false},
		{usageProc{name: "bash"}, "bash", false},
		{usageProc{name: "\x00kernel"}, "Linux kernel", false},
	}
	for _, c := range cases {
		got, site := usageLabel(c.p, sites)
		if got != c.want || site != c.site {
			t.Errorf("%+v: got %q %v", c.p, got, site)
		}
	}
}

func TestParseUsageStat(t *testing.T) {
	// pid (comm) state ppid ... utime=14 stime=15 ... starttime=22 vsize rss=24
	stat := "42 (php-fpm: pool (www)) S 1 42 42 0 -1 4194560 100 0 0 0 250 50 0 0 20 0 1 0 9999 1000 2560"
	p, ok := parseUsageStat(stat, 4.0/1024)
	if !ok || p.ticks != 300 || p.start != "9999" || p.name != "php-fpm: pool (www)" || p.rssMB != 10 {
		t.Fatalf("got %+v %v", p, ok)
	}

	kernel := "7 (kworker/0:1) I 2 0 0 0 -1 0 0 0 0 0 1 2 0 0 20 0 1 0 5 0 0"
	if p, _ := parseUsageStat(kernel, 1); p.name != "\x00kernel" {
		t.Errorf("kernel thread read as %q", p.name)
	}
}

func TestSpikeShares(t *testing.T) {
	// Over 10s, mysql ran at its usual 2 ticks/s; php jumped well above its 1.
	groups := map[string]*usageGroup{
		"MySQL": {share: UsageShare{Name: "MySQL"}, ticks: 20},
		"PHP":   {share: UsageShare{Name: "PHP"}, ticks: 310},
		"idle":  {share: UsageShare{Name: "idle"}},
	}
	got := spikeShares(groups, map[string]float64{"MySQL": 2, "PHP": 1}, 10, 60)
	if len(got) != 1 || got[0].Name != "PHP" || got[0].Percent != 60 || !got[0].Spike {
		t.Fatalf("got %+v", got)
	}

	// More than usageTop lines: the rest is one line, and all of it adds up.
	groups = map[string]*usageGroup{}
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		groups[n] = &usageGroup{share: UsageShare{Name: n}, ticks: 10}
	}
	got = spikeShares(groups, nil, 1, 70)
	sum := 0.0
	for _, s := range got {
		sum += s.Percent
	}
	if len(got) != usageTop+1 || got[usageTop].Name != "Everything else" || sum != 70 {
		t.Fatalf("got %+v sum %v", got, sum)
	}

	if spikeShares(map[string]*usageGroup{"x": {ticks: 5}}, map[string]float64{"x": 5}, 1, 50) != nil {
		t.Fatal("background alone should name nothing")
	}
}
