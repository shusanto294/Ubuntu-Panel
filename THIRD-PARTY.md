# Third-party notices

Ubuntu Panel is MIT licensed — see [LICENSE](LICENSE). A release is a single
binary with the built frontend embedded in it, so that binary carries the
libraries below as well as this project's own code. Their copyright notices are
reproduced by naming them here; each project's full licence text is in its own
repository and in this repository's dependency lockfiles (`go.sum`,
`package-lock.json`).

Nothing here is copyleft in a way that reaches this project's own source: all of
it is MIT, BSD or MPL-2.0, and MPL-2.0 applies per file to the library itself
rather than to a program that links it.

## Go, compiled into the binary

| Module | Licence |
| --- | --- |
| `golang.org/x/crypto` | BSD-3-Clause — The Go Authors |
| `modernc.org/sqlite` and its `modernc.org/*` support packages | BSD-3-Clause |
| `github.com/creack/pty` | MIT — Keith Rarick |
| `github.com/dustin/go-humanize` | MIT — Dustin Sallings |
| `github.com/google/uuid` | BSD-3-Clause — Google |
| `github.com/gorilla/websocket` | BSD-2-Clause — The Gorilla WebSocket Authors |
| `github.com/hashicorp/golang-lru/v2` | MPL-2.0 — HashiCorp |
| `github.com/mattn/go-isatty` | MIT — Yasuhiro Matsumoto |
| `github.com/ncruces/go-strftime` | MIT — Nuno Cruces |
| `github.com/remyoudompheng/bigfft` | BSD-3-Clause — Rémy Oudompheng |

The Go standard library itself is BSD-3-Clause.

## Frontend, embedded in the binary

| Package | Licence |
| --- | --- |
| `vue` | MIT — Evan You and Vue contributors |
| `@inertiajs/vue3` | MIT — Jonathan Reinink |
| `@xterm/xterm` and its addons | MIT — The xterm.js authors |
| `axios` | MIT — Matt Zabriskie and contributors |
| `sweetalert2` | MIT — Limon Monte |
| `qrcode` | MIT — Ryan Day |
| `tailwindcss`, `@tailwindcss/forms` | MIT — Tailwind Labs |
| `vite`, `@vitejs/plugin-vue`, `postcss`, `autoprefixer` | MIT (build tools; not shipped in the binary) |

Several components under `resources/js/Components` and `resources/js/Pages/Auth`
began as Laravel Breeze's Vue starter kit and have been restyled since —
MIT, copyright Taylor Otwell.

## Not bundled

The panel installs and configures nginx, PHP-FPM, MariaDB, MongoDB, Redis,
Node.js, Composer, WP-CLI, PM2, phpMyAdmin, Certbot, Postfix, Dovecot, OpenDKIM
and Roundcube from Ubuntu's own repositories and from upstream releases. They
are separate programs under their own licences, installed on your server rather
than distributed as part of this project. The same is true of WordPress,
Nextcloud and n8n, which the panel downloads when you deploy a site of that
type.
