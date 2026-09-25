package systemmetrics

import "testing"

func TestCollectorReturnsSaneMetrics(t *testing.T) {
	var collector Collector
	for _, snapshot := range []Snapshot{collector.Collect(), collector.Collect()} {
		if snapshot.MemoryTotalBytes > 0 && snapshot.MemoryUsedBytes > snapshot.MemoryTotalBytes {
			t.Fatalf("used memory %d exceeds total %d", snapshot.MemoryUsedBytes, snapshot.MemoryTotalBytes)
		}
		if snapshot.CPUPercent < 0 || snapshot.CPUPercent > 100 {
			t.Fatalf("host CPU out of range: %f", snapshot.CPUPercent)
		}
		if snapshot.ProcessCPUPercent < 0 {
			t.Fatalf("process CPU is negative: %f", snapshot.ProcessCPUPercent)
		}
		if snapshot.Goroutines < 1 {
			t.Fatalf("goroutines = %d", snapshot.Goroutines)
		}
	}
}
