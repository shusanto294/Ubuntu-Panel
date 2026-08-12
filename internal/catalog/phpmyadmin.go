package catalog

import (
	"fmt"

	"github.com/shusanto294/ubuntu-panel-go/internal/proc"
	"github.com/shusanto294/ubuntu-panel-go/internal/sites"
)

// phpMyAdminSecret is where the blowfish key lives once it has been generated.
// Kept rather than regenerated, because it encrypts the cookie holding the
// logged-in database password: a new one on every install would sign everybody
// out and, worse, is the thing phpMyAdmin refuses to run without.
const phpMyAdminSecret = "phpmyadmin_blowfish"

// writePhpMyAdminConfig writes config.inc.php.
//
// phpMyAdmin ships without one and shows "The configuration file now needs a
// secret passphrase (blowfish_secret)" on every page until it has one. The
// other half is TempDir: unset, it writes its template cache under its own
// directory, which is root-owned here, and every page carries a warning about
// it.
//
// Written rather than templated from the sample file: the sample is 150 lines
// of commented defaults, and the three settings that matter are these.
func (s *Service) writePhpMyAdminConfig() (string, error) {
	secret, err := s.Store.RememberSecret(phpMyAdminSecret, 32)
	if err != nil {
		return "", err
	}

	// Exactly 32 bytes. phpMyAdmin warns when the key is shorter and truncates
	// when it is longer, and RememberSecret's alphabet is wider than one byte
	// per character in neither case — but the check it runs is on the string
	// length, so it is cut to size here rather than hoped about.
	if len(secret) > 32 {
		secret = secret[:32]
	}

	contents := fmt.Sprintf(`<?php
/* Managed by Ubuntu Panel. */

declare(strict_types=1);

$cfg['blowfish_secret'] = '%s';

$i = 0;
$i++;
$cfg['Servers'][$i]['auth_type'] = 'cookie';
$cfg['Servers'][$i]['host'] = '127.0.0.1';
$cfg['Servers'][$i]['compress'] = false;
$cfg['Servers'][$i]['AllowNoPassword'] = false;

$cfg['TempDir'] = '/var/lib/ubuntu-panel/phpmyadmin-tmp';

/* Served from a path on the panel's own origin rather than a vhost of its
   own, so absolute URLs have to be told about the prefix. */
$cfg['PmaAbsoluteUri'] = '';
`, secret)

	path := sites.PhpMyAdminParent + "/phpmyadmin/config.inc.php"

	if err := proc.PutFile(path, contents, true); err != nil {
		return "", err
	}

	// Readable by the PHP-FPM worker, by nobody else: it holds the key that
	// encrypts the login cookie.
	proc.Ok(fmt.Sprintf("sudo chown root:www-data %s && sudo chmod 640 %s",
		proc.Quote(path), proc.Quote(path)))

	return "Wrote config.inc.php with a blowfish secret and a writable temp directory.", nil
}
