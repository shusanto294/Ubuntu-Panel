package system

import "syscall"

// panelNice is the CPU priority the panel runs at: below the sites it hosts.
// The panel is a control panel; nothing it does in the background is worth a
// visitor's page waiting for it. Under load the kernel gives nginx, PHP and
// the databases the CPU first, and the panel takes what is left.
const panelNice = 10

// ioprio_set's "best effort" class at its lowest level, for the process group.
const (
	ioprioWhoPgrp     = 2
	ioprioClassBE     = 2
	ioprioClassShift  = 13
	ioprioLowestLevel = 7
)

// LowerPriority puts the panel and everything it starts — sudo, journalctl,
// fail2ban-client, apt, nginx -t — behind the sites, for CPU and for disk.
//
// For the whole process group, not the process: on Linux a nice value belongs
// to a thread, and the Go runtime has several. Asked for the group, the kernel
// sets every thread, and threads and children made afterwards inherit it.
// systemd starts the panel in a group of its own. It cannot fail in a way
// worth stopping for, so it returns nothing.
func LowerPriority() {
	syscall.Setpriority(syscall.PRIO_PGRP, 0, panelNice)

	syscall.Syscall(syscall.SYS_IOPRIO_SET, ioprioWhoPgrp, 0,
		uintptr(ioprioClassBE<<ioprioClassShift|ioprioLowestLevel))
}
