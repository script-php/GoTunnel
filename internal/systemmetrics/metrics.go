package systemmetrics

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Snapshot is a point-in-time view of host and GoTunnel process resources.
// CPU values need two samples, so they are zero on the first collection.
type Snapshot struct {
	CPUPercent        float64 `json:"cpu_percent"`
	ProcessCPUPercent float64 `json:"process_cpu_percent"`
	MemoryUsedBytes   uint64  `json:"memory_used_bytes"`
	MemoryTotalBytes  uint64  `json:"memory_total_bytes"`
	ProcessRSSBytes   uint64  `json:"process_rss_bytes"`
	Goroutines        int     `json:"goroutines"`
}

type Collector struct {
	mu        sync.Mutex
	lastTotal uint64
	lastIdle  uint64
	lastProc  uint64
}

func (c *Collector) Collect() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	s := Snapshot{Goroutines: runtime.NumGoroutine()}
	total, idle, proc, ok := readCPU()
	if ok && c.lastTotal != 0 && total > c.lastTotal {
		deltaTotal := total - c.lastTotal
		deltaIdle := idle - c.lastIdle
		deltaProc := proc - c.lastProc
		s.CPUPercent = float64(deltaTotal-deltaIdle) * 100 / float64(deltaTotal)
		s.ProcessCPUPercent = float64(deltaProc) * float64(runtime.NumCPU()) * 100 / float64(deltaTotal)
	}
	if ok {
		c.lastTotal, c.lastIdle, c.lastProc = total, idle, proc
	}
	s.MemoryTotalBytes, s.MemoryUsedBytes = readMemory()
	s.ProcessRSSBytes = readRSS()
	return s
}

func readCPU() (total, idle, process uint64, ok bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, 0, false
	}
	fields := strings.Fields(strings.SplitN(string(data), "\n", 2)[0])
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, 0, false
	}
	for i, value := range fields[1:] {
		n, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return 0, 0, 0, false
		}
		total += n
		if i == 3 || i == 4 { // idle and iowait
			idle += n
		}
	}
	self, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return total, idle, 0, false
	}
	// The command name may contain spaces; fields after the final ')' are stable.
	end := strings.LastIndexByte(string(self), ')')
	if end < 0 {
		return total, idle, 0, false
	}
	procFields := strings.Fields(string(self)[end+1:])
	if len(procFields) < 13 {
		return total, idle, 0, false
	}
	user, err1 := strconv.ParseUint(procFields[11], 10, 64)
	system, err2 := strconv.ParseUint(procFields[12], 10, 64)
	return total, idle, user + system, err1 == nil && err2 == nil
}

func readMemory() (total, used uint64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	var available uint64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, _ := strconv.ParseUint(fields[1], 10, 64)
		switch fields[0] {
		case "MemTotal:":
			total = value * 1024
		case "MemAvailable:":
			available = value * 1024
		}
	}
	if total >= available {
		used = total - available
	}
	return total, used
}

func readRSS() uint64 {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * uint64(os.Getpagesize())
}

// UnixTimeMillis is shared by telemetry producers and consumers.
func UnixTimeMillis() int64 { return time.Now().UnixMilli() }
