package security

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/shusanto294/Ubuntu-Panel/internal/proc"
)

// ------------------------------------------------------------------ PHP

// PHP hardening: what a web shell needs to be more than a file. Uploaded
// through a vulnerable plugin, a shell's first act is to run commands —
// download a miner, open a reverse shell, read other sites' files with cat.
// With the functions that start programs disabled, the same file can do none
// of that. For PHP-FPM only, so WP-CLI, Composer and cron jobs on the command
// line are untouched.

// phpHardeningName is the file, in each PHP version's FPM conf.d.
const phpHardeningName = "99-ubuntu-panel-hardening.ini"

// phpDisabled are the functions turned off: everything that runs another
// program, plus Debian's own pcntl list, which FPM should never use anyway.
var phpDisabled = []string{
	"exec", "passthru", "shell_exec", "system", "proc_open", "popen", "pcntl_exec", "pcntl_fork",
	"pcntl_signal", "pcntl_alarm", "pcntl_waitpid", "pcntl_wait", "show_source",
}

// PHPHardeningFile is the ini file.
func PHPHardeningFile() string {
	return "; Managed by Ubuntu Panel — PHP hardening, Security → Protection.\n" +
		"; For PHP-FPM only: the command line (WP-CLI, Composer, cron) is not affected.\n" +
		"disable_functions = " + strings.Join(phpDisabled, ",") + "\n" +
		"expose_php = Off\n" +
		"allow_url_include = Off\n"
}

// phpFPMDirs are the conf.d directories of every PHP-FPM installed.
func phpFPMDirs() []string {
	output := proc.Run("ls -d /etc/php/*/fpm/conf.d 2>/dev/null", 30*time.Second).Output
	return strings.Fields(output)
}

// PHPHardeningApplied is whether every PHP-FPM has the file.
func PHPHardeningApplied() bool {
	dirs := phpFPMDirs()
	for _, dir := range dirs {
		if !proc.Exists(filepath.Join(dir, phpHardeningName), true) {
			return false
		}
	}
	return len(dirs) > 0
}

// ApplyPHPHardening writes the file for every PHP-FPM, or removes it, and
// reloads each. A version that will not reload gets its old state back.
func ApplyPHPHardening(on bool) error {
	dirs := phpFPMDirs()
	if on && len(dirs) == 0 {
		return fmt.Errorf("PHP-FPM is not installed")
	}

	for _, dir := range dirs {
		version := filepath.Base(filepath.Dir(filepath.Dir(dir)))
		path := filepath.Join(dir, phpHardeningName)

		if on {
			if current, err := proc.ReadFile(path, true); err == nil && current == PHPHardeningFile() {
				continue
			}
			if err := proc.PutFile(path, PHPHardeningFile(), true); err != nil {
				return err
			}
		} else {
			if !proc.Exists(path, true) {
				continue
			}
			proc.Run("sudo rm -f "+proc.Quote(path), time.Minute)
		}

		if test := proc.Run(fmt.Sprintf("sudo php-fpm%s -t 2>&1", version), time.Minute); !test.OK() {
			if on {
				proc.Run("sudo rm -f "+proc.Quote(path), time.Minute)
			}
			return fmt.Errorf("PHP %s refused the settings: %s", version, lastLines(test.Output, 2))
		}

		proc.Run(fmt.Sprintf("sudo systemctl reload php%s-fpm 2>&1", version), time.Minute)
	}

	return nil
}

// ------------------------------------------------------------------ compilers

// Compilers for administrators only. An attacker with a shell as www-data or
// a site account often builds their exploit on the server, because a binary
// built there runs there. With the compilers and the assembler readable and
// runnable only by root and the panel, that step fails. dpkg-statoverride
// makes it survive package upgrades, which would otherwise put the modes back.

// CompilerGroup may still run them, beside root.
const CompilerGroup = "panel-compilers"

// compilerNames are looked up in /usr/bin and followed to the real binary.
var compilerNames = []string{"gcc", "g++", "cc", "c++", "as", "ld", "clang", "tcc"}

// compilerPaths are the real files behind the names, where they exist.
func compilerPaths() []string {
	script := "for n in " + strings.Join(compilerNames, " ") + "; do " +
		"p=$(command -v $n 2>/dev/null) && readlink -f \"$p\"; done | sort -u"
	return strings.Fields(proc.Run(script, 30*time.Second).Output)
}

// CompilersRestricted is whether the overrides are in place.
func CompilersRestricted() bool {
	paths := compilerPaths()
	if len(paths) == 0 {
		return false
	}
	for _, path := range paths {
		if !proc.Ok("sudo dpkg-statoverride --list " + proc.Quote(path) + " >/dev/null 2>&1") {
			return false
		}
	}
	return true
}

// RestrictCompilers puts the compilers behind CompilerGroup, which has panelUser
// in it, or takes them back to everyone.
func RestrictCompilers(on bool, panelUser string) error {
	paths := compilerPaths()

	if on {
		if len(paths) == 0 {
			return fmt.Errorf("no compiler is installed, so there is nothing to restrict")
		}

		setup := "sudo groupadd -f " + CompilerGroup
		if ValidUsername(panelUser) {
			setup += " && sudo usermod -aG " + CompilerGroup + " " + panelUser
		}
		if result := proc.Run(setup+" 2>&1", time.Minute); !result.OK() {
			return fmt.Errorf("could not create the %s group: %s", CompilerGroup, lastLines(result.Output, 2))
		}

		for _, path := range paths {
			command := fmt.Sprintf("sudo dpkg-statoverride --remove %[1]s >/dev/null 2>&1; "+
				"sudo dpkg-statoverride --update --add root %[2]s 750 %[1]s 2>&1", proc.Quote(path), CompilerGroup)
			if result := proc.Run(command, time.Minute); !result.OK() {
				return fmt.Errorf("could not restrict %s: %s", path, lastLines(result.Output, 2))
			}
		}
		return nil
	}

	for _, path := range paths {
		proc.Run(fmt.Sprintf("sudo dpkg-statoverride --remove %[1]s >/dev/null 2>&1; "+
			"sudo chown root:root %[1]s && sudo chmod 755 %[1]s", proc.Quote(path)), time.Minute)
	}

	return nil
}
