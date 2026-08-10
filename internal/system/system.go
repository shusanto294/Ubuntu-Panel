// Package system reads the machine the panel runs on: what it is, how loaded
// it is, and what runtimes are listening on it.
//
// Reading /proc costs microseconds, so there is no sampling daemon in the
// request path: the page asks, the kernel answers. CPU usage is a delta
// between two readings, so the first call after a restart returns null for it
// and the browser simply polls again a second later.
package system

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/shusanto294/ubuntu-panel-go/internal/config"
	"github.com/shusanto294/ubuntu-panel-go/internal/proc"
)

// Metrics is one reading.
type Metrics struct {
	CPUPercent float64 // -1 when there is no previous sample
	Cores      int

	MemoryTotal     float64
	MemoryUsed      float64
	MemoryAvailable float64
	MemoryPercent   float64

	SwapTotal   float64
	SwapUsed    float64
	SwapPercent float64

	DiskTotal   float64
	DiskUsed    float64
	DiskFree    float64
	DiskPercent float64

	Load          [3]float64
	UptimeSeconds int64
	SampledAt     string
}

var cpu struct {
	sync.Mutex
	idle  uint64
	total uint64
	seen  bool
}

// Read takes a sample.
func Read() Metrics {
	metrics := Metrics{
		CPUPercent: -1,
		Cores:      cores(),
		SampledAt:  time.Now().UTC().Format(time.RFC3339),
	}

	readMemory(&metrics)
	readDisk(&metrics)
	readLoad(&metrics)
	readUptime(&metrics)
	metrics.CPUPercent = readCPU()

	return metrics
}

// Summary is the shape the dashboard's graphs read: one object per resource,
// because CPU, memory and disk answer different questions and only share a unit
// by accident.
//
// A CPU usage of null is a first reading with nothing to compare against — the
// counters in /proc/stat are cumulative — and the page simply asks again a
// second later. Sending a zero instead would draw a graph that says the machine
// was idle when it was only just asked.
func (m Metrics) Summary() map[string]any {
	usage := any(nil)
	if m.CPUPercent >= 0 {
		usage = round(m.CPUPercent)
	}

	return map[string]any{
		"cpu": map[string]any{
			"usage": usage,
			"cores": m.Cores,
		},
		"memory": map[string]any{
			"total":     m.MemoryTotal,
			"used":      m.MemoryUsed,
			"available": m.MemoryAvailable,
			"percent":   round(m.MemoryPercent),
		},
		"swap": map[string]any{
			"total":   m.SwapTotal,
			"used":    m.SwapUsed,
			"percent": round(m.SwapPercent),
		},
		"disk": map[string]any{
			"total":   m.DiskTotal,
			"used":    m.DiskUsed,
			"free":    m.DiskFree,
			"percent": round(m.DiskPercent),
		},
		"load":           []float64{m.Load[0], m.Load[1], m.Load[2]},
		"uptime_seconds": m.UptimeSeconds,
		"sampled_at":     m.SampledAt,
	}
}

func round(value float64) float64 {
	return float64(int(value*10+0.5)) / 10
}

func cores() int {
	file, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return 1
	}
	defer file.Close()

	count := 0
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "processor") {
			count++
		}
	}

	if count == 0 {
		return 1
	}

	return count
}

func readMemory(metrics *Metrics) {
	values := map[string]float64{}

	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		key, rest, found := strings.Cut(scanner.Text(), ":")
		if !found {
			continue
		}

		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}

		if parsed, err := strconv.ParseFloat(fields[0], 64); err == nil {
			values[key] = parsed * 1024 // kB in the file, bytes here
		}
	}

	metrics.MemoryTotal = values["MemTotal"]
	metrics.MemoryAvailable = values["MemAvailable"]
	metrics.MemoryUsed = metrics.MemoryTotal - metrics.MemoryAvailable

	if metrics.MemoryTotal > 0 {
		metrics.MemoryPercent = metrics.MemoryUsed / metrics.MemoryTotal * 100
	}

	metrics.SwapTotal = values["SwapTotal"]
	metrics.SwapUsed = values["SwapTotal"] - values["SwapFree"]

	if metrics.SwapTotal > 0 {
		metrics.SwapPercent = metrics.SwapUsed / metrics.SwapTotal * 100
	}
}

func readDisk(metrics *Metrics) {
	var stat syscall.Statfs_t

	if err := syscall.Statfs("/", &stat); err != nil {
		return
	}

	blockSize := float64(stat.Bsize)

	metrics.DiskTotal = float64(stat.Blocks) * blockSize
	metrics.DiskFree = float64(stat.Bavail) * blockSize

	// Used is total minus what root cannot have either: the reserved blocks
	// are neither free nor available, and counting them as free is what makes
	// a panel disagree with `df`.
	metrics.DiskUsed = metrics.DiskTotal - float64(stat.Bfree)*blockSize

	if metrics.DiskTotal > 0 {
		metrics.DiskPercent = metrics.DiskUsed / metrics.DiskTotal * 100
	}
}

func readLoad(metrics *Metrics) {
	contents, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return
	}

	fields := strings.Fields(string(contents))

	for i := 0; i < 3 && i < len(fields); i++ {
		metrics.Load[i], _ = strconv.ParseFloat(fields[i], 64)
	}
}

func readUptime(metrics *Metrics) {
	contents, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return
	}

	fields := strings.Fields(string(contents))
	if len(fields) == 0 {
		return
	}

	seconds, _ := strconv.ParseFloat(fields[0], 64)
	metrics.UptimeSeconds = int64(seconds)
}

// readCPU is a delta between two readings of /proc/stat, so the first call
// after a restart has nothing to compare against and returns -1.
func readCPU() float64 {
	contents, err := os.ReadFile("/proc/stat")
	if err != nil {
		return -1
	}

	line, _, _ := strings.Cut(string(contents), "\n")

	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return -1
	}

	var total, idle uint64

	for i, field := range fields[1:] {
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			continue
		}

		total += value

		// user nice system idle iowait irq softirq …
		if i == 3 || i == 4 {
			idle += value
		}
	}

	cpu.Lock()
	defer cpu.Unlock()

	previousIdle, previousTotal, seen := cpu.idle, cpu.total, cpu.seen

	cpu.idle, cpu.total, cpu.seen = idle, total, true

	if !seen || total <= previousTotal {
		return -1
	}

	deltaTotal := float64(total - previousTotal)
	deltaIdle := float64(idle - previousIdle)

	percent := (deltaTotal - deltaIdle) / deltaTotal * 100

	if percent < 0 {
		return 0
	}

	if percent > 100 {
		return 100
	}

	return percent
}

// ------------------------------------------------------------- host info

var publicIP struct {
	sync.Mutex
	value   string
	fetched time.Time
}

// PublicIP is this machine's public IPv4, cached for an hour.
//
// The default route's source address is right on almost every VPS, and unlike
// asking an external service it needs no network call.
func PublicIP() string {
	publicIP.Lock()
	defer publicIP.Unlock()

	if publicIP.value != "" && time.Since(publicIP.fetched) < time.Hour {
		return publicIP.value
	}

	result := proc.Run(`ip -4 route get 1.1.1.1 2>/dev/null | `+
		`awk '{for (i=1; i<=NF; i++) if ($i == "src") print $(i+1)}'`, 15*time.Second)

	if result.OK() {
		address, _, _ := strings.Cut(strings.TrimSpace(result.Output), "\n")

		if address != "" {
			publicIP.value = strings.TrimSpace(address)
			publicIP.fetched = time.Now()
		}
	}

	return publicIP.value
}

// ForgetPublicIP drops the cache, for when the address has just changed.
func ForgetPublicIP() {
	publicIP.Lock()
	defer publicIP.Unlock()

	publicIP.value = ""
}

// Hostname is what the machine calls itself.
func Hostname() string {
	name, err := os.Hostname()
	if err != nil {
		return "server"
	}

	return name
}

// OS is the pretty name, e.g. "Ubuntu 24.04.1 LTS".
func OS() string {
	contents, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "Linux"
	}

	for _, line := range strings.Split(string(contents), "\n") {
		if value, found := strings.CutPrefix(line, "PRETTY_NAME="); found {
			return strings.Trim(strings.TrimSpace(value), `"`)
		}
	}

	return "Linux"
}

// MemoryMB is total RAM. The install steps size themselves against it:
// Ubuntu's defaults assume a machine bigger than the smallest VPS this ends up
// on.
func MemoryMB() int64 {
	metrics := Metrics{}
	readMemory(&metrics)

	return int64(metrics.MemoryTotal / (1024 * 1024))
}

// InstalledPHPVersions are the versions with a running FPM socket, newest
// first.
//
// A site pointed at a version this machine does not have gets an nginx vhost
// naming a PHP-FPM socket that is not there, and every request to it is a 502
// — so the choice is worth making an informed one.
func InstalledPHPVersions() []string {
	result := proc.Run("ls /run/php/*-fpm.sock 2>/dev/null", 15*time.Second)

	versions := []string{}

	for _, version := range config.PHPVersions {
		if strings.Contains(result.Output, "php"+version+"-fpm.sock") {
			versions = append(versions, version)
		}
	}

	return versions
}
