package security

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// The ports: what is listening on this machine, what each port is for, and
// whether the firewall lets the internet reach it. Read when the Security page
// asks — one `ss` and the `ufw status` it already reads — and nothing else.

// Port is one port and protocol as the Security page shows it.
type Port struct {
	Port        int    `json:"port"`
	Protocol    string `json:"protocol"` // tcp | udp
	Name        string `json:"name"`
	Description string `json:"description"`

	// Risk is "essential" for what keeps the server reachable, "risky" for a
	// service that should never face the internet, and "" otherwise.
	Risk string `json:"risk"`

	// Listening is whether a program answers on it; Public whether it
	// answers on an address the internet can reach rather than only on this
	// machine; Programs which programs they are.
	Listening bool     `json:"listening"`
	Public    bool     `json:"public"`
	Programs  []string `json:"programs"`

	// Open is whether a firewall rule lets everyone reach it. AllowedFrom
	// are the addresses a rule lets reach it when not everyone may.
	Open        bool     `json:"open"`
	AllowedFrom []string `json:"allowed_from"`

	// Locked is why it cannot be closed from the page, or "".
	Locked string `json:"locked"`

	// InRange is the rule number of a port range that opens it, which the
	// switch cannot split: it is closed by removing that rule.
	InRange int `json:"in_range"`
}

// portInfo is what the page says about a well-known port.
type portInfo struct {
	name, description, risk string
}

// knownPorts are described by port and protocol.
var knownPorts = map[string]portInfo{
	"20/tcp":    {"FTP data", "Carries files for FTP. FTP sends passwords unencrypted — SFTP, over SSH, does the same job safely.", "risky"},
	"21/tcp":    {"FTP", "File transfer. Sends passwords unencrypted; SFTP over SSH does the same job safely. Keep it closed unless you need it.", "risky"},
	"22/tcp":    {"SSH", "Remote command line and SFTP file transfer. How you get into the server if the panel is down.", ""},
	"25/tcp":    {"SMTP (incoming mail)", "Other mail servers deliver email to this server here. Needed only if it receives email.", ""},
	"53/tcp":    {"DNS", "Answers DNS lookups for the domains this server is the nameserver for. Large answers and zone transfers use TCP.", ""},
	"53/udp":    {"DNS", "Answers DNS lookups for the domains this server is the nameserver for.", ""},
	"67/udp":    {"DHCP server", "Hands out network addresses. A VPS should not run one facing the internet.", "risky"},
	"68/udp":    {"DHCP client", "How the server gets its own address from the hosting provider. Outgoing — the firewall does not need to open it.", ""},
	"80/tcp":    {"HTTP (websites)", "Websites over plain HTTP. Needed for Let's Encrypt certificates and to redirect visitors to HTTPS.", ""},
	"110/tcp":   {"POP3", "Downloads email to a mail app without encryption. Use 995 instead where you can.", ""},
	"111/tcp":   {"RPC portmapper", "Used by NFS file sharing. Open to the internet it leaks information and is abused for attacks.", "risky"},
	"111/udp":   {"RPC portmapper", "Used by NFS file sharing. Open to the internet it is abused to amplify attacks on others.", "risky"},
	"123/udp":   {"NTP", "Keeps the server's clock right. Outgoing — it does not need to be open to the internet.", ""},
	"143/tcp":   {"IMAP", "Mail apps read email here, encrypted with STARTTLS. 993 is the always-encrypted version.", ""},
	"443/tcp":   {"HTTPS (websites)", "Websites over HTTPS. Every site with a certificate is served here.", ""},
	"443/udp":   {"HTTPS over QUIC", "HTTP/3 for websites, when nginx has it on.", ""},
	"465/tcp":   {"SMTPS (sending mail)", "Mail apps send email through this server here, always encrypted.", ""},
	"587/tcp":   {"Submission (sending mail)", "Mail apps send email through this server here, encrypted with STARTTLS.", ""},
	"993/tcp":   {"IMAPS", "Mail apps read email here, always encrypted.", ""},
	"995/tcp":   {"POP3S", "Mail apps download email here, always encrypted.", ""},
	"1194/udp":  {"OpenVPN", "An OpenVPN server.", ""},
	"2375/tcp":  {"Docker API", "Controls Docker without a password — whoever reaches it owns the server. Never open it.", "risky"},
	"2376/tcp":  {"Docker API (TLS)", "Controls Docker. Whoever gets in owns the server; keep it closed.", "risky"},
	"3000/tcp":  {"Node.js app", "A common default port for Node.js apps. Sites normally reach apps through nginx on 80/443 instead.", ""},
	"3306/tcp":  {"MariaDB / MySQL", "The database. Sites on this server reach it locally; open it only to the addresses that need it.", "risky"},
	"3389/tcp":  {"Remote Desktop", "Windows-style remote desktop. A favourite target for password guessing.", "risky"},
	"4190/tcp":  {"ManageSieve", "Mail filter rules, used by webmail. Webmail reaches it locally; it rarely needs to be open.", ""},
	"5432/tcp":  {"PostgreSQL", "The database. Apps on this server reach it locally; open it only to the addresses that need it.", "risky"},
	"5678/tcp":  {"n8n", "The n8n automation app. Normally reached through its site on 443 instead.", ""},
	"5900/tcp":  {"VNC", "Remote screen sharing, often with a weak password. Keep it closed.", "risky"},
	"5984/tcp":  {"CouchDB", "A database. Should only be reachable from this server.", "risky"},
	"6379/tcp":  {"Redis", "A cache with no password by default. An open Redis is the most common way a VPS gets taken over.", "risky"},
	"8080/tcp":  {"HTTP (alternative)", "Often an app, a proxy or a development server.", ""},
	"8086/tcp":  {"InfluxDB", "A database. Should only be reachable from this server.", "risky"},
	"8088/tcp":  {"Ubuntu Panel (internal)", "The panel itself, behind nginx. It only answers this server.", ""},
	"9000/tcp":  {"PHP-FPM / app", "Often PHP-FPM or an app's admin. Should only be reachable from this server.", "risky"},
	"9200/tcp":  {"Elasticsearch", "A search database with no password by default. Keep it closed.", "risky"},
	"10000/tcp": {"Webmin", "Another control panel. Open only if you use it.", ""},
	"11211/tcp": {"Memcached", "A cache with no password. Keep it closed.", "risky"},
	"11211/udp": {"Memcached", "A cache with no password, abused to amplify attacks. Keep it closed.", "risky"},
	"27017/tcp": {"MongoDB", "A database, often without a password. Keep it closed.", "risky"},
	"51820/udp": {"WireGuard", "A WireGuard VPN.", ""},
}

// appProfiles are the ufw application profiles rules can be written with, and
// the ports each opens.
var appProfiles = map[string][]string{
	"openssh":             {"22/tcp"},
	"nginx full":          {"80/tcp", "443/tcp"},
	"nginx http":          {"80/tcp"},
	"nginx https":         {"443/tcp"},
	"nginx quic":          {"443/udp"},
	"postfix":             {"25/tcp"},
	"postfix smtps":       {"465/tcp"},
	"postfix submission":  {"587/tcp"},
	"dovecot imap":        {"143/tcp"},
	"dovecot secure imap": {"993/tcp"},
	"dovecot pop3":        {"110/tcp"},
	"dovecot secure pop3": {"995/tcp"},
	"bind9":               {"53/tcp", "53/udp"},
	"www":                 {"80/tcp"},
	"www secure":          {"443/tcp"},
	"www full":            {"80/tcp", "443/tcp"},
}

// listener is one line of `ss -tulnp`.
type listener struct {
	key     string
	public  bool
	program string
}

var programName = regexp.MustCompile(`\(\("([^"]+)"`)

// ReadListening asks ss what answers on which ports. Through sudo, so the
// programs of every user are named.
func readListening() []listener {
	return parseListening(proc.Run("sudo ss -tulnpH 2>/dev/null", 30*time.Second).Output)
}

func parseListening(output string) []listener {
	found := []listener{}

	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		protocol := fields[0]
		if protocol != "tcp" && protocol != "udp" {
			continue
		}

		host, port, err := net.SplitHostPort(fields[4])
		if err != nil {
			continue
		}

		number, err := strconv.Atoi(port)
		if err != nil || number < 1 {
			continue
		}

		entry := listener{key: fmt.Sprintf("%d/%s", number, protocol), public: publicListener(host)}
		if match := programName.FindStringSubmatch(strings.Join(fields[5:], " ")); match != nil {
			entry.program = match[1]
		}

		found = append(found, entry)
	}

	return found
}

// ReadPorts is every port worth showing: the ones something listens on, and
// the ones the firewall opens.
func ReadPorts(firewall Firewall, sshPorts []string, panelPort int) []Port {
	return buildPorts(readListening(), firewall, sshPorts, panelPort)
}

func buildPorts(listening []listener, firewall Firewall, sshPorts []string, panelPort int) []Port {
	ports := map[string]*Port{}

	get := func(key string) *Port {
		if port, ok := ports[key]; ok {
			return port
		}

		number, protocol, _ := strings.Cut(key, "/")
		value, _ := strconv.Atoi(number)
		port := &Port{Port: value, Protocol: protocol, Programs: []string{}, AllowedFrom: []string{}}
		ports[key] = port

		return port
	}

	for _, entry := range listening {
		port := get(entry.key)
		port.Listening = true
		port.Public = port.Public || entry.public
		if entry.program != "" && !containsText(port.Programs, entry.program) {
			port.Programs = append(port.Programs, entry.program)
		}
	}

	for _, rule := range firewall.Rules {
		if !inbound(rule) || (rule.Action != "ALLOW" && rule.Action != "LIMIT") {
			continue
		}

		keys, ranged := ruleTargets(rule)
		anywhere := fromAnywhere(rule)
		from := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rule.From), "(v6)"))

		for _, key := range keys {
			port := get(key)
			if anywhere {
				port.Open = true
			} else if from != "" && !containsText(port.AllowedFrom, from) {
				port.AllowedFrom = append(port.AllowedFrom, from)
			}
		}

		// A range is shown only for the ports in it something listens on.
		for _, r := range ranged {
			for key, port := range ports {
				number, protocol, _ := strings.Cut(key, "/")
				value, _ := strconv.Atoi(number)
				if value >= r.from && value <= r.to && (r.protocol == "" || r.protocol == protocol) {
					if anywhere {
						port.Open = true
						port.InRange = rule.Number
					} else if from != "" && !containsText(port.AllowedFrom, from) {
						port.AllowedFrom = append(port.AllowedFrom, from)
					}
				}
			}
		}
	}

	essential := map[string]string{}
	for _, port := range sshPorts {
		if port == "ssh" {
			port = "22"
		}
		essential[port+"/tcp"] = "SSH listens here — closing it could lock you out of the server."
	}
	if panelPort > 0 {
		essential[fmt.Sprintf("%d/tcp", panelPort)] = "This panel is reached on this port — closing it would lock you out of the panel."
	}

	list := make([]Port, 0, len(ports))

	for key, port := range ports {
		if info, ok := knownPorts[key]; ok {
			port.Name, port.Description, port.Risk = info.name, info.description, info.risk
		}

		if why, ok := essential[key]; ok {
			port.Locked = why
			port.Risk = "essential"
			if strings.HasPrefix(why, "This panel") {
				port.Name = "Ubuntu Panel"
				port.Description = "This control panel. Your browser reaches it on this port."
			}
			if port.Name == "" {
				port.Name = "SSH"
				port.Description = knownPorts["22/tcp"].description
			}
		}

		if port.Name == "" {
			if len(port.Programs) > 0 {
				port.Name = port.Programs[0]
				port.Description = "Opened by " + strings.Join(port.Programs, ", ") + "."
			} else {
				port.Name = "Port " + strconv.Itoa(port.Port)
				port.Description = "Opened by a firewall rule; nothing is listening on it right now."
			}
		}

		list = append(list, *port)
	}

	sort.Slice(list, func(i, j int) bool {
		if list[i].Port != list[j].Port {
			return list[i].Port < list[j].Port
		}
		return list[i].Protocol < list[j].Protocol
	})

	return list
}

func containsText(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func inbound(rule Rule) bool { return rule.Direction == "" || rule.Direction == "IN" }

func fromAnywhere(rule Rule) bool { return strings.HasPrefix(strings.TrimSpace(rule.From), "Anywhere") }

// portRange is a range a rule opens, from:to, tcp, udp or both ("").
type portRange struct {
	from, to int
	protocol string
}

// ruleTargets are the single ports a rule opens, as "port/protocol", and the
// ranges it opens. A rule on every port ("Anywhere") opens neither.
func ruleTargets(rule Rule) ([]string, []portRange) {
	to := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rule.To), "(v6)"))

	if ports, ok := appProfiles[strings.ToLower(to)]; ok {
		return ports, nil
	}

	list, protocol, _ := strings.Cut(to, "/")
	if protocol != "" && protocol != "tcp" && protocol != "udp" {
		return nil, nil
	}

	protocols := []string{"tcp", "udp"}
	if protocol != "" {
		protocols = []string{protocol}
	}

	keys := []string{}
	ranges := []portRange{}

	for _, part := range strings.Split(list, ",") {
		part = strings.TrimSpace(part)

		if low, high, isRange := strings.Cut(part, ":"); isRange {
			from, err1 := strconv.Atoi(low)
			to, err2 := strconv.Atoi(high)
			if err1 == nil && err2 == nil {
				ranges = append(ranges, portRange{from: from, to: to, protocol: protocol})
			}
			continue
		}

		if number, err := strconv.Atoi(part); err == nil && number > 0 && number <= 65535 {
			for _, p := range protocols {
				keys = append(keys, fmt.Sprintf("%d/%s", number, p))
			}
		}
	}

	return keys, ranges
}

// validProtocol is what the page's switch sends.
func validProtocol(protocol string) bool { return protocol == "tcp" || protocol == "udp" }

// OpenPort lets everyone reach a port.
func OpenPort(port int, protocol, note string) error {
	if port < 1 || port > 65535 || !validProtocol(protocol) {
		return errors.New("that is not a port")
	}

	if !commentPattern.MatchString(note) {
		note = ""
	}
	if note == "" {
		if info, ok := knownPorts[fmt.Sprintf("%d/%s", port, protocol)]; ok && commentPattern.MatchString(info.name) {
			note = info.name
		}
	}

	command := fmt.Sprintf("sudo ufw allow %d/%s", port, protocol)
	if note != "" {
		command += " comment " + proc.Quote(note)
	}

	if result := proc.Run(command+" 2>&1", time.Minute); !result.OK() {
		return fmt.Errorf("ufw refused: %s", lastLines(result.Output, 3))
	}

	return nil
}

// ClosePort removes every rule that lets everyone reach a port. A rule that
// opens other ports as well — "80,443/tcp", a profile, or a port for both
// protocols — is replaced by one for the others, so closing one port never
// closes another. Rules for particular addresses are kept.
//
// sshPorts and panelPort are refused: closing them is the one change this page
// could make that it could not then undo.
func ClosePort(port int, protocol string, sshPorts []string, panelPort int) error {
	if port < 1 || port > 65535 || !validProtocol(protocol) {
		return errors.New("that is not a port")
	}

	key := fmt.Sprintf("%d/%s", port, protocol)

	if protocol == "tcp" {
		for _, ssh := range sshPorts {
			if ssh == strconv.Itoa(port) || (ssh == "ssh" && port == 22) {
				return errors.New("SSH listens on this port; closing it could lock everybody out of the server. " +
					"Move SSH to another port first (SSH settings), then close this one")
			}
		}
		if port == panelPort {
			return errors.New("this is the port the panel is reached on; closing it would lock you out of the panel")
		}
	}

	firewall := ReadFirewall()

	numbers := []int{}
	keep := map[string]string{}

	for _, rule := range firewall.Rules {
		if !inbound(rule) || (rule.Action != "ALLOW" && rule.Action != "LIMIT") || !fromAnywhere(rule) {
			continue
		}

		keys, ranges := ruleTargets(rule)

		for _, r := range ranges {
			if port >= r.from && port <= r.to && (r.protocol == "" || r.protocol == protocol) {
				return fmt.Errorf("rule %d opens the range %d:%d, which this port is in — remove that rule "+
					"from the list of all firewall rules to close it", rule.Number, r.from, r.to)
			}
		}

		if !containsText(keys, key) {
			continue
		}

		numbers = append(numbers, rule.Number)
		for _, other := range keys {
			if other != key {
				keep[other] = rule.Comment
			}
		}
	}

	if len(numbers) == 0 {
		return nil
	}

	// What else those rules opened goes back in first, so nothing else is
	// closed even for a moment.
	others := make([]string, 0, len(keep))
	for other := range keep {
		others = append(others, other)
	}
	sort.Strings(others)

	for _, other := range others {
		number, protocol, _ := strings.Cut(other, "/")
		value, _ := strconv.Atoi(number)
		if err := OpenPort(value, protocol, keep[other]); err != nil {
			return err
		}
	}

	if len(others) > 0 {
		// The numbers have moved: delete what is still there by what it is.
		numbers = numbers[:0]
		for _, rule := range ReadFirewall().Rules {
			if !inbound(rule) || (rule.Action != "ALLOW" && rule.Action != "LIMIT") || !fromAnywhere(rule) {
				continue
			}
			keys, _ := ruleTargets(rule)
			if containsText(keys, key) {
				numbers = append(numbers, rule.Number)
			}
		}
	}

	return deleteRules(numbers)
}
