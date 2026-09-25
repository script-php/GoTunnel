package main

import (
	"strings"
	"testing"
)

func TestRenderServiceQuotesArgumentsAndHardensProcess(t *testing.T) {
	unit := renderService("server", "/opt/Go Tunnel/gotunnel", []string{
		"server",
		"-password",
		"spaces and %n specifier",
		"-config",
		"config file.json",
	})
	for _, want := range []string{
		`ExecStart="/opt/Go Tunnel/gotunnel" "server" "-password" "spaces and %%n specifier" "-config" "config file.json"`,
		"DynamicUser=yes",
		"WorkingDirectory=/var/lib/gotunnel",
		"NoNewPrivileges=yes",
		"CapabilityBoundingSet=CAP_NET_BIND_SERVICE",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("service missing %q:\n%s", want, unit)
		}
	}
}

func TestFilterArgsPreservesArgumentBoundaries(t *testing.T) {
	got := filterArgs([]string{"-password", "a password", "-register", "-name", "machine"})
	want := []string{"-password", "a password", "-name", "machine"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("filterArgs() = %#v, want %#v", got, want)
	}
}
