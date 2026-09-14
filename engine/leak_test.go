package engine

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// TestTagFileReloadNoGoroutineLeak verifies that repeatedly reloading a
// driver's tags-file does not leak watcher goroutines (H7). Before the
// fix, each reload started a new watcher whose goroutine could never be
// stopped, growing goroutines without bound.
func TestTagFileReloadNoGoroutineLeak(t *testing.T) {
	registerMockTypes()

	dir := t.TempDir()
	path := filepath.Join(dir, "tags.yaml")

	initial := `
- name: temperature
  address: "40001"
  type: float32
  interval: 100ms
`
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	eng := New()
	cfg := &core.Config{
		Drivers: []core.DriverConfig{
			{
				Name:         "watched",
				Type:         "mock-state",
				TagsFile:     path,
				TagsInterval: "100ms",
				Tags:         []core.TagConfig{{Name: "temperature", Interval: "100ms"}},
			},
		},
		Transports: []core.TransportConfig{{Name: "t1", Type: "mock-data"}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx, cfg); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer eng.Stop()

	// Let the watcher settle.
	time.Sleep(300 * time.Millisecond)

	runtime.GC()
	baseGoroutines := runtime.NumGoroutine()

	// Trigger many reloads by rewriting the file with a changing tag count.
	for i := 0; i < 10; i++ {
		content := "- name: temperature\n  address: \"40001\"\n  type: float32\n  interval: 100ms\n"
		for j := 0; j <= i; j++ {
			content += "- name: extra" + itoa(j) + "\n  address: \"40" + itoa2(j) + "0\"\n  type: float32\n  interval: 100ms\n"
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	runtime.GC()
	finalGoroutines := runtime.NumGoroutine()

	leaked := finalGoroutines - baseGoroutines
	t.Logf("goroutines: base=%d final=%d delta=%d", baseGoroutines, finalGoroutines, leaked)
	if leaked > 3 {
		t.Errorf("goroutine leak: started=%d, after %d reloads=%d (delta=%d)", baseGoroutines, 10, finalGoroutines, leaked)
	}
}

func itoa(j int) string {
	return string(rune('0' + j))
}
func itoa2(j int) string {
	return string(rune('1'+j)) + "0"
}
