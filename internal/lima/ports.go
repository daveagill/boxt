package lima

import (
	"fmt"
	"net"
	"strconv"

	"github.com/boxt/boxt/internal/config"
)

// PortConflictError reports a requested host port that cannot be bound.
// boxt never silently chooses another port.
type PortConflictError struct {
	HostIP string
	Port   int
	Cause  error
}

func (e *PortConflictError) Error() string {
	return fmt.Sprintf("host port %d is unavailable on %s: %v", e.Port, e.HostIP, e.Cause)
}

func (e *PortConflictError) Unwrap() error { return e.Cause }

// Hint explains how to resolve the conflict.
func (e *PortConflictError) Hint() string {
	return fmt.Sprintf("Host port %d is already in use.\n\nChoose another port or stop the process using that port.", e.Port)
}

// CheckPortAvailable verifies that boxt can bind the requested host address.
// The listener is closed immediately; Lima binds it again when starting.
func CheckPortAvailable(hostIP string, port int) error {
	if hostIP == "" {
		hostIP = "127.0.0.1"
	}
	addr := net.JoinHostPort(hostIP, strconv.Itoa(port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return &PortConflictError{HostIP: hostIP, Port: port, Cause: err}
	}
	return ln.Close()
}

// CheckPorts verifies every requested forward before the VM is started.
func CheckPorts(ports []config.PortForward) error {
	for _, p := range ports {
		if err := CheckPortAvailable(p.HostIP, p.Host); err != nil {
			return err
		}
	}
	return nil
}
