//go:build !linux

package terminal

import (
	"os/user"
	"syscall"
)

// credentials is a Linux idea. Everywhere else the panel is being developed
// rather than run, and the shell is whoever started it.
func credentials(account *user.User) *syscall.SysProcAttr { return nil }
