package sites

// A Node site's environment variables.
//
// Next.js, Nuxt and plain Node apps all read their configuration — database
// URLs, signing secrets, API keys — from the environment, and none of it is in
// the repository, because none of it should be. The panel had nowhere to put
// it: a .env dropped into the site directory by hand survived a pull and was
// deleted by the next redeploy, which clones into an empty directory. A build
// that validates its configuration then failed with "Invalid environment
// configuration", and a running app that reads a secret failed on first use.
//
// The variables are stored encrypted in the panel's database and reach the
// app two ways:
//
//   - At run time, through a file systemd reads (EnvironmentFile=), kept under
//     /var/lib/ubuntu-panel rather than in the site directory: not in the
//     repository's working tree, not in a backup of the site, not deleted by a
//     redeploy, and readable by root alone — systemd reads it before dropping
//     to www-data.
//
//   - At install and build time, through the environment of the npm commands
//     (proc.RunEnv), never their command line — so they are not in `ps`, not
//     echoed into the deployment console, and not in a failure's summary.
//
// Real environment variables outrank every .env file Next.js, Nuxt and Vite
// load, so what is set here wins over anything a repository commits.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/tasks"
)

// EnvRoot is where the files systemd reads live.
const EnvRoot = "/var/lib/ubuntu-panel/site-env"

// EnvFilePath is one site's file.
func EnvFilePath(site *store.Site) string {
	return fmt.Sprintf("%s/site-%d.env", EnvRoot, site.ID)
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reservedEnv are names the panel sets itself. PORT and HOST are how nginx
// reaches the app — overriding them is a site that starts and then answers on
// a port nothing proxies to — and PATH and HOME are what the build runs with.
var reservedEnv = map[string]string{
	"PORT": "the panel assigns the port nginx proxies to",
	"HOST": "the app has to listen on 127.0.0.1 for nginx to reach it",
	"PATH": "the build and the service need the system PATH",
	"HOME": "npm needs the home directory the panel gives it",
}

// EnvVar is one variable.
type EnvVar struct {
	Name  string
	Value string
}

// ParseEnv reads KEY=value lines, the way a .env file is written: blank lines
// and # comments are skipped, `export ` in front is allowed, and a value in
// matching single or double quotes has them removed. A name given twice keeps
// its last value, as a .env file would.
func ParseEnv(text string) ([]EnvVar, error) {
	values := map[string]string{}
	order := []string{}

	for number, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))

		name, value, found := strings.Cut(line, "=")
		name = strings.TrimSpace(name)

		if !found {
			return nil, fmt.Errorf("line %d has no = in it. Write each variable as NAME=value", number+1)
		}

		if !envName.MatchString(name) {
			return nil, fmt.Errorf("line %d: %q is not a variable name — letters, digits and _ only, "+
				"not starting with a digit", number+1, name)
		}

		if why, reserved := reservedEnv[strings.ToUpper(name)]; reserved {
			return nil, fmt.Errorf("line %d: %s is set by the panel (%s)", number+1, name, why)
		}

		value = strings.TrimSpace(value)

		if len(value) >= 2 {
			first, last := value[0], value[len(value)-1]

			if (first == '"' || first == '\'') && first == last {
				value = value[1 : len(value)-1]
			}
		}

		if strings.ContainsAny(value, "\x00") {
			return nil, fmt.Errorf("line %d: %s has a character a variable cannot hold", number+1, name)
		}

		if _, seen := values[name]; !seen {
			order = append(order, name)
		}

		values[name] = value
	}

	vars := make([]EnvVar, 0, len(order))

	for _, name := range order {
		vars = append(vars, EnvVar{Name: name, Value: values[name]})
	}

	return vars, nil
}

// siteEnv is the site's variables, or none when what is stored no longer
// parses — which cannot happen through the form, and is not worth failing a
// deployment over if it somehow did.
func siteEnv(site *store.Site) []EnvVar {
	vars, err := ParseEnv(site.EnvVars)
	if err != nil {
		return nil
	}

	return vars
}

// renderEnvFile is the file in systemd's EnvironmentFile syntax: every value
// double-quoted, with the two characters that are special inside the quotes
// escaped. systemd does no $-expansion in this file, so nothing else is.
func renderEnvFile(vars []EnvVar) string {
	var out strings.Builder

	out.WriteString("# Managed by Ubuntu Panel — edit on the site's Environment tab, not here.\n")

	for _, v := range vars {
		value := strings.ReplaceAll(v.Value, `\`, `\\`)
		value = strings.ReplaceAll(value, `"`, `\"`)

		fmt.Fprintf(&out, "%s=\"%s\"\n", v.Name, value)
	}

	return out.String()
}

// WriteEnvFile puts the site's variables where its service reads them, or
// removes the file when there are none.
func WriteEnvFile(site *store.Site) error {
	path := EnvFilePath(site)
	vars := siteEnv(site)

	if len(vars) == 0 {
		proc.Ok("sudo rm -f " + proc.Quote(path))

		return nil
	}

	// The directory first, and root's alone, so the file is unreachable even
	// in the moment between it being copied in and its mode being narrowed.
	if result := proc.Run("sudo install -d -o root -g root -m 700 "+proc.Quote(EnvRoot),
		time.Minute); !result.OK() {
		return fmt.Errorf("could not prepare %s: %s", EnvRoot, result.Output)
	}

	if err := proc.PutFile(path, renderEnvFile(vars), true); err != nil {
		return err
	}

	if result := proc.Run("sudo chown root:root "+proc.Quote(path)+" && sudo chmod 600 "+
		proc.Quote(path), time.Minute); !result.OK() {
		return fmt.Errorf("could not protect %s: %s", path, result.Output)
	}

	return nil
}

// RemoveEnvFile takes a site's file away with the site.
func RemoveEnvFile(site *store.Site) {
	proc.Ok("sudo rm -f " + proc.Quote(EnvFilePath(site)))
}

// asSite is the shell for running tail in the site's directory as www-data,
// with the site's variables handed through, and the environment that carries
// their values.
//
// sudo resets the environment, so the names are listed for --preserve-env and
// the values travel in the environment of the shell that runs sudo. The panel's
// sudoers rule matches ALL, which is what permits keeping them.
func asSite(site *store.Site, root, tail string) (string, []string) {
	vars := siteEnv(site)

	if len(vars) == 0 {
		return fmt.Sprintf("cd %s && sudo -u www-data HOME=/tmp %s", root, tail), nil
	}

	names := make([]string, 0, len(vars))
	env := make([]string, 0, len(vars))

	for _, v := range vars {
		names = append(names, v.Name)
		env = append(env, v.Name+"="+v.Value)
	}

	sort.Strings(names)

	return fmt.Sprintf("cd %s && sudo --preserve-env=%s -u www-data HOME=/tmp %s",
		root, strings.Join(names, ","), tail), env
}

// siteCommand is a step that runs tail as the site, with its variables.
//
// The site is read when the step runs rather than when the list is built, so
// variables saved while a deployment was queued are the ones it builds with.
func (s *Service) siteCommand(ctx context.Context, siteID int64, root, tail string) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		site, err := s.Store.FindSite(ctx, siteID)
		if err != nil {
			return "", errors.New("site vanished")
		}

		command, env := asSite(site, root, tail)

		if len(env) > 0 {
			runner.Writef("With the site's %d environment variable(s).\n", len(env))
		}

		return "", runner.MustEnv(command, proc.DefaultTimeout, env)
	}
}

// refreshServiceEnv writes the site's variables and its unit, so the restart
// that follows starts the app with them.
func (s *Service) refreshServiceEnv(ctx context.Context, siteID int64) func(*tasks.Runner) (string, error) {
	return func(runner *tasks.Runner) (string, error) {
		site, err := s.Store.FindSite(ctx, siteID)
		if err != nil {
			return "", errors.New("site vanished")
		}

		if err := ApplyEnv(site); err != nil {
			return "", err
		}

		if n := len(siteEnv(site)); n > 0 {
			return fmt.Sprintf("%d environment variable(s) in place for the service.", n), nil
		}

		return "No environment variables set for this site.", nil
	}
}

// ApplyEnv writes the site's variables and makes sure its unit reads them.
// It does not restart the service; the caller decides when.
func ApplyEnv(site *store.Site) error {
	if err := WriteEnvFile(site); err != nil {
		return err
	}

	unit := RenderUnit(site)

	if current, err := proc.ReadFile(UnitPath(site), true); err == nil && current == unit {
		return nil
	}

	if !proc.Exists(UnitPath(site), true) {
		// Not deployed yet: the deployment writes the unit itself.
		return nil
	}

	if err := proc.PutFile(UnitPath(site), unit, true); err != nil {
		return err
	}

	if result := proc.Run("sudo systemctl daemon-reload", time.Minute); !result.OK() {
		return fmt.Errorf("could not reload systemd: %s", result.Output)
	}

	return nil
}
