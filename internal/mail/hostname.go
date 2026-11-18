package mail

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/system"
)

// ReverseDNS is what this server's IPv4 address says about itself, which is
// the first thing a receiving mail server checks: Gmail refuses mail outright
// from an address with no PTR record, or one whose name does not resolve back.
//
// The record lives at the hosting provider, not in DNS the panel manages, so
// it is the one part of sending mail the panel cannot do itself. What it can
// do is notice when it is right and use it, and say exactly what to set when
// it is not.
type ReverseDNS struct {
	IP        string `json:"ip"`
	Name      string `json:"name"`
	Confirmed bool   `json:"confirmed"`
	// Suggested is what the record should say: the name this server already
	// greets other servers as, which resolves to it.
	Suggested string `json:"suggested"`
}

var reverseCache struct {
	sync.Mutex
	value ReverseDNS
	asked time.Time
}

// reverseDNS is the lookup, kept for ten minutes: the Email pages show it and
// reload themselves while anything runs, and the answer only changes when
// somebody edits a record at their provider.
func reverseDNS(ctx context.Context) ReverseDNS {
	reverseCache.Lock()
	defer reverseCache.Unlock()

	ip := system.PublicIP()

	if reverseCache.value.IP == ip && time.Since(reverseCache.asked) < 10*time.Minute {
		return reverseCache.value
	}

	name, confirmed := system.ReverseName(ctx, ip)

	reverseCache.value = ReverseDNS{IP: ip, Name: name, Confirmed: confirmed}
	reverseCache.asked = time.Now()

	return reverseCache.value
}

// usableName is whether a confirmed PTR name can be what Postfix calls itself.
// Not a mail domain itself: Postfix delivers $myhostname locally, so naming the
// server `example.com` while example.com is a virtual mailbox domain makes
// Postfix claim the domain twice and bounce mail to it.
func (s *Service) usableName(ctx context.Context, name string) bool {
	if name == "" || !strings.Contains(name, ".") {
		return false
	}

	domains, err := s.Store.EmailDomains(ctx)
	if err != nil {
		return false
	}

	for _, domain := range domains {
		if strings.EqualFold(domain.Domain, name) {
			return false
		}
	}

	return true
}

// ReverseDNSStatus is the lookup with what the record should say filled in.
func (s *Service) ReverseDNSStatus(ctx context.Context) ReverseDNS {
	status := reverseDNS(ctx)
	status.Suggested = s.Hostname()

	if status.Confirmed && s.usableName(ctx, status.Name) {
		status.Suggested = status.Name
	}

	return status
}

// preferredHostname is what this server should call itself: the name its
// reverse DNS gives, when that resolves back to it, because the name in EHLO
// matching the PTR is what receivers look for. Otherwise mail.<domain> for the
// first mail domain, which the panel publishes an A record for — and which is
// exactly what the PTR record should then be set to.
func (s *Service) preferredHostname(ctx context.Context, fallback string) string {
	status := reverseDNS(ctx)

	if status.Confirmed && s.usableName(ctx, status.Name) {
		return status.Name
	}

	return fallback
}

// SyncHostname makes Postfix greet other servers with the name this server's
// reverse DNS gives, once that name resolves back to it. Nobody sets the mail
// hostname by hand: the panel picks mail.<domain> when the first domain is
// added, and moves to the PTR name as soon as the provider's record is right.
//
// Cheap when nothing changed — one cached lookup and a comparison — so it is
// run on every start and whenever the Email pages are looked at, which is how
// a PTR record fixed at the provider is picked up without a button.
func (s *Service) SyncHostname(ctx context.Context) (string, error) {
	if !s.IsConfigured() {
		return "", nil
	}

	current := s.Hostname()

	wanted := s.preferredHostname(ctx, current)
	if wanted == current {
		return "", nil
	}

	s.Store.SetSetting("mail_hostname", wanted)

	if err := s.writeConfigs(); err != nil {
		return "", err
	}

	result := proc.Run("sudo postconf -e "+proc.Quote("myhostname="+wanted)+
		" && sudo systemctl reload postfix", time.Minute)
	if !result.OK() {
		return "", fmt.Errorf("postfix did not take the new name: %s", result.Output)
	}

	return "This server now greets other servers as " + wanted +
		", the name its reverse DNS gives.", nil
}

var syncing sync.Mutex

// SyncHostnameInBackground is SyncHostname for a page load: it never makes the
// page wait, and two loads at once do not both reload Postfix.
func (s *Service) SyncHostnameInBackground() {
	if !syncing.TryLock() {
		return
	}

	go func() {
		defer syncing.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		s.SyncHostname(ctx)
	}()
}
