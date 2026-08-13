package sites

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/proc"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
)

// The object cache, which is the other half of making a site fast and is not
// the same thing as the page cache at all.
//
// nginx's page cache keeps the finished HTML and never asks the application
// again. Redis keeps what the application looked up on the way to building
// that HTML — the options, the queries, the transients — so the page is still
// built for every visitor, but built without asking MariaDB the same forty
// questions each time.
//
// That is why they are offered as a choice rather than a switch: a site whose
// pages are the same for everybody wants the page cache, and a site whose
// pages are personal — a shop, a members' area, anything behind a login —
// cannot have one, and wants this instead.

// RedisSupported reports whether the panel can wire this site into Redis by
// itself.
//
// WordPress only, and honestly so: WordPress has a documented drop-in for an
// object cache and a plugin that installs it, so the panel can do the whole
// job. A Laravel site keeps its cache driver in .env and a Node app in its own
// code — the panel would be editing somebody's application, and getting it
// wrong there means a site that boots into a stack trace.
func RedisSupported(site *store.Site) bool {
	return site.Type == "wordpress"
}

// RedisPrefix keeps one site's keys away from another's.
//
// Every site on the machine shares one Redis. Without a prefix the second
// WordPress install reads the first one's cached options — its site title, its
// active plugins, its user meta — and the symptom is two sites showing each
// other's content with nothing wrong in either database.
func RedisPrefix(site *store.Site) string {
	return sanitiseIdentifier(site.Domain) + "_"
}

// redisInstalled asks the machine rather than the panel's record of it: the
// record is what somebody installed, and this has to be true right now.
func redisInstalled() bool {
	return proc.Ok("command -v redis-server >/dev/null 2>&1")
}

// wpCommand is wp-cli in this site's directory, as the user that owns it.
func wpCommand(site *store.Site) string {
	return "sudo -u www-data wp --path=" + proc.Quote(site.DocumentRoot())
}

// EnableRedisCache points a site's object cache at Redis.
//
// The constants are written into wp-config.php and the plugin's drop-in is
// installed, which is what WordPress actually reads — activating the plugin
// alone changes nothing.
func (s *Service) EnableRedisCache(ctx context.Context, site *store.Site) error {
	if !RedisSupported(site) {
		return fmt.Errorf("the panel cannot wire a %s site into Redis by itself",
			strings.ToLower(site.TypeLabel()))
	}

	if !redisInstalled() {
		return errors.New("Redis is not installed on this machine — install it from the " +
			"Apps page first, then turn this on")
	}

	if !proc.Ok("sudo systemctl is-active --quiet redis-server") &&
		!proc.Ok("sudo systemctl enable --now redis-server") {
		return errors.New("Redis is installed but will not start, so nothing was changed")
	}

	wp := wpCommand(site)

	// The plugin is the supported way to install WordPress's object-cache
	// drop-in. Installed from wordpress.org, which is where wp-cli gets it.
	if result := proc.Run(fmt.Sprintf(
		"%[1]s plugin is-installed redis-cache 2>/dev/null || %[1]s plugin install redis-cache", wp),
		5*time.Minute); !result.OK() {
		return fmt.Errorf("the Redis Object Cache plugin could not be installed: %s",
			lastLine(result.Output))
	}

	if result := proc.Run(wp+" plugin activate redis-cache", 2*time.Minute); !result.OK() {
		return fmt.Errorf("the Redis Object Cache plugin could not be activated: %s",
			lastLine(result.Output))
	}

	// The password Redis was configured with when it was installed. Without it
	// every connection is refused and WordPress falls back to the database —
	// slower than no cache at all, and silent about it.
	settings := []string{
		"config set WP_REDIS_HOST 127.0.0.1",
		"config set WP_REDIS_PORT 6379 --raw",
		"config set WP_REDIS_PREFIX " + proc.Quote(RedisPrefix(site)),
		"config set WP_CACHE true --raw",
	}

	if password := s.Store.SecretSetting("redis_password"); password != "" {
		settings = append(settings, "config set WP_REDIS_PASSWORD "+proc.Quote(password))
	}

	for _, setting := range settings {
		if result := proc.Run(wp+" "+setting, time.Minute); !result.OK() {
			return fmt.Errorf("wp-config.php could not be written: %s", lastLine(result.Output))
		}
	}

	// The drop-in. This is the line that makes WordPress use Redis.
	if result := proc.Run(wp+" redis enable", 2*time.Minute); !result.OK() {
		return fmt.Errorf("WordPress would not connect to Redis: %s", lastLine(result.Output))
	}

	return nil
}

// DisableRedisCache takes the drop-in back out.
//
// Redis itself is left running and the plugin left installed: the server is
// shared with every other site on the machine, and a plugin that is not
// activated costs nothing but is there when somebody turns this on again.
func (s *Service) DisableRedisCache(ctx context.Context, site *store.Site) error {
	if !RedisSupported(site) {
		return nil
	}

	wp := wpCommand(site)

	proc.Run(wp+" redis disable", 2*time.Minute)
	proc.Run(wp+" plugin deactivate redis-cache", 2*time.Minute)

	return nil
}

// FlushRedisCache empties what this site put in Redis, and nothing another
// site put there — the prefix is what makes that possible.
func (s *Service) FlushRedisCache(site *store.Site) error {
	if !RedisSupported(site) {
		return nil
	}

	if result := proc.Run(wpCommand(site)+" redis flush", 2*time.Minute); !result.OK() {
		// The plugin's own flush is the one that respects the prefix; falling
		// back to WordPress's is right when the drop-in is not in place.
		if fallback := proc.Run(wpCommand(site)+" cache flush", time.Minute); !fallback.OK() {
			return errors.New("the object cache could not be emptied")
		}
	}

	return nil
}

// lastLine is the part of a command's output worth putting in front of
// somebody: the complaint, not the transcript.
func lastLine(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")

	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}

	return "no output"
}
