package sites

import (
	"strings"
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

func TestParseEnv(t *testing.T) {
	vars, err := ParseEnv(strings.Join([]string{
		"# a comment",
		"",
		"APP_SECRET=abc def",
		"export TOKEN='single $quoted'",
		`QUOTED="double"`,
		"EMPTY=",
		"URL=mongodb://u:p@h/db?a=b",
		"APP_SECRET=last wins",
	}, "\r\n"))
	if err != nil {
		t.Fatal(err)
	}

	want := []EnvVar{
		{"APP_SECRET", "last wins"},
		{"TOKEN", "single $quoted"},
		{"QUOTED", "double"},
		{"EMPTY", ""},
		{"URL", "mongodb://u:p@h/db?a=b"},
	}

	if len(vars) != len(want) {
		t.Fatalf("got %d vars, want %d: %#v", len(vars), len(want), vars)
	}

	for i := range want {
		if vars[i] != want[i] {
			t.Errorf("var %d = %#v, want %#v", i, vars[i], want[i])
		}
	}
}

func TestParseEnvRefuses(t *testing.T) {
	for _, text := range []string{
		"NO_EQUALS",
		"1ABC=x",
		"BAD-NAME=x",
		"PORT=3000",
		"host=0.0.0.0",
		"PATH=/bin",
	} {
		if _, err := ParseEnv(text); err == nil {
			t.Errorf("ParseEnv(%q) was accepted", text)
		}
	}
}

func TestRenderEnvFileEscapes(t *testing.T) {
	out := renderEnvFile([]EnvVar{{"A", `say "hi" \ $HOME`}})

	if !strings.Contains(out, `A="say \"hi\" \\ $HOME"`) {
		t.Errorf("unexpected file:\n%s", out)
	}
}

func TestAsSiteKeepsValuesOffTheCommandLine(t *testing.T) {
	site := &store.Site{EnvVars: "B_SECRET=hunter2\nA_KEY=xyz"}

	command, env := asSite(siteEnv(site), "'/var/www/x'", "npm run build")

	if strings.Contains(command, "hunter2") || strings.Contains(command, "xyz") {
		t.Errorf("a value reached the command line: %s", command)
	}

	if !strings.Contains(command, "--preserve-env=A_KEY,B_SECRET ") {
		t.Errorf("names not preserved: %s", command)
	}

	if len(env) != 2 || env[0] != "B_SECRET=hunter2" {
		t.Errorf("env = %#v", env)
	}

	plain, none := asSite(nil, "'/var/www/x'", "npm run build")
	if none != nil || strings.Contains(plain, "preserve-env") {
		t.Errorf("a site with no variables changed its command: %s", plain)
	}
}
