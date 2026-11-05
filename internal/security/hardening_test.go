package security

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestTheWatchScriptIsValidShell(t *testing.T) {
	inner := strings.TrimSuffix(strings.TrimPrefix(watchScript, "sudo sh -c '"), "' 2>/dev/null")
	inner = strings.ReplaceAll(inner, `'"'"'`, "'")
	if out, err := exec.Command("sh", "-n", "-c", inner).CombinedOutput(); err != nil {
		t.Fatalf("the watch script does not parse: %v\n%s", err, out)
	}
	if out, err := exec.Command("bash", "-n", "-c", watchScript).CombinedOutput(); err != nil {
		t.Fatalf("the watch command does not parse: %v\n%s", err, out)
	}
	if out, err := exec.Command("bash", "-n", "-c", processListing).CombinedOutput(); err != nil {
		t.Fatalf("the process listing does not parse: %v\n%s", err, out)
	}
}

func TestWatchFindsBackdoorsAndIgnoresThePanel(t *testing.T) {
	before := ParseWatch(strings.Join([]string{
		"user\troot\tuid=0 shell=/bin/bash home=/root",
		"user\tsite1\tuid=1001 shell=/bin/bash home=/var/www/site1",
		"cron\t/etc/crontab\taaaa",
		"service\tubuntu-panel.service\t1",
		"service\tmulti-user.target.wants/app-example.service\t2",
	}, "\n"), WatchEnv{SiteHomes: []string{"/var/www/site1"}, PanelUnits: []string{"app-example"}})

	if len(before) != 2 {
		t.Fatalf("the panel's own items were not left out: %+v", before)
	}

	after := ParseWatch(strings.Join([]string{
		"user\troot\tuid=0 shell=/bin/bash home=/root",
		"user\ttoor\tuid=0",
		"cron\t/etc/crontab\tbbbb",
		"key\troot\t1234abcd attacker@box",
		"preload\t/etc/ld.so.preload\tcccc",
	}, "\n"), WatchEnv{})

	changes := DiffWatch(before, after, time.Unix(1, 0))
	summaries := []string{}
	for _, c := range changes {
		summaries = append(summaries, c.Summary)
	}
	joined := strings.Join(summaries, "\n")

	for _, want := range []string{"root's powers: toor", "scheduled jobs in /etc/crontab were changed", "New SSH key for root",
		"rootkit"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "Removed scheduled jobs") {
		t.Errorf("an edited cron file was also reported as removed:\n%s", joined)
	}
}

func TestCodeChangesAreReported(t *testing.T) {
	before := ParseSnapshot("index.php\t100\t1700000000.5\nwp-config.php\t300\t1700000000\nold.php\t1\t1\n")
	after := ParseSnapshot("index.php\t140\t1700001000.1\nwp-config.php\t300\t1700000000.9\nwp-content/uploads/2026/x.php\t20\t1700002000\n")

	change := DiffSnapshots("example.com", before, after, time.Unix(5, 0))
	if strings.Join(change.Changed, ",") != "index.php" || strings.Join(change.Added, ",") != "wp-content/uploads/2026/x.php" ||
		strings.Join(change.Removed, ",") != "old.php" || len(change.InUploads) != 1 {
		t.Errorf("unexpected change: %+v", change)
	}

	if !DiffSnapshots("x", before, DecodeSnapshot(EncodeSnapshot(before)), time.Now()).Empty() {
		t.Error("a snapshot read back differs from itself")
	}
}

func TestLockCommandsLeaveWritableFolders(t *testing.T) {
	find := lockFind("/var/www/example.com", "-print")
	for _, want := range []string{"-mindepth 1", "-name 'uploads'", "-name 'cache'", "-name 'storage'", "-prune", "-xdev"} {
		if !strings.Contains(find, want) {
			t.Errorf("the lock's find is missing %q: %s", want, find)
		}
	}

	// Run it for real on a temporary tree.
	root := t.TempDir()
	for _, path := range []string{"index.php", "wp-content/uploads/a.jpg", "wp-content/plugins/p/p.php", "storage/logs/l.log"} {
		os.MkdirAll(root+"/"+path[:strings.LastIndex(path, "/")+1], 0o755)
		os.WriteFile(root+"/"+path, []byte("x"), 0o644)
	}
	out, err := exec.Command("sh", "-c", lockFind(root, "-print")).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	listed := string(out)
	if !strings.Contains(listed, "index.php") || !strings.Contains(listed, "p.php") ||
		strings.Contains(listed, "a.jpg") || strings.Contains(listed, "l.log") {
		t.Errorf("the lock covers the wrong files:\n%s", listed)
	}
}

func TestForwardRules(t *testing.T) {
	forwards := []Forward{
		{Protocol: "tcp", Port: 8080, ToPort: 3000},
		{Protocol: "tcp", Port: 5433, ToIP: "10.0.0.5", ToPort: 5432, Comment: "db"},
	}
	for _, f := range forwards {
		if problems := f.Validate(); len(problems) > 0 {
			t.Fatalf("%+v: %v", f, problems)
		}
	}

	rules := "# ufw\n*filter\n:ufw-before-input - [0:0]\n-A ufw-before-input -i lo -j ACCEPT\nCOMMIT\n"
	next, err := withForwards(rules, forwards)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"*nat\n", "-A PREROUTING -p tcp --dport 8080 -j REDIRECT --to-ports 3000",
		"-A PREROUTING -p tcp --dport 5433 -j DNAT --to-destination 10.0.0.5:5432 -m comment --comment \"db\"",
		"-A POSTROUTING -p tcp -d 10.0.0.5 --dport 5432 -j MASQUERADE",
		"-A ufw-before-input -p tcp --dport 3000 -m conntrack --ctstate DNAT -j ACCEPT",
	} {
		if !strings.Contains(next, want) {
			t.Errorf("missing %q:\n%s", want, next)
		}
	}
	if strings.Index(next, "*nat") > strings.Index(next, "*filter") ||
		strings.Index(next, "ufw-before-forward") > strings.LastIndex(next, "COMMIT") {
		t.Errorf("the blocks are in the wrong place:\n%s", next)
	}

	// Writing again replaces rather than adds, and none takes them all out.
	again, _ := withForwards(next, forwards)
	if again != next {
		t.Error("writing the forwards twice changed the file")
	}
	if cleared, _ := withForwards(next, nil); cleared != rules {
		t.Errorf("removing every forward did not give back the original:\n%s", cleared)
	}

	if (Forward{Protocol: "tcp", Port: 80, ToPort: 80}).Validate()["to_port"] == "" {
		t.Error("forwarding a port to itself was accepted")
	}
	if (Forward{Protocol: "tcp", Port: 1, ToIP: "2001:db8::1", ToPort: 2}).Validate()["to_ip"] == "" {
		t.Error("an IPv6 target was accepted")
	}
}

func TestPHPHardeningFile(t *testing.T) {
	file := PHPHardeningFile()
	if !strings.Contains(file, "disable_functions = exec,passthru,shell_exec,system,proc_open,popen") ||
		!strings.Contains(file, "expose_php = Off") {
		t.Errorf("unexpected file:\n%s", file)
	}
}
