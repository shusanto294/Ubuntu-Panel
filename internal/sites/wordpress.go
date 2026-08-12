package sites

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/crypto"
	"github.com/shusanto294/ubuntu-panel-go/internal/proc"
	"github.com/shusanto294/ubuntu-panel-go/internal/store"
)

// wordPressLoginPlugin is the must-use plugin that signs the panel in.
//
// A must-use plugin rather than an ordinary one: it cannot be deactivated by
// accident, it is not in the plugins list to be deleted, and it loads before
// anything a theme or a plugin does.
//
// The exchange is deliberately small. The panel puts a user id in a transient
// under the hash of a random token and sends the browser to the site with the
// token in the query string; this reads it, spends it, and sets the cookie.
// The token is never stored anywhere — only its hash — so the row this reads
// is worthless to anybody who finds it, and it lives for two minutes.
const wordPressLoginPlugin = `<?php
/**
 * Ubuntu Panel — one-time sign-in.
 *
 * Written by the panel. Deleting it costs nothing but the WP Admin button.
 */

add_action('init', function () {
    if (empty($_GET['ubuntu-panel-login'])) {
        return;
    }

    $token = (string) $_GET['ubuntu-panel-login'];

    // Short tokens are not ours: refuse before touching the database.
    if (strlen($token) < 32 || ! ctype_alnum($token)) {
        return;
    }

    $key = 'ubuntu_panel_login_' . hash('sha256', $token);
    $user_id = get_transient($key);

    // Spent whether or not it works, so a token is good for one attempt.
    delete_transient($key);

    if (! $user_id) {
        return;
    }

    $user = get_user_by('id', (int) $user_id);

    if (! $user) {
        return;
    }

    wp_set_current_user($user->ID);
    wp_set_auth_cookie($user->ID, false, is_ssl());

    wp_safe_redirect(admin_url());
    exit;
});
`

// WordPressLoginURL mints a one-time sign-in link into a site's dashboard.
//
// The account is the panel's own administrator where it still exists, and
// otherwise the oldest administrator on the site — which is the one somebody
// means by "the admin user" on a site whose original login has been renamed or
// removed.
func (s *Service) WordPressLoginURL(ctx context.Context, siteID int64) (string, error) {
	site, err := s.Store.FindSite(ctx, siteID)
	if err != nil {
		return "", errors.New("that site no longer exists")
	}

	if site.Type != "wordpress" {
		return "", errors.New("this is not a WordPress site")
	}

	if !proc.Which("wp") {
		return "", errors.New("WP-CLI is not installed on this machine")
	}

	root := site.DocumentRoot()
	wp := "sudo -u www-data wp --path=" + proc.Quote(root)

	userID, err := s.wordPressAdminID(wp, site)
	if err != nil {
		return "", err
	}

	if err := s.installWordPressLoginPlugin(root); err != nil {
		return "", err
	}

	// Alphanumeric, because the plugin refuses anything else — a token that
	// needs escaping in a URL is a token that will be escaped differently
	// somewhere along the way.
	token := crypto.Password(48)
	token = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}

		return -1
	}, token)

	if len(token) < 32 {
		return "", errors.New("could not generate a sign-in token")
	}

	sum := sha256.Sum256([]byte(token))
	key := "ubuntu_panel_login_" + hex.EncodeToString(sum[:])

	// Two minutes: long enough for a browser to follow a redirect, short
	// enough that a link left in a history is dead by the time anybody reads
	// it.
	command := fmt.Sprintf("%s transient set %s %d 120", wp, proc.Quote(key), userID)

	if result := proc.Run(command, 2*time.Minute); !result.OK() {
		return "", fmt.Errorf("WordPress would not take the sign-in token: %s",
			strings.TrimSpace(result.Output))
	}

	return site.URL() + "/?ubuntu-panel-login=" + token, nil
}

// wordPressAdminID is the account the sign-in link is for.
func (s *Service) wordPressAdminID(wp string, site *store.Site) (int64, error) {
	// The panel's own administrator first, by name: it is the account whose
	// password this page shows, so signing in as anybody else would be
	// confusing on the one screen that says who you are.
	if site.WPAdminUser != "" {
		found := strings.TrimSpace(proc.Run(fmt.Sprintf(
			"%s user get %s --field=ID 2>/dev/null", wp, proc.Quote(site.WPAdminUser)),
			2*time.Minute).Output)

		if id, err := strconv.ParseInt(found, 10, 64); err == nil && id > 0 {
			return id, nil
		}
	}

	// Otherwise the oldest administrator, which is what "the admin user" means
	// on a site whose first login was renamed or removed.
	found := strings.TrimSpace(proc.Run(fmt.Sprintf(
		"%s user list --role=administrator --field=ID --orderby=ID --order=asc --number=1 2>/dev/null",
		wp), 2*time.Minute).Output)

	// One id, whatever the command decided to print around it.
	for _, line := range strings.Fields(found) {
		if id, err := strconv.ParseInt(line, 10, 64); err == nil && id > 0 {
			return id, nil
		}
	}

	return 0, errors.New("this site has no administrator the panel can sign in as")
}

// installWordPressLoginPlugin writes the plugin, every time.
//
// Rewritten rather than checked for: it is a file the panel owns, it is small,
// and a copy left behind by an older panel would otherwise keep whatever it
// used to do.
func (s *Service) installWordPressLoginPlugin(root string) error {
	directory := strings.TrimRight(root, "/") + "/wp-content/mu-plugins"

	if !proc.Ok(fmt.Sprintf("sudo mkdir -p %[1]s && sudo chown www-data:www-data %[1]s",
		proc.Quote(directory))) {
		return errors.New("could not create the mu-plugins directory")
	}

	path := directory + "/ubuntu-panel-login.php"

	if err := proc.PutFile(path, wordPressLoginPlugin, true); err != nil {
		return err
	}

	proc.Ok(fmt.Sprintf("sudo chown www-data:www-data %[1]s && sudo chmod 644 %[1]s",
		proc.Quote(path)))

	return nil
}
