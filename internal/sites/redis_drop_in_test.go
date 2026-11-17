package sites

import (
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// The drop-in is looked for where WordPress loads it from: under the web
// directory when the site serves from one.
func TestTheObjectCacheDropInIsWhereWordPressLoadsIt(t *testing.T) {
	site := &store.Site{RootPath: "/var/www/blog.example/"}
	if got := ObjectCacheDropIn(site); got != "/var/www/blog.example/wp-content/object-cache.php" {
		t.Errorf("drop-in at %s", got)
	}

	site.WebDirectory = "public"
	if got := ObjectCacheDropIn(site); got != "/var/www/blog.example/public/wp-content/object-cache.php" {
		t.Errorf("drop-in under a web directory at %s", got)
	}
}
