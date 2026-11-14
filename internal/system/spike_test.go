package system

import (
	"strings"
	"testing"
	"time"
)

func fakeCommand(pid int, name string) string {
	if pid == 20 || pid == 21 {
		return "php-fpm: pool example.com"
	}
	return "/usr/bin/" + name
}

// Workers of one pool are one line, named for the pool, and the shares are
// of the whole machine — two cores, so one busy core is 50%.
func TestSpikeGroupsAPoolAndSharesTheMachine(t *testing.T) {
	before := map[int]procTicks{
		20: {ticks: 1000, name: "php-fpm8.3"},
		21: {ticks: 1000, name: "php-fpm8.3"},
		30: {ticks: 500, name: "mariadbd"},
	}
	after := map[int]procTicks{
		20: {ticks: 1060, name: "php-fpm8.3"}, // 0.6 s
		21: {ticks: 1040, name: "php-fpm8.3"}, // 0.4 s
		30: {ticks: 520, name: "mariadbd"},    // 0.2 s
	}

	spike := spikeOf(before, after, time.Second, 2, 70, kernelTime{}, time.Unix(100, 0), fakeCommand, nil)

	// 60% found in the processes; the other 10% of the 70 is "Other".
	if len(spike.Top) != 3 || !spike.Top[2].Other || spike.Top[2].CPU != 10 {
		t.Fatalf("top is %+v", spike.Top)
	}

	pool := spike.Top[0]
	if pool.Name != "php-fpm: pool example.com" || pool.Processes != 2 || pool.CPU != 50 {
		t.Fatalf("pool is %+v", pool)
	}

	if db := spike.Top[1]; db.Name != "mariadbd" || db.CPU != 10 {
		t.Fatalf("database is %+v", db)
	}
}

// A program that started and exited inside the second is counted on the
// parent that waited for it, and said to be finished.
func TestSpikeCountsFinishedChildrenOnTheParent(t *testing.T) {
	before := map[int]procTicks{1: {ticks: 10, children: 0, name: "ubuntu-panel"}}
	after := map[int]procTicks{1: {ticks: 12, children: 80, name: "ubuntu-panel"}}

	spike := spikeOf(before, after, time.Second, 2, 41, kernelTime{}, time.Unix(100, 0), fakeCommand, nil)

	if len(spike.Top) != 1 || spike.Top[0].CPU != 41 || spike.Top[0].Finished != 40 {
		t.Fatalf("top is %+v", spike.Top)
	}
}

// A child that had been running a long time and was reaped in this second
// brings its whole life into the parent's children time; only what it used
// in this second may be counted.
func TestSpikeDoesNotCountALongChildsWholeLife(t *testing.T) {
	before := map[int]procTicks{
		1:  {ticks: 10, children: 0, name: "php-fpm8.3"},
		50: {ticks: 360000, parent: 1, name: "php-fpm8.3"}, // an hour
	}
	after := map[int]procTicks{
		1: {ticks: 10, children: 360010, name: "php-fpm8.3"}, // the hour, plus 0.1 s
	}

	spike := spikeOf(before, after, time.Second, 2, 55, kernelTime{}, time.Unix(100, 0), fakeCommand, nil)

	if len(spike.Top) != 2 || spike.Top[0].CPU != 5 || !spike.Top[1].Other || spike.Top[1].CPU != 50 {
		t.Fatalf("top is %+v", spike.Top)
	}
}

func sumOf(top []SpikeProcess) float64 {
	tenths := 0
	for _, line := range top {
		tenths += int(line.CPU*10 + 0.5)
	}
	return float64(tenths) / 10
}

// A process that started inside the second — the panel's minute starting a
// sudo — is counted whole, not left out for missing from the first reading.
func TestSpikeCountsAProcessStartedInTheSecond(t *testing.T) {
	before := map[int]procTicks{1: {ticks: 10, name: "ubuntu-panel"}}
	after := map[int]procTicks{
		1:  {ticks: 11, name: "ubuntu-panel"},
		90: {ticks: 50, parent: 1, name: "sudo"}, // 0.5 s, new
	}

	spike := spikeOf(before, after, time.Second, 2, 25.5, kernelTime{}, time.Unix(100, 0), fakeCommand, nil)

	if len(spike.Top) != 2 || spike.Top[0].Name != "sudo" || spike.Top[0].CPU != 25 {
		t.Fatalf("top is %+v", spike.Top)
	}
}

// The kernel's interrupts and the host's steal are lines of their own, and
// what nothing explains is "Other": the lines add up to the figure.
func TestSpikeLinesAddUpToTheFigure(t *testing.T) {
	before := map[int]procTicks{}
	after := map[int]procTicks{}
	for pid := 1; pid <= 9; pid++ {
		before[pid] = procTicks{ticks: 100, name: "p" + string(rune('0'+pid))}
		after[pid] = procTicks{ticks: 100 + uint64(pid), name: "p" + string(rune('0'+pid))}
	}

	spike := spikeOf(before, after, time.Second, 3, 27, kernelTime{interrupts: 2.2, steal: 1.1},
		time.Unix(100, 0), fakeCommand, nil)

	if got := sumOf(spike.Top); got != 27 {
		t.Fatalf("lines add up to %v: %+v", got, spike.Top)
	}

	if last := spike.Top[len(spike.Top)-1]; !last.Other {
		t.Fatalf("other is not last: %+v", spike.Top)
	}

	if len(spike.Top) != spikeTop+1 {
		t.Fatalf("top is %+v", spike.Top)
	}
}

// Lines that found more than the figure are scaled down to it.
func TestSpikeLinesNeverAddUpToMore(t *testing.T) {
	before := map[int]procTicks{1: {ticks: 0, name: "a"}, 2: {ticks: 0, name: "b"}}
	after := map[int]procTicks{1: {ticks: 60, name: "a"}, 2: {ticks: 40, name: "b"}}

	spike := spikeOf(before, after, time.Second, 1, 80, kernelTime{}, time.Unix(100, 0), fakeCommand, nil)

	if got := sumOf(spike.Top); got != 80 || spike.Top[0].CPU != 48 || spike.Top[1].CPU != 32 {
		t.Fatalf("lines add up to %v: %+v", got, spike.Top)
	}
}

func TestRoundToTotal(t *testing.T) {
	got := roundToTotal([]float64{3.33, 3.33, 3.34}, 10)
	if got[0]+got[1]+got[2] != 100 {
		t.Fatalf("rounded to %v", got)
	}
}

func TestChildTicks(t *testing.T) {
	stat := "1234 (php-fpm: pool (www)) S 77 1234 1234 0 -1 4194624 500 0 0 0 250 50 30 12 20 0 1 0 100 300000000 2560 18446744073709551615"

	children, parent := childTicks(stat)
	if children != 42 || parent != 77 {
		t.Fatalf("children %d, parent %d", children, parent)
	}
}

// The window carries the busiest second it saw, and taking it starts again.
func TestSamplerKeepsTheBusiestSpike(t *testing.T) {
	var s Sampler

	s.mu.Lock()
	s.spike = &Spike{CPU: 80}
	s.mu.Unlock()

	if window := s.Take(); window.Spike == nil || window.Spike.CPU != 80 {
		t.Fatalf("window is %+v", window)
	}

	if next := s.Take(); next.Spike != nil {
		t.Fatalf("an empty window has %+v", next.Spike)
	}
}

// A worker busy for a site is that site's, by name, with what it ran under
// it; a worker split between two sites in the second is shared between them.
func TestSpikePutsWorkersDownToTheirSites(t *testing.T) {
	before := map[int]procTicks{
		20: {ticks: 1000, name: "php-fpm8.3"},
		21: {ticks: 1000, name: "php-fpm8.3"},
		22: {ticks: 1000, name: "php-fpm8.3"},
		30: {ticks: 500, name: "mariadbd"},
	}
	after := map[int]procTicks{
		20: {ticks: 1060, name: "php-fpm8.3"}, // 0.6 s, shop.test
		21: {ticks: 1040, name: "php-fpm8.3"}, // 0.4 s, half each
		22: {ticks: 1010, name: "php-fpm8.3"}, // 0.1 s, no site known
		30: {ticks: 520, name: "mariadbd"},    // 0.2 s
	}

	sites := func(pid int) []siteShare {
		switch pid {
		case 20:
			return []siteShare{{domain: "shop.test", weight: 1}}
		case 21:
			return []siteShare{{domain: "shop.test", weight: 0.5}, {domain: "blog.test", weight: 0.5}}
		}
		return nil
	}

	command := func(pid int, name string) string {
		if pid >= 20 && pid <= 22 {
			return "php-fpm: pool www"
		}
		return name
	}

	spike := spikeOf(before, after, time.Second, 2, 65, kernelTime{}, time.Unix(100, 0), command, sites)

	byName := map[string]SpikeProcess{}
	for _, line := range spike.Top {
		byName[line.Name] = line
	}

	shop := byName["shop.test"]
	if !shop.Site || shop.CPU != 40 || shop.Programs != "PHP ×2" {
		t.Fatalf("shop is %+v", shop)
	}

	if blog := byName["blog.test"]; !blog.Site || blog.CPU != 10 || blog.Programs != "PHP" {
		t.Fatalf("blog is %+v", blog)
	}

	if pool := byName["php-fpm: pool www"]; pool.Site || pool.CPU != 5 {
		t.Fatalf("the unknown worker is %+v", pool)
	}

	if spike.Top[0].Name != "shop.test" {
		t.Fatalf("busiest first, got %+v", spike.Top)
	}
}

// A quiet second still says what it was spent on.
func TestSpikeOfAQuietSecond(t *testing.T) {
	before := map[int]procTicks{30: {ticks: 500, name: "mariadbd"}}
	after := map[int]procTicks{30: {ticks: 501, name: "mariadbd"}}

	spike := spikeOf(before, after, time.Second, 2, 0.5, kernelTime{}, time.Unix(100, 0), fakeCommand, nil)

	if len(spike.Top) != 1 || spike.Top[0].CPU != 0.5 {
		t.Fatalf("top is %+v", spike.Top)
	}
}

func TestMemoryIsBySiteThenProgram(t *testing.T) {
	procs := map[int]procTicks{
		20: {name: "php-fpm8.3", rssMB: 100},
		21: {name: "php-fpm8.3", rssMB: 100},
		22: {name: "php-fpm8.3", rssMB: 50},
		30: {name: "mariadbd", rssMB: 400},
		40: {name: "node", rssMB: 150},
	}

	sites := func(pid int) string {
		switch pid {
		case 20, 21:
			return "shop.test"
		case 40:
			return "app.test"
		}
		return ""
	}

	usage := memoryOf(procs, 1000, time.Unix(100, 0), sites)

	if len(usage.Top) != 4 {
		t.Fatalf("top is %+v", usage.Top)
	}

	if first := usage.Top[0]; first.Name != "mariadbd" || first.Percent != 40 || first.Site {
		t.Fatalf("first is %+v", first)
	}

	if shop := usage.Top[1]; shop.Name != "shop.test" || !shop.Site || shop.Percent != 20 ||
		shop.Detail != "PHP ×2" || shop.Bytes != 200<<20 {
		t.Fatalf("shop is %+v", shop)
	}

	if app := usage.Top[2]; app.Name != "app.test" || app.Detail != "Node.js" {
		t.Fatalf("app is %+v", app)
	}
}

func TestSiteIndexFindsTheDeepestRoot(t *testing.T) {
	index := newSiteIndex([]SiteRoot{
		{Domain: "example.com", Root: "/var/www/example.com/"},
		{Domain: "shop.example.com", Root: "/var/www/example.com/shop"},
		{Domain: "broken", Root: "/"},
	})

	cases := map[string]string{
		"/var/www/example.com":                 "example.com",
		"/var/www/example.com/public/wp-admin": "example.com",
		"/var/www/example.com/shop/public":     "shop.example.com",
		"/var/www/example.community":           "",
		"/root":                                "",
	}

	for directory, want := range cases {
		if got := index.site(directory); got != want {
			t.Errorf("%s is %q, want %q", directory, got, want)
		}
	}
}

func TestSiteSharesSplitBySightings(t *testing.T) {
	index := newSiteIndex([]SiteRoot{{Domain: "a.test", Root: "/var/www/a"}, {Domain: "b.test", Root: "/var/www/b"}})

	shares := siteShares(map[string]int{"/var/www/a/public": 3, "/var/www/b": 1, "/tmp": 5}, index)

	if len(shares) != 2 || shares[0].domain != "a.test" || shares[0].weight != 0.75 || shares[1].weight != 0.25 {
		t.Fatalf("shares are %+v", shares)
	}

	if none := siteShares(map[string]int{"/var/lib/mysql": 10}, index); none != nil {
		t.Fatalf("no site, got %+v", none)
	}
}

func TestCWDFeedReadsSeconds(t *testing.T) {
	var feed cwdFeed

	feed.read(strings.NewReader("20\t3\t/var/www/a\n20\t1\t/var/www/b\n\n21\t10\t/var/www/c\n"))

	latest := feed.snapshot()
	if len(latest) != 1 || latest[20]["/var/www/a"] != 3 || latest[20]["/var/www/b"] != 1 {
		t.Fatalf("latest is %+v", latest)
	}
}

func TestParseDu(t *testing.T) {
	sizes := parseDu("12345\t/var/www/a\n999\t/var/log\nnot a line\n")

	if sizes["/var/www/a"] != 12345 || sizes["/var/log"] != 999 || len(sizes) != 2 {
		t.Fatalf("sizes are %+v", sizes)
	}
}
