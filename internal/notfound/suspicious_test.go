package notfound

import "testing"

func TestSuspiciousIsWhatScannersAskForAndNothingElse(t *testing.T) {
	for _, path := range []string{
		"/.env", "/app/.env", "/.env.production", "/.git/config", "/.aws/credentials",
		"/wp-config.php.bak", "/wp-config.old", "/backup.sql", "/db.sql.gz", "/index.php~",
		"/phpmyadmin/", "/pma/index.php", "/adminer.php", "/phpinfo.php",
		"/vendor/phpunit/phpunit/src/Util/PHP/eval-stdin.php", "/cgi-bin/luci", "/.DS_Store",
		"/shell.php?cmd=id", "/info.php", "/wp-content/dsa.php", "/wp-content/uploads/2024/x.php",
		"/wp-includes/js/up.php",
	} {
		if !Suspicious(path) {
			t.Errorf("%s is a scanner's request and was not seen as one", path)
		}
	}

	// What real visitors 404 on: dead links, typos, removed posts, old
	// sitemaps, apps probing for their own files.
	for _, path := range []string{
		"/", "/about-us", "/blog/2021/old-post/", "/shop/product-that-was-removed",
		"/wp-login.php", "/wp-admin/", "/contact?ref=email", "/environment-policy",
		"/news/gitlab-release", "/category/sql-tutorials/", "/oldsite/", "/backups-guide",
		"/apple-app-site-association", "/.well-known/assetlinks.json", "/index.php",
		"/wp-content/uploads/2024/photo.jpg", "/information.php-guide", "/contact.php",
	} {
		if Suspicious(path) {
			t.Errorf("%s is a request a real visitor makes and was seen as a scanner's", path)
		}
	}
}
