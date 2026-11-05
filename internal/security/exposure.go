package security

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// riskyPorts are services that should never answer the internet: they either
// have no password by default or are what a scanner tries passwords on
// first. An open Redis is the most common way a fresh VPS gets a miner.
var riskyPorts = map[int]string{
	6379: "Redis", 27017: "MongoDB", 11211: "Memcached", 3306: "MariaDB", 5432: "PostgreSQL",
	9200: "Elasticsearch", 2375: "the Docker API", 2376: "the Docker API", 5984: "CouchDB", 8086: "InfluxDB",
}

// ExposedServices names the risky services listening on a public address that
// the firewall lets the internet reach: all of them while it is off, the ones
// with an "allow from anywhere" rule while it is on.
func ExposedServices(firewall Firewall) []string {
	return exposed(proc.Run("ss -tlnH 2>/dev/null", 30*time.Second).Output, firewall)
}

func exposed(listening string, firewall Firewall) []string {
	found := map[string]bool{}

	for _, line := range strings.Split(listening, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		host, port, err := net.SplitHostPort(fields[3])
		if err != nil {
			continue
		}
		number, _ := strconv.Atoi(port)
		name, risky := riskyPorts[number]
		if !risky || !publicListener(host) {
			continue
		}

		if firewall.Active && !allowedFromAnywhere(firewall.Rules, port) {
			continue
		}

		found[fmt.Sprintf("%s (port %d)", name, number)] = true
	}

	list := make([]string, 0, len(found))
	for service := range found {
		list = append(list, service)
	}
	sort.Strings(list)

	return list
}

// publicListener is whether a listening address takes connections from
// outside: every interface, or a public one.
func publicListener(host string) bool {
	host = strings.Trim(host, "[]")
	if i := strings.Index(host, "%"); i >= 0 {
		host = host[:i]
	}
	if host == "*" || host == "" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsUnspecified() || !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}

func allowedFromAnywhere(rules []Rule, port string) bool {
	for _, rule := range rules {
		to := strings.Fields(rule.To)
		if len(to) == 0 {
			continue
		}
		target := strings.TrimSuffix(strings.TrimSuffix(to[0], "/tcp"), "/udp")
		if (target == port || target == "Anywhere") && strings.HasPrefix(rule.Action, "ALLOW") &&
			strings.HasPrefix(rule.From, "Anywhere") {
			return true
		}
	}
	return false
}
