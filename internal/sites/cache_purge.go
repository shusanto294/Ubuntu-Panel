package sites

import (
	"fmt"
	"strings"

	"github.com/shusanto294/Ubuntu-Panel/internal/config"
	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
	"github.com/shusanto294/Ubuntu-Panel/internal/store"
)

// A page cache is only as good as the moment it is emptied. nginx keeps a
// page for as long as the site's lifetime says, and without this a post
// published at nine is on the front page whenever the copy from eight runs
// out — twelve hours later by default. WordPress knows the moment something
// changed, so WordPress empties the cache: a must-use plugin the panel writes
// into every WordPress site that has a page cache, and takes out again when
// it has none.
//
// The whole cache, never one URL. A post is on its own page, the front page,
// its categories, its tags, its author's archive, the feed and the sitemap,
// and working all of those out is how purge plugins come to miss one. nginx
// rebuilds what is asked for next, behind the visitor, so emptying it all
// costs one slow request per page rather than a stale site.

// PagePurgePluginPath is where the plugin goes in a site.
func PagePurgePluginPath(site *store.Site) string {
	return strings.TrimRight(site.DocumentRoot(), "/") + "/wp-content/mu-plugins/ubuntu-panel-cache.php"
}

// isWordPress goes by the files rather than the site's type: WordPress put
// up as a plain PHP site is still WordPress, and still has a cache to empty.
func isWordPress(site *store.Site) bool {
	if site.Type == "wordpress" {
		return true
	}

	return proc.Ok("sudo test -f " + proc.Quote(strings.TrimRight(site.DocumentRoot(), "/")+"/wp-config.php"))
}

// SyncPagePurge writes the plugin into a WordPress site with a page cache and
// removes it from one without. Safe to run at any time, as often as wanted.
func (s *Service) SyncPagePurge(site *store.Site) {
	path := PagePurgePluginPath(site)

	if !CacheActive(site) || !isWordPress(site) {
		proc.Ok("sudo rm -f " + proc.Quote(path))
		return
	}

	directory := strings.TrimSuffix(path, "/ubuntu-panel-cache.php")

	if !proc.Ok(fmt.Sprintf("sudo mkdir -p %[1]s && sudo chown www-data:www-data %[1]s", proc.Quote(directory))) {
		return
	}

	if proc.PutFile(path, PagePurgePlugin(CacheDir(site)), true) == nil {
		proc.Ok(fmt.Sprintf("sudo chown www-data:www-data %[1]s && sudo chmod 644 %[1]s", proc.Quote(path)))
	}
}

// PagePurgePlugin is the plugin, pointed at one site's cache directory.
//
// PHP runs as www-data, and so do the nginx workers that write the cache, so
// the files are WordPress's to delete. The purge is put off to the end of the
// request: saving a post fires a dozen of these hooks, and one sweep at the
// end is the same result as twelve.
func PagePurgePlugin(cacheDir string) string {
	return fmt.Sprintf(`<?php
/**
 * Plugin Name: %[1]s page cache
 * Description: Empties this site's page cache when its content changes, and adds "Purge page cache" to the admin bar. Managed by %[1]s — changes here are overwritten.
 */

if (!defined('ABSPATH')) {
    exit;
}

const UBUNTU_PANEL_CACHE_DIR = '%[2]s';

function ubuntu_panel_cache_purge_now() {
    $dir = UBUNTU_PANEL_CACHE_DIR;
    if (!is_dir($dir)) {
        return;
    }

    $entries = new RecursiveIteratorIterator(
        new RecursiveDirectoryIterator($dir, FilesystemIterator::SKIP_DOTS),
        RecursiveIteratorIterator::CHILD_FIRST
    );

    foreach ($entries as $entry) {
        $entry->isDir() ? @rmdir($entry->getPathname()) : @unlink($entry->getPathname());
    }
}

function ubuntu_panel_cache_purge_later() {
    static $queued = false;
    if (!$queued) {
        $queued = true;
        add_action('shutdown', 'ubuntu_panel_cache_purge_now');
    }
}

// Content: anything that was, or now is, published. A draft being saved is
// nobody's page yet.
add_action('transition_post_status', function ($new, $old, $post) {
    if ($new !== 'publish' && $old !== 'publish') {
        return;
    }
    if (wp_is_post_revision($post) || wp_is_post_autosave($post)) {
        return;
    }
    if (!is_post_type_viewable(get_post_type($post))) {
        return;
    }
    ubuntu_panel_cache_purge_later();
}, 10, 3);

// Comments, once they are visible.
add_action('wp_insert_comment', function ($id, $comment) {
    if ((string) $comment->comment_approved === '1') {
        ubuntu_panel_cache_purge_later();
    }
}, 10, 2);

foreach (array(
    'transition_comment_status', 'edit_comment', 'deleted_comment',
    // What every page is wrapped in.
    'switch_theme', 'customize_save_after', 'wp_update_nav_menu',
    'update_option_sidebars_widgets', 'update_option_blogname', 'update_option_blogdescription',
    // Categories and tags, which list posts and are listed by them.
    'created_term', 'edited_term', 'delete_term',
    // Code that changes what a page renders.
    'activated_plugin', 'deactivated_plugin', 'upgrader_process_complete',
    // A shop's stock, which the product page shows.
    'woocommerce_product_set_stock', 'woocommerce_variation_set_stock',
) as $hook) {
    add_action($hook, 'ubuntu_panel_cache_purge_later');
}

// The button, for whatever none of the above sees: a page builder that
// saves through its own tables, an option some plugin keeps.
add_action('admin_bar_menu', function ($bar) {
    if (!current_user_can('manage_options')) {
        return;
    }
    $bar->add_node(array(
        'id'    => 'ubuntu-panel-purge',
        'title' => 'Purge page cache',
        'href'  => wp_nonce_url(admin_url('admin-post.php?action=ubuntu_panel_purge'), 'ubuntu_panel_purge'),
    ));
}, 100);

add_action('admin_post_ubuntu_panel_purge', function () {
    if (!current_user_can('manage_options') || !check_admin_referer('ubuntu_panel_purge')) {
        wp_die('Not allowed.', '', array('response' => 403));
    }
    ubuntu_panel_cache_purge_now();
    wp_safe_redirect(wp_get_referer() ?: admin_url());
    exit;
});
`, config.Name, strings.ReplaceAll(cacheDir, "'", ""))
}
