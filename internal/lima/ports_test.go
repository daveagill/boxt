package lima

import (
	"errors"
	"net"
	"testing"

	"github.com/boxt/boxt/internal/config"
)

func TestCheckPortAvailable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := CheckPortAvailable("127.0.0.1", port); err == nil {
		ln.Close()
		t.Fatal("expected conflict for occupied port")
	} else {
		var conflict *PortConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("expected PortConflictError, got %T: %v", err, err)
		}
		if conflict.Port != port {
			t.Fatalf("port = %d, want %d", conflict.Port, port)
		}
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if err := CheckPortAvailable("127.0.0.1", port); err != nil {
		t.Fatalf("free port reported conflict: %v", err)
	}
}

func TestCheckPorts(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	defer ln.Close()
	err = CheckPorts([]config.PortForward{
		{Guest: 81, Host: port, HostIP: "127.0.0.1"},
	})
	if err == nil {
		t.Fatal("expected conflict")
	}
	var conflict *PortConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected PortConflictError, got %T", err)
	}
	if conflict.HostIP != "127.0.0.1" || conflict.Port != port {
		t.Fatalf("unexpected conflict: %#v", conflict)
	}
}
