package security

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// The kernel's network settings: hardening a public server should have, and
// whether it answers ping. Each is a sysctl file the switch writes or
// removes, and costs nothing once it is loaded.

// ------------------------------------------------------------------ kernel

const kernelPath = "/etc/sysctl.d/60-ubuntu-panel.conf"

// kernelSettings are what a public server should have and Ubuntu does not all
// set: SYN cookies and a deeper backlog against SYN floods, and no ICMP
// redirects or source-routed packets, which only matter to routers and are a
// way to redirect a host's traffic. Nothing here touches forwarding, so
// Docker and VPNs keep working.
var kernelSettings = [][2]string{
	{"net.ipv4.tcp_syncookies", "1"},
	{"net.ipv4.tcp_max_syn_backlog", "4096"},
	{"net.ipv4.tcp_rfc1337", "1"},
	{"net.ipv4.conf.all.accept_redirects", "0"},
	{"net.ipv4.conf.default.accept_redirects", "0"},
	{"net.ipv4.conf.all.secure_redirects", "0"},
	{"net.ipv4.conf.default.secure_redirects", "0"},
	{"net.ipv4.conf.all.send_redirects", "0"},
	{"net.ipv4.conf.default.send_redirects", "0"},
	{"net.ipv4.conf.all.accept_source_route", "0"},
	{"net.ipv4.conf.default.accept_source_route", "0"},
	{"net.ipv6.conf.all.accept_redirects", "0"},
	{"net.ipv6.conf.default.accept_redirects", "0"},
	{"net.ipv6.conf.all.accept_source_route", "0"},
	{"net.ipv6.conf.default.accept_source_route", "0"},
	{"net.ipv4.icmp_echo_ignore_broadcasts", "1"},
	{"net.ipv4.icmp_ignore_bogus_error_responses", "1"},
	{"kernel.dmesg_restrict", "1"},
	{"kernel.kptr_restrict", "1"},
}

// KernelFile renders the sysctl file.
func KernelFile() string {
	var file strings.Builder

	file.WriteString("# Managed by Ubuntu Panel — network hardening, from the Security page.\n")
	file.WriteString("# Turning it off there puts back the values these replaced.\n")

	for _, setting := range kernelSettings {
		fmt.Fprintf(&file, "%s = %s\n", setting[0], setting[1])
	}

	return file.String()
}

// KernelValues reads the current value of every setting the file sets.
func KernelValues() map[string]string {
	values := map[string]string{}

	for _, setting := range kernelSettings {
		output := strings.TrimSpace(proc.Run("sysctl -n "+setting[0]+" 2>/dev/null", 10*time.Second).Output)
		if output != "" && !strings.ContainsAny(output, "\n;") {
			values[setting[0]] = output
		}
	}

	return values
}

// KernelApplied is whether the file is on disk.
func KernelApplied() bool { return proc.Exists(kernelPath, true) }

// ApplyKernel writes the file and loads it, or removes it and puts back the
// values it replaced: deleting a sysctl file changes nothing until a reboot.
func ApplyKernel(on bool, previous map[string]string) error {
	if on {
		if err := proc.PutFile(kernelPath, KernelFile(), true); err != nil {
			return err
		}

		if result := proc.Run("sudo sysctl -p "+kernelPath+" 2>&1", time.Minute); !result.OK() {
			// A setting this kernel does not have (IPv6 disabled, a container)
			// is reported but the rest are applied; only a total failure is one.
			if !strings.Contains(result.Output, " = ") {
				return fmt.Errorf("the kernel refused the settings: %s", lastLines(result.Output, 3))
			}
		}

		return nil
	}

	proc.Run("sudo rm -f "+kernelPath, time.Minute)

	for _, setting := range kernelSettings {
		value, ok := previous[setting[0]]
		if !ok {
			continue
		}

		if _, err := strconv.Atoi(value); err != nil {
			continue
		}

		proc.Run(fmt.Sprintf("sudo sysctl -w %s=%s >/dev/null 2>&1", setting[0], value), 10*time.Second)
	}

	return nil
}

// ------------------------------------------------------------------ ping

const pingPath = "/etc/sysctl.d/61-ubuntu-panel-ping.conf"

var pingSettings = [][2]string{
	{"net.ipv4.icmp_echo_ignore_all", "1"},
	{"net.ipv6.icmp.echo_ignore_all", "1"},
}

// PingValues are the current values of what the ping switch sets.
func PingValues() map[string]string {
	values := map[string]string{}
	for _, setting := range pingSettings {
		output := strings.TrimSpace(proc.Run("sysctl -n "+setting[0]+" 2>/dev/null", 10*time.Second).Output)
		if _, err := strconv.Atoi(output); err == nil {
			values[setting[0]] = output
		}
	}
	return values
}

// PingApplied is whether the file is on disk.
func PingApplied() bool { return proc.Exists(pingPath, true) }

// ApplyNoPing has the kernel stop answering ping, or puts back what it
// answered with before. ICMP's other messages — the ones path MTU discovery
// and error reporting need — are untouched.
func ApplyNoPing(on bool, previous map[string]string) error {
	if on {
		var file strings.Builder
		file.WriteString("# Managed by Ubuntu Panel — the server does not answer ping. From the Security page.\n")
		for _, setting := range pingSettings {
			fmt.Fprintf(&file, "%s = %s\n", setting[0], setting[1])
		}
		if err := proc.PutFile(pingPath, file.String(), true); err != nil {
			return err
		}
		if result := proc.Run("sudo sysctl -p "+pingPath+" 2>&1", time.Minute); !result.OK() &&
			!strings.Contains(result.Output, " = ") {
			return fmt.Errorf("the kernel refused the setting: %s", lastLines(result.Output, 3))
		}
		return nil
	}

	proc.Run("sudo rm -f "+pingPath, time.Minute)

	for _, setting := range pingSettings {
		value := previous[setting[0]]
		if _, err := strconv.Atoi(value); err != nil {
			value = "0"
		}
		proc.Run(fmt.Sprintf("sudo sysctl -w %s=%s >/dev/null 2>&1", setting[0], value), 10*time.Second)
	}

	return nil
}
