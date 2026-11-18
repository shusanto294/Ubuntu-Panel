package mail

import (
	"context"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/security"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
	"github.com/shusanto294/Ubuntu-Panel/internal/system"
)

// DNSCheck is one record a mail domain needs, and what the public DNS says
// about it right now.
type DNSCheck struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Expected string `json:"expected"`
	Found    string `json:"found"`
	OK       bool   `json:"ok"`
	// Required records decide the domain's tick. The rest are worth having
	// and are shown, but a domain without them still delivers.
	Required bool   `json:"required"`
	Hint     string `json:"hint"`
}

// DNSHealth is every check for one domain.
type DNSHealth struct {
	DomainID  int64      `json:"domain_id"`
	Domain    string     `json:"domain"`
	OK        bool       `json:"ok"`
	Passed    int        `json:"passed"`
	Total     int        `json:"total"`
	Checks    []DNSCheck `json:"checks"`
	CheckedAt int64      `json:"checked_at"`
}

var healthCache = struct {
	sync.Mutex
	entries map[int64]DNSHealth
}{entries: map[int64]DNSHealth{}}

// healthFor is how long an answer is kept: long enough that the list does
// not ask 1.1.1.1 the same seven questions on every reload, short enough that
// a record just fixed shows up while the operator is still looking.
const healthFor = 5 * time.Minute

// DNSHealthOf checks what the public DNS says about a mail domain's records
// against what it should say. fresh skips the cache.
func (s *Service) DNSHealthOf(ctx context.Context, domain *store.EmailDomain, fresh bool) DNSHealth {
	healthCache.Lock()
	cached, ok := healthCache.entries[domain.ID]
	healthCache.Unlock()

	if ok && !fresh && cached.Domain == domain.Domain && time.Since(time.Unix(cached.CheckedAt, 0)) < healthFor {
		return cached
	}

	health := s.checkDNS(ctx, domain)

	healthCache.Lock()
	healthCache.entries[domain.ID] = health
	healthCache.Unlock()

	return health
}

func (s *Service) checkDNS(ctx context.Context, domain *store.EmailDomain) DNSHealth {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	ip := system.PublicIP()
	name := domain.Domain
	mailHost := domain.WebmailHost()
	resolver := system.PublicResolver()

	selector := domain.DKIMSelector
	if selector == "" {
		selector = "mail"
	}

	var (
		wait                            sync.WaitGroup
		mailAddrs, autoAddrs, apexAddrs []string
		mxs                             []*net.MX
		apexTXT, dmarcTXT, dkimTXT      []string
		reverse                         ReverseDNS
	)

	run := func(f func()) { wait.Add(1); go func() { defer wait.Done(); f() }() }

	run(func() { mailAddrs, _ = resolver.LookupHost(ctx, mailHost) })
	run(func() { autoAddrs, _ = resolver.LookupHost(ctx, domain.AutoconfigHost()) })
	run(func() { apexAddrs, _ = resolver.LookupHost(ctx, name) })
	run(func() { mxs, _ = resolver.LookupMX(ctx, name) })
	run(func() { apexTXT, _ = resolver.LookupTXT(ctx, name) })
	run(func() { dmarcTXT, _ = resolver.LookupTXT(ctx, "_dmarc."+name) })
	run(func() { dkimTXT, _ = resolver.LookupTXT(ctx, selector+"._domainkey."+name) })
	run(func() { reverse = s.ReverseDNSStatus(ctx) })
	wait.Wait()

	checks := []DNSCheck{}

	// The mail host: what the MX points at, and what clients connect to.
	mailCheck := DNSCheck{Key: "mail_a", Label: "Mail server address", Type: "A", Name: mailHost,
		Expected: ip, Found: strings.Join(mailAddrs, ", "), Required: true}
	switch {
	case contains(mailAddrs, ip):
		mailCheck.OK = true
	case len(mailAddrs) == 0:
		mailCheck.Hint = "Add an A record for " + mailHost + " pointing at " + ip + "."
	case allCloudflare(mailAddrs):
		mailCheck.Hint = "It is proxied through Cloudflare (orange cloud), which only carries web " +
			"traffic. Set it to DNS only (grey cloud) so mail can reach this server."
	default:
		mailCheck.Hint = "It points somewhere else. Change it to " + ip + "."
	}
	checks = append(checks, mailCheck)

	// MX: where the rest of the internet delivers this domain's mail.
	mxNames := []string{}
	mxOK := false
	for _, mx := range mxs {
		host := strings.TrimSuffix(strings.ToLower(mx.Host), ".")
		mxNames = append(mxNames, host)
		if host == mailHost {
			mxOK = true
		}
	}
	mxCheck := DNSCheck{Key: "mx", Label: "Incoming mail (MX)", Type: "MX", Name: name,
		Expected: "10 " + mailHost, Found: strings.Join(mxNames, ", "), OK: mxOK, Required: true}
	if !mxOK {
		if len(mxNames) == 0 {
			mxCheck.Hint = "Add an MX record on " + name + " pointing at " + mailHost + ", priority 10."
		} else {
			mxCheck.Hint = "The MX points at another mail service, so mail to this domain never " +
				"reaches this server. Replace it with " + mailHost + "."
		}
	}
	checks = append(checks, mxCheck)

	// SPF: which servers may send as this domain.
	spf := []string{}
	for _, txt := range apexTXT {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(txt)), "v=spf1") {
			spf = append(spf, txt)
		}
	}
	spfCheck := DNSCheck{Key: "spf", Label: "Sender policy (SPF)", Type: "TXT", Name: name,
		Expected: "v=spf1 a:" + mailHost + " ~all", Found: strings.Join(spf, " | "), Required: true}
	switch {
	case len(spf) == 0:
		spfCheck.Hint = "Add the TXT record. Without it Gmail and Outlook cannot tell this server " +
			"is allowed to send for " + name + "."
	case len(spf) > 1:
		spfCheck.Hint = "There are " + strconv.Itoa(len(spf)) + " SPF records, and receivers treat that as " +
			"no SPF at all. Merge them into one."
	case spfAllows(spf[0], ip, mailHost, mxOK, contains(apexAddrs, ip)):
		spfCheck.OK = true
	default:
		spfCheck.Hint = "The SPF record does not include this server. Add a:" + mailHost +
			" (or ip4:" + ip + ") before its ~all."
	}
	checks = append(checks, spfCheck)

	// DKIM: the signature on every message this server sends.
	dkimCheck := DNSCheck{Key: "dkim", Label: "Signature key (DKIM)", Type: "TXT",
		Name: selector + "._domainkey." + name, Required: true}
	if domain.DKIMPublicKey == "" {
		dkimCheck.Hint = "The DKIM key has not been generated yet. Set the domain up again from its page."
	} else {
		dkimCheck.Expected = "v=DKIM1; k=rsa; p=" + domain.DKIMPublicKey
		for _, txt := range dkimTXT {
			dkimCheck.Found = txt
			if strings.Contains(stripSpace(txt), "p="+stripSpace(domain.DKIMPublicKey)) {
				dkimCheck.OK = true
				break
			}
		}
		if !dkimCheck.OK {
			if len(dkimTXT) == 0 {
				dkimCheck.Hint = "Add the TXT record. Without it messages are unsigned, and Gmail sends " +
					"unsigned mail to spam or refuses it."
			} else {
				dkimCheck.Hint = "The published key is not the one this server signs with. Replace it " +
					"with the value above."
			}
		}
	}
	checks = append(checks, dkimCheck)

	// DMARC: what receivers should do with mail that fails the two above.
	dmarcCheck := DNSCheck{Key: "dmarc", Label: "Policy (DMARC)", Type: "TXT", Name: "_dmarc." + name,
		Expected: "v=DMARC1; p=none; rua=mailto:postmaster@" + name, Required: true}
	for _, txt := range dmarcTXT {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(txt)), "V=DMARC1") {
			dmarcCheck.OK, dmarcCheck.Found = true, txt
		}
	}
	if !dmarcCheck.OK {
		dmarcCheck.Hint = "Add the TXT record. Gmail and Yahoo require DMARC from anyone sending to them in volume."
	}
	checks = append(checks, dmarcCheck)

	// Reverse DNS: the server's, shared by every domain on it, and the first
	// thing Gmail checks.
	ptrCheck := DNSCheck{Key: "ptr", Label: "Reverse DNS (PTR)", Type: "PTR", Name: reverse.IP,
		Expected: reverse.Suggested, Found: reverse.Name, OK: reverse.Confirmed, Required: true}
	if !ptrCheck.OK {
		ptrCheck.Hint = "Set at your hosting provider, not in DNS: the reverse DNS of " + reverse.IP +
			" should be " + reverse.Suggested + ". Gmail refuses mail from a server without one."
		if reverse.Name != "" {
			ptrCheck.Hint = reverse.Name + " does not resolve back to " + reverse.IP + ". " + ptrCheck.Hint
		}
	}
	checks = append(checks, ptrCheck)

	// Autoconfig: only for mail clients setting themselves up.
	autoCheck := DNSCheck{Key: "autoconfig", Label: "Mail client setup", Type: "A",
		Name: domain.AutoconfigHost(), Expected: ip, Found: strings.Join(autoAddrs, ", "),
		OK: len(autoAddrs) > 0 && (contains(autoAddrs, ip) || allCloudflare(autoAddrs))}
	if !autoCheck.OK {
		autoCheck.Hint = "Optional. Lets Thunderbird and other mail clients fill in the server settings by themselves."
	}
	checks = append(checks, autoCheck)

	health := DNSHealth{DomainID: domain.ID, Domain: name, Checks: checks, OK: true,
		CheckedAt: time.Now().Unix()}
	for _, check := range checks {
		if !check.Required {
			continue
		}
		health.Total++
		if check.OK {
			health.Passed++
		} else {
			health.OK = false
		}
	}

	return health
}

// spfAllows is whether an SPF record lets this server send: by naming the
// mail host, the address, or a mechanism that already covers one of them.
// Includes are not followed; a record that relies on one for this server is
// rare enough to be told to name it outright.
func spfAllows(record, ip, mailHost string, mxIsOurs, apexIsOurs bool) bool {
	for _, term := range strings.Fields(strings.ToLower(record)) {
		term = strings.TrimLeft(term, "+")

		switch {
		case term == "a:"+mailHost, term == "ip4:"+ip, term == "ip6:"+ip:
			return true
		case strings.HasPrefix(term, "ip4:") && strings.Contains(term, "/"):
			if _, network, err := net.ParseCIDR(strings.TrimPrefix(term, "ip4:")); err == nil &&
				network.Contains(net.ParseIP(ip)) {
				return true
			}
		case term == "mx" && mxIsOurs, term == "a" && apexIsOurs:
			return true
		}
	}

	return false
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func allCloudflare(addresses []string) bool {
	if len(addresses) == 0 {
		return false
	}
	for _, address := range addresses {
		if !security.FromCloudflare(address) {
			return false
		}
	}
	return true
}

func stripSpace(text string) string {
	return strings.Join(strings.Fields(text), "")
}
