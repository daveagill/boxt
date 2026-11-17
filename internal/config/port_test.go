package config

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestAddPort(t *testing.T) {
	path := writeTestConfig(t, DefaultYAML)
	if err := AddPort(path, PortForward{Guest: 3000, Host: 8080}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.VM.Ports) != 1 {
		t.Fatalf("ports = %#v", cfg.VM.Ports)
	}
	got := cfg.VM.Ports[0]
	if got.Guest != 3000 || got.Host != 8080 || got.HostIP != "127.0.0.1" {
		t.Fatalf("port = %#v", got)
	}
}

func TestAddPortRejectsConflictsAtomically(t *testing.T) {
	path := writeTestConfig(t, DefaultYAML)
	if err := AddPort(path, PortForward{Guest: 3000, Host: 8080}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := AddPort(path, PortForward{Guest: 3000, Host: 9090}); err == nil {
		t.Fatal("duplicate guest port accepted")
	}
	if err := AddPort(path, PortForward{Guest: 3001, Host: 8080}); err == nil {
		t.Fatal("duplicate host binding accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("rejected port modified the configuration")
	}
}

func TestAddPortValidation(t *testing.T) {
	path := writeTestConfig(t, DefaultYAML)
	cases := []struct {
		name string
		port PortForward
		want string
	}{
		{"reserved", PortForward{Guest: 22, Host: 2222}, "reserved"},
		{"guest range", PortForward{Guest: 0, Host: 8080}, "guest port"},
		{"host range", PortForward{Guest: 3000, Host: 70000}, "host port"},
		{"bad ip", PortForward{Guest: 3000, Host: 8080, HostIP: "nope"}, "invalid host IP"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := AddPort(path, tc.port)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("AddPort(%#v) = %v, want %q", tc.port, err, tc.want)
			}
		})
	}
}

func TestRemovePortByGuest(t *testing.T) {
	path := writeTestConfig(t, `version: 1
vm:
  ports:
    - guest: 3000
      host: 8080
      host_ip: 127.0.0.1
    - guest: 4000
      host: 4000
`)
	if err := RemovePortByGuest(path, 3000); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.VM.Ports) != 1 || cfg.VM.Ports[0].Guest != 4000 {
		t.Fatalf("ports = %#v", cfg.VM.Ports)
	}
	var missing *ErrPortNotFound
	if err := RemovePortByGuest(path, 3000); !errors.As(err, &missing) {
		t.Fatalf("expected ErrPortNotFound, got %v", err)
	}
}
