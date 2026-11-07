package security

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

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
