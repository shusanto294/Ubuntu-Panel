//go:build !linux

package system

// LowerPriority does nothing off Linux, where the panel is only ever built to
// be developed.
func LowerPriority() {}
