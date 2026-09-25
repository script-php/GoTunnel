package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadInitializesMissingMachinesAndPreservesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"server":{"port":9000,"panel_port":9001}}`), 0600); err != nil {
		t.Fatal(err)
	}
	m := NewManager(path)
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	if got := m.GetAllMachines(); got == nil || len(got) != 0 {
		t.Fatalf("machines = %#v, want initialized empty map", got)
	}
}

func TestLoadRejectsDuplicateRemotePorts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"server":{"port":7727,"panel_port":7726},"machines":{"one":{"tunnels":[{"remote":8000,"local":80}]},"two":{"tunnels":[{"remote":8000,"local":81}]}}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	err := NewManager(path).Load()
	if err == nil || !strings.Contains(err.Error(), "duplicate remote port") {
		t.Fatalf("Load() error = %v, want duplicate remote port", err)
	}
}

func TestGettersReturnDeepCopies(t *testing.T) {
	m := NewManager("")
	m.InitDefault()
	want := MachineConfig{Tunnels: []TunnelConfig{{Remote: 8000, Local: 80}}}
	if err := m.AddOrUpdateMachine("machine", &want); err != nil {
		t.Fatal(err)
	}

	want.Tunnels[0].Local = 9999
	got := m.GetMachine("machine")
	got.Tunnels[0].Local = 7777
	all := m.GetAllMachines()
	all["machine"].Tunnels[0].Local = 6666

	if local := m.GetMachine("machine").Tunnels[0].Local; local != 80 {
		t.Fatalf("stored local port mutated through caller: got %d", local)
	}
}

func TestAddTunnelRejectsDuplicateRemotePort(t *testing.T) {
	m := NewManager("")
	m.InitDefault()
	if err := m.AddTunnelPorts("one", 8000, 80); err != nil {
		t.Fatal(err)
	}
	if err := m.AddTunnelPorts("two", 8000, 81); err == nil || !strings.Contains(err.Error(), "duplicate remote port") {
		t.Fatalf("AddTunnelPorts() error = %v, want duplicate remote port", err)
	}
	if got := m.GetMachine("two"); got != nil {
		t.Fatalf("rejected machine was stored: %#v", got)
	}
}

func TestAddTunnelRollsBackWhenSaveFails(t *testing.T) {
	path := t.TempDir() // Renaming a file over this directory must fail.
	m := NewManager(path)
	m.InitDefault()
	if err := m.AddTunnelPorts("machine", 8000, 80); err == nil {
		t.Fatal("AddTunnelPorts() unexpectedly succeeded")
	}
	if got := m.GetMachine("machine"); got != nil {
		t.Fatalf("failed addition remained in memory: %#v", got)
	}
}

func TestSavedConfigUsesPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m := NewManager(path)
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("config permissions = %o, want 600", got)
	}
}
