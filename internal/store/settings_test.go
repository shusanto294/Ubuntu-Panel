package store

import (
	"testing"

	"github.com/shusanto294/Ubuntu-Panel/internal/config"
)

// A new panel starts on the newest PHP and Node.js it knows; one that already
// ran PHP under the old 8.3 fallback, and never chose, stays on 8.3.
func TestTheDefaultPHPIsTheNewestUnlessThisMachineAlreadyRunsTheOldOne(t *testing.T) {
	fresh := metricStore(t)
	fresh.PinLegacyPHPDefault(false)

	if got := fresh.PHPVersion(); got != config.PHPVersions[0] {
		t.Errorf("a new panel defaults to PHP %s, not the newest (%s)", got, config.PHPVersions[0])
	}
	if got := fresh.NodeVersion(); got != config.NodeVersions[0] {
		t.Errorf("a new panel defaults to Node.js %s, not the newest (%s)", got, config.NodeVersions[0])
	}

	existing := metricStore(t)
	existing.PinLegacyPHPDefault(true)

	if got := existing.PHPVersion(); got != "8.3" {
		t.Errorf("a panel already running PHP moved its default to %s", got)
	}

	chosen := metricStore(t)
	chosen.SetSetting("php_version", "8.4")
	chosen.PinLegacyPHPDefault(true)

	if got := chosen.PHPVersion(); got != "8.4" {
		t.Errorf("a chosen default was overwritten with %s", got)
	}
}
