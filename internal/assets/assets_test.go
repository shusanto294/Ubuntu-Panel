package assets

import (
	"bytes"
	"io/fs"
	"testing"
)

// The panel's mark travels inside the binary.
//
// Both files are served from the root — the sidebar and the login page point at
// /logo.png, and every page's <head> at /favicon.png — so a release built
// without them is one where the panel renders a broken image on the first
// screen anybody sees. They live outside build/, which is gitignored, exactly
// so that they are committed rather than produced.
func TestTheLogoIsEmbedded(t *testing.T) {
	embedded := FS("", false)

	// The first bytes of a PNG, so a placeholder or a truncated copy fails here
	// rather than in a browser.
	signature := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

	for _, name := range []string{"logo.png", "favicon.png"} {
		contents, err := fs.ReadFile(embedded, name)
		if err != nil {
			t.Fatalf("%s is not in the binary: %v", name, err)
		}

		if !bytes.HasPrefix(contents, signature) {
			t.Errorf("%s is not a PNG", name)
		}
	}
}
