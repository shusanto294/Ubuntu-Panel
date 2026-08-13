package sites

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shusanto294/ubuntu-panel-go/internal/config"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
)

func nextcloud() *store.Site {
	return &store.Site{
		ID:           2,
		Domain:       "cloud.example.com",
		Type:         "nextcloud",
		RootPath:     "/var/www/cloud.example.com",
		WebDirectory: "/public",
		PHPVersion:   "8.3",
	}
}

// Nextcloud's clients find the server through these redirects, and its own
// setup check complains until they are there. A generic PHP vhost has none of
// them, which is a Nextcloud that logs in and then never syncs.
func TestNextcloudVhostServesWhatItsClientsLookFor(t *testing.T) {
	vhost := RenderVhost(nextcloud(), false, true)

	for _, needed := range []string{
		"location = /.well-known/carddav { return 301 /remote.php/dav/; }",
		"location = /.well-known/caldav  { return 301 /remote.php/dav/; }",
		"location /remote { return 301 /remote.php$request_uri; }",
		"fastcgi_param PATH_INFO $path_info;",
		"fastcgi_pass unix:/run/php/php8.3-fpm.sock;",
	} {
		if !strings.Contains(vhost, needed) {
			t.Errorf("the Nextcloud vhost is missing %q", needed)
		}
	}

	// Everybody's files are one bad location block away from being
	// downloadable.
	if !strings.Contains(vhost, "|templates|data)(?:$|/) { return 404; }") {
		t.Error("the Nextcloud vhost does not refuse its own data and config directories")
	}

	// The ACME challenge is served by every site on port 80; a `^~ /.well-known`
	// block that swallowed it would mean no certificate could ever be issued.
	if !strings.Contains(vhost, "location /.well-known/acme-challenge    { try_files $uri $uri/ =404; }") {
		t.Error("the Nextcloud vhost swallows the ACME challenge")
	}
}

// The data directory is beside the document root, never inside it.
func TestNextcloudKeepsItsDataOutOfTheDocumentRoot(t *testing.T) {
	site := nextcloud()

	if !strings.HasPrefix(site.DocumentRoot(), site.RootPath+"/") {
		t.Fatalf("document root %q is not below %q", site.DocumentRoot(), site.RootPath)
	}

	data := site.RootPath + "/data"

	if strings.HasPrefix(data, site.DocumentRoot()+"/") {
		t.Fatalf("the data directory %q is inside the document root %q", data, site.DocumentRoot())
	}
}

// n8n's editor talks to itself over a websocket, and OpenClaw's gateway
// streams. A proxy without the upgrade headers is an editor that says
// "connection lost" on a site that is running perfectly.
func TestManagedNodeAppsAreProxiedWithUpgradeHeaders(t *testing.T) {
	for _, kind := range []string{"n8n", "openclaw"} {
		site := &store.Site{
			ID: 3, Domain: kind + ".example.com", Type: kind,
			RootPath: "/var/www/" + kind + ".example.com", AppPort: 3001,
		}

		vhost := RenderVhost(site, false, true)

		for _, needed := range []string{
			"upstream " + site.ServiceName() + "_upstream {",
			"server 127.0.0.1:3001;",
			"proxy_set_header Upgrade $http_upgrade;",
			"proxy_set_header X-Forwarded-Proto $scheme;",
		} {
			if !strings.Contains(vhost, needed) {
				t.Errorf("the %s vhost is missing %q", kind, needed)
			}
		}
	}
}

// OpenClaw takes its port from --port before it looks at the environment, and
// falls back to 18789 — which nginx does not proxy to. A gateway on the wrong
// port is a 502 on a service that is running perfectly.
func TestOpenClawUnitCarriesThePortOnTheCommandLine(t *testing.T) {
	site := &store.Site{
		ID: 5, Domain: "agent.example.com", Type: "openclaw",
		RootPath: "/var/www/agent.example.com", AppPort: 3007,
		StartCommand: "openclaw gateway run",
	}

	unit := RenderUnit(site)

	if !strings.Contains(unit, "openclaw gateway run --port 3007") {
		t.Errorf("the unit does not pass the port on the command line:\n%s", unit)
	}

	// An operator who put the flag there themselves does not get a second one.
	site.StartCommand = "openclaw gateway run --port 4000"

	if strings.Count(RenderUnit(site), "--port") != 1 {
		t.Error("the panel added a second --port to a command that already had one")
	}
}

// An application that took a port of its own is followed rather than argued
// with — but only when there is one port to follow.
func TestTheAnsweringPortIsReadFromTheListeningSockets(t *testing.T) {
	// The same port on both address families is one port.
	both := `LISTEN 0 511 127.0.0.1:18789 0.0.0.0:* users:(("node",pid=1234,fd=21))
LISTEN 0 511    [::1]:18789     [::]:* users:(("node",pid=1234,fd=22))`

	if got := answeringPort(both); got != 18789 {
		t.Errorf("answeringPort = %d, want 18789", got)
	}

	// Two different ports is not a choice the panel should make silently.
	two := `LISTEN 0 511 127.0.0.1:18789 0.0.0.0:* users:(("node",pid=1234,fd=21))
LISTEN 0 511 127.0.0.1:9229  0.0.0.0:* users:(("node",pid=1234,fd=22))`

	if got := answeringPort(two); got != 0 {
		t.Errorf("answeringPort picked %d out of two candidates", got)
	}

	for _, nothing := range []string{
		"", "the process has no listening socket at all", "the service has no running process",
	} {
		if got := answeringPort(nothing); got != 0 {
			t.Errorf("answeringPort(%q) = %d, want 0", nothing, got)
		}
	}
}

// The gateway compares browser origins literally and ships knowing about
// localhost only, so the dashboard is refused on the domain it is published on
// until the site's own origin is in the list — scheme included, aliases too.
func TestOpenClawAllowsTheOriginsItsDashboardIsServedFrom(t *testing.T) {
	site := &store.Site{
		ID: 7, Domain: "agent.example.com", Type: "openclaw",
		Aliases: []string{"www.agent.example.com"},
		SSL:     true,
	}

	origins := openClawOrigins(site)

	if len(origins) != 2 || origins[0] != "https://agent.example.com" ||
		origins[1] != "https://www.agent.example.com" {
		t.Fatalf("origins = %v", origins)
	}

	if got := originsJSON(origins); got !=
		`["https://agent.example.com","https://www.agent.example.com"]` {
		t.Errorf("originsJSON = %s", got)
	}

	// Before the certificate is issued the dashboard really is on http, and an
	// https origin would be the one thing that does not match.
	site.SSL = false

	if got := openClawOrigins(site)[0]; got != "http://agent.example.com" {
		t.Errorf("origin without a certificate = %s, want http://agent.example.com", got)
	}
}

// A configuration OpenClaw objects to is not something restarting fixes: it
// exits 78 ten seconds after every start, and the unit that relaunches it
// anyway burned a core for three minutes on the first site that hit this.
func TestOpenClawUnitStopsRestartingOnAConfigurationError(t *testing.T) {
	site := &store.Site{
		ID: 6, Domain: "agent.example.com", Type: "openclaw",
		RootPath: "/var/www/agent.example.com", AppPort: 3009,
		StartCommand: "openclaw gateway",
	}

	if !strings.Contains(RenderUnit(site), "RestartPreventExitStatus=78") {
		t.Error("the OpenClaw unit restarts for ever on a configuration error")
	}

	// Everything else still restarts on anything: a Node app that exits 78 is
	// exiting for its own reasons, not sysexits' ones.
	site.Type = "nodejs"

	if strings.Contains(RenderUnit(site), "RestartPreventExitStatus") {
		t.Error("a plain Node app was given OpenClaw's exit-code rule")
	}
}

// Every proxied type needs something to run, and every managed type needs the
// deployment to know what to install. A new entry that forgets one of those
// fails here rather than halfway through a deployment.
func TestSiteTypeTableIsSelfConsistent(t *testing.T) {
	for _, kind := range config.SiteTypes {
		if kind.Proxied && kind.Start == "" {
			t.Errorf("%s is proxied but has no start command", kind.Key)
		}

		if kind.Description == "" {
			t.Errorf("%s has no description for the create form", kind.Key)
		}

		if kind.Runtime != "node" && kind.NodeMinimum != 0 {
			t.Errorf("%s is not a Node.js type but names a minimum Node version", kind.Key)
		}
	}
}

// The managed applications are the ones the panel installs itself, so a
// repository would be deployed over them.
func TestManagedTypesAreTheOnesThePanelInstalls(t *testing.T) {
	managed := map[string]bool{}

	for _, kind := range config.SiteTypes {
		if kind.Managed {
			managed[kind.Key] = true
		}
	}

	for _, key := range []string{"wordpress", "nextcloud", "n8n", "openclaw"} {
		if !managed[key] {
			t.Errorf("%s should be a managed application", key)
		}
	}

	for _, key := range []string{"php", "static", "nodejs", "nextjs", "laravel"} {
		if managed[key] {
			t.Errorf("%s is deployed from a repository and should not be managed", key)
		}
	}
}

// The gateway has no login of its own and its dashboard asks for a token, so
// nginx authenticates instead and tells the gateway who it let through. The
// header is set rather than forwarded: a visitor must not be able to name
// themselves.
func TestNoSiteIsPutBehindAPanelLogin(t *testing.T) {
	site := &store.Site{
		ID: 8, Domain: "agent.example.com", Type: "openclaw",
		RootPath: "/var/www/agent.example.com", AppPort: 3011, SSL: true,
	}

	vhost := RenderVhost(site, true, true)

	// OpenClaw authenticates for itself. The panel asking first meant the
	// gateway could not hold the token its own mobile pairing needs.
	for _, unwanted := range []string{
		"auth_basic",
		"auth_basic_user_file",
		"X-Auth-Request-Email",
	} {
		if strings.Contains(vhost, unwanted) {
			t.Errorf("the OpenClaw vhost still carries %q", unwanted)
		}
	}

	// And the proxy is otherwise the same one every proxied site gets.
	for _, needed := range []string{
		"proxy_pass http://ubuntu-panel-agent-example-com_upstream;",
		"proxy_set_header Upgrade $http_upgrade;",
	} {
		if !strings.Contains(vhost, needed) {
			t.Errorf("the OpenClaw vhost is missing %q", needed)
		}
	}
}

// The token is the whole of what stands in front of the gateway now, so it has
// to reach it. It was deliberately withheld while nginx did the
// authenticating: trusted-proxy mode and a shared token together are refused
// at startup.
func TestOpenClawIsGivenItsOwnToken(t *testing.T) {
	site := &store.Site{
		ID: 9, Domain: "agent.example.com", Type: "openclaw",
		RootPath: "/var/www/agent.example.com", AppPort: 3013,
	}

	environment := openClawEnvironment(site, "s3cret-token")

	found := ""

	for _, pair := range environment {
		if pair[0] == "OPENCLAW_GATEWAY_TOKEN" {
			found = pair[1]
		}
	}

	if found != "s3cret-token" {
		t.Fatalf("the gateway environment carries token %q", found)
	}
}

// runOpenClawConfigScript runs the merge script the deployment runs, on a
// throwaway config file, and returns what it wrote.
//
// The script is executed rather than read: the keys in it are the whole of
// what makes an OpenClaw dashboard open without asking for a token, and a
// test that only grepped the source would pass just as happily on a typo.
func runOpenClawConfigScript(t *testing.T, existing string) map[string]any {
	t.Helper()

	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not on this machine; the deployment runs the script on the server")
	}

	path := filepath.Join(t.TempDir(), "openclaw.json")

	if existing != "" {
		if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	command := exec.Command("python3", "-c", openClawConfigScript)
	command.Env = append(os.Environ(),
		"CONFIG="+path,
		`ORIGINS=["https://openclaw.example.com"]`,
		"PORT=18801",
		"TOKEN=s3cret-token")

	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("the script failed: %v\n%s", err, output)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var config map[string]any

	if err := json.Unmarshal(written, &config); err != nil {
		t.Fatalf("the script did not write valid JSON: %v\n%s", err, written)
	}

	gateway, ok := config["gateway"].(map[string]any)
	if !ok {
		t.Fatalf("no gateway section:\n%s", written)
	}

	return gateway
}

// The gateway does its own authenticating. The panel sets the token and the
// three keys that follow from running behind this panel at all — mode, bind
// and port — and nothing about how anybody is let in.
func TestOpenClawKeepsItsOwnAuthentication(t *testing.T) {
	gateway := runOpenClawConfigScript(t, "")

	auth, ok := gateway["auth"].(map[string]any)
	if !ok {
		t.Fatalf("no auth section: %v", gateway)
	}

	if auth["token"] != "s3cret-token" {
		t.Errorf("the gateway was not given its token: %v", auth["token"])
	}

	if _, present := auth["mode"]; present {
		t.Errorf("the panel set an auth mode of its own: %v", auth["mode"])
	}

	if _, present := auth["trustedProxy"]; present {
		t.Error("the panel is still authenticating on the gateway's behalf")
	}

	control, ok := gateway["controlUi"].(map[string]any)
	if !ok {
		t.Fatalf("no controlUi section: %v", gateway)
	}

	// The break-glass switch the old arrangement needed. Device pairing is the
	// gateway's own, and works over the HTTPS these sites are served on.
	if _, present := control["dangerouslyDisableDeviceAuth"]; present {
		t.Error("device authentication is still disabled")
	}

	if gateway["mode"] != "local" || gateway["bind"] != "loopback" {
		t.Errorf("the gateway is not local on loopback: %v", gateway)
	}
}

// A site deployed by the panel that authenticated on the gateway's behalf has
// trusted-proxy in its file. Left there beside a token, the gateway refuses to
// start — once every ten seconds, for ever.
func TestOpenClawUndoesThePanelsOldAuthentication(t *testing.T) {
	gateway := runOpenClawConfigScript(t, `{
	  "gateway": {"auth": {"mode": "trusted-proxy",
	                       "trustedProxy": {"userHeader": "x-auth-request-email",
	                                        "allowLoopback": true}},
	              "controlUi": {"dangerouslyDisableDeviceAuth": true}}
	}`)

	auth := gateway["auth"].(map[string]any)

	if _, present := auth["mode"]; present {
		t.Errorf("trusted-proxy survived alongside the token: %v", auth["mode"])
	}

	if _, present := auth["trustedProxy"]; present {
		t.Error("the trusted-proxy settings survived")
	}

	if _, present := gateway["controlUi"].(map[string]any)["dangerouslyDisableDeviceAuth"]; present {
		t.Error("device authentication is still disabled from the old deployment")
	}
}

// Everything the operator configured has to stay — including a token they
// chose themselves, which a redeploy must not swap for the panel's.
func TestOpenClawKeepsTheRestOfTheConfiguration(t *testing.T) {
	gateway := runOpenClawConfigScript(t, `{
	  "gateway": {"auth": {"token": "chosen-by-hand"},
	              "controlUi": {"allowedOrigins": ["https://kept.example.com"]}},
	  "channels": {"whatsapp": {"enabled": true}}
	}`)

	auth := gateway["auth"].(map[string]any)

	if auth["token"] != "chosen-by-hand" {
		t.Errorf("the operator's own token was overwritten: %v", auth["token"])
	}

	origins := gateway["controlUi"].(map[string]any)["allowedOrigins"].([]any)

	if len(origins) != 2 || origins[0] != "https://kept.example.com" {
		t.Errorf("the operator's own origin was not kept: %v", origins)
	}
}

// The link `openclaw dashboard` prints is for somebody at the machine's own
// terminal: it names the loopback address the gateway is bound to, which is
// nothing a browser anywhere else can open. Everything else about it — the
// path, the token, the parameters — is exactly what is needed.
func TestOpenClawDashboardLinkIsPointedAtTheSite(t *testing.T) {
	site := &store.Site{
		ID: 11, Domain: "agent.example.com", Type: "openclaw",
		RootPath: "/var/www/agent.example.com", AppPort: 18801, SSL: true,
	}

	output := "Gateway is running.\n" +
		"Open this in a browser:\n" +
		"  http://127.0.0.1:18801/dashboard?token=abc123&gateway=ws%3A%2F%2F127.0.0.1%3A18801\n"

	link := OpenClawDashboardURL(site, output)

	if !strings.HasPrefix(link, "https://agent.example.com/dashboard?") {
		t.Fatalf("the link was not pointed at the site: %s", link)
	}

	if !strings.Contains(link, "token=abc123") {
		t.Errorf("the token was lost: %s", link)
	}

	// The parameters carry an address of their own, at the same loopback port.
	if strings.Contains(link, "127.0.0.1") || strings.Contains(link, "18801") {
		t.Errorf("a loopback address survived inside the link: %s", link)
	}

	// A browser on an https page will not open a ws:// socket.
	if !strings.Contains(link, "wss%3A%2F%2Fagent.example.com") {
		t.Errorf("the WebSocket address was left insecure: %s", link)
	}
}

func TestOpenClawDashboardLinkOnAPlainSite(t *testing.T) {
	site := &store.Site{
		ID: 12, Domain: "agent.example.com", Type: "openclaw",
		RootPath: "/var/www/agent.example.com", AppPort: 18801,
	}

	link := OpenClawDashboardURL(site, "http://localhost:18801/?token=xyz\n")

	if link != "http://agent.example.com/?token=xyz" {
		t.Fatalf("link is %q", link)
	}

	// Nothing to send the browser to is better said by the caller, which falls
	// back to the site itself.
	if OpenClawDashboardURL(site, "command not found: openclaw\n") != "" {
		t.Error("a failed command produced a link anyway")
	}
}

// The command has to run as the user the gateway runs as, with the HOME its
// configuration lives under — those two decide which site's gateway answers.
func TestOpenClawDashboardCommandAsksThisSitesGateway(t *testing.T) {
	site := &store.Site{Domain: "agent.example.com", RootPath: "/var/www/agent.example.com"}

	command := OpenClawDashboardCommand(site)

	for _, needed := range []string{"sudo -u www-data", "HOME='/var/www/agent.example.com'",
		"openclaw dashboard --no-open"} {
		if !strings.Contains(command, needed) {
			t.Errorf("the command is missing %q: %s", needed, command)
		}
	}

	// Without --no-open the command's job is to open a browser, and on a
	// server there is none: the link never reaches stdout. The plain form is
	// the fallback for a version that predates the flag.
	if strings.Count(command, "openclaw dashboard") != 2 {
		t.Errorf("there is no fallback for a version without the flag: %s", command)
	}
}

// The link comes out of a terminal command, wrapped in the colour it writes
// for a terminal. A token with an escape sequence through it is a token the
// gateway does not recognise.
func TestOpenClawDashboardLinkIsReadThroughTerminalColour(t *testing.T) {
	site := &store.Site{
		ID: 13, Domain: "agent.example.com", Type: "openclaw",
		RootPath: "/var/www/agent.example.com", AppPort: 18801, SSL: true,
	}

	output := "\x1b[32mDashboard:\x1b[0m \x1b[4mhttp://127.0.0.1:18801/?token=abc123\x1b[0m\n"

	if link := OpenClawDashboardURL(site, output); link != "https://agent.example.com/?token=abc123" {
		t.Fatalf("link is %q", link)
	}
}

// The CLI is a client of the gateway: devices, dashboard and status all
// connect over the WebSocket and authenticate with gateway.remote.token. Set
// on one side only, the gateway refuses its own command line with "gateway
// token mismatch" — which is every command that has to ask it anything,
// including the one that prints the dashboard link.
func TestOpenClawCanTalkToItsOwnGateway(t *testing.T) {
	gateway := runOpenClawConfigScript(t, "")

	auth := gateway["auth"].(map[string]any)
	remote, ok := gateway["remote"].(map[string]any)

	if !ok {
		t.Fatalf("no remote section, so the CLI cannot authenticate: %v", gateway)
	}

	if remote["token"] != auth["token"] {
		t.Fatalf("remote token %v does not match auth token %v", remote["token"], auth["token"])
	}

	// And with a token the operator chose, the two still have to agree — it is
	// the mismatch that breaks the CLI, not which token it is.
	gateway = runOpenClawConfigScript(t, `{"gateway": {"auth": {"token": "chosen-by-hand"}}}`)

	if gateway["remote"].(map[string]any)["token"] != "chosen-by-hand" {
		t.Fatal("the operator's own token was not carried to the client half")
	}
}

// `openclaw gateway` is not the foreground form, and a unit of Type=simple
// wants the process it started to be the gateway. A site deployed before the
// catalogue said so has the older command stored against it.
func TestOpenClawUnitRunsTheGatewayInTheForeground(t *testing.T) {
	site := &store.Site{
		ID: 14, Domain: "agent.example.com", Type: "openclaw",
		RootPath: "/var/www/agent.example.com", AppPort: 18801,
		StartCommand: "openclaw gateway",
	}

	unit := RenderUnit(site)

	if !strings.Contains(unit, "openclaw gateway run --port 18801") {
		t.Fatalf("the stored command was not corrected:\n%s", unit)
	}

	// And one that already says it is left exactly as it is.
	site.StartCommand = "openclaw gateway run --port 18801"

	if strings.Count(RenderUnit(site), "gateway run") != 1 {
		t.Error("the correction was applied twice")
	}
}
