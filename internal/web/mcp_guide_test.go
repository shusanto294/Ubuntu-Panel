package web

import (
	"log/slog"
	"strings"
	"testing"
)

// The guide is only worth reading if everything in it is true.
//
// It names tools by hand, in prose written for an assistant, so the one way it
// rots is a tool being renamed or removed underneath it — which produces a
// panel that confidently tells an assistant to call something that is not
// there. In-package rather than in web_test because it asks the registry
// directly: this is about the two lists agreeing, not about the endpoint.
func TestTheGuideOnlyNamesRealTools(t *testing.T) {
	app := &App{Log: slog.Default()}
	server := app.buildMCP()

	registered := map[string]bool{}

	for _, name := range server.ToolNames() {
		registered[name] = true
	}

	for _, name := range guideTools() {
		if !registered[name] {
			t.Errorf("panel_guide names %s, which is not a registered tool", name)
		}
	}
}

// And every area of the panel is in it. A page whose tools exist but which the
// guide never mentions is a page an assistant finds by accident, which is the
// failure this whole thing is here to stop.
func TestTheGuideCoversEveryAreaOfThePanel(t *testing.T) {
	areas := map[string]bool{}

	for _, area := range panelGuide {
		areas[area.Key] = true

		if strings.TrimSpace(area.Page) == "" || strings.TrimSpace(area.What) == "" {
			t.Errorf("the %s area has no page or no description", area.Key)
		}

		if len(area.Tools) == 0 {
			t.Errorf("the %s area names no tools", area.Key)
		}
	}

	for _, key := range []string{"sites", "files", "email", "databases", "dns", "apps",
		"system", "shell"} {
		if !areas[key] {
			t.Errorf("panel_guide has nothing about %s", key)
		}
	}
}
