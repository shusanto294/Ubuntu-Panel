//go:build linux

package terminal

import (
	"os/user"
	"strconv"
	"syscall"
)

// credentials drops a shell started by root to the panel's own user.
//
// A browser shell that is root by accident is the difference between "the panel
// was compromised" and "the machine was". The panel normally runs as its own
// user and this never applies; it is here for the install that put it under
// root anyway.
func credentials(account *user.User) *syscall.SysProcAttr {
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return nil
	}

	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return nil
	}

	return &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Uid: uint32(uid),
			Gid: uint32(gid),
		},
	}
}
