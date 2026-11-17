package cli

import (
	"context"
	"strings"
	"testing"
)

func TestParsePortSpec(t *testing.T) {
	cases := []struct {
		spec  string
		guest int
		host  int
		ip    string
		bad   bool
	}{
		{spec: "3000", guest: 3000, host: 3000, ip: "127.0.0.1"},
		{spec: "3000:8080", guest: 3000, host: 8080, ip: "127.0.0.1"},
		{spec: "3000:8080@10.0.0.5", guest: 3000, host: 8080, ip: "10.0.0.5"},
		{spec: "3000@::1", guest: 3000, host: 3000, ip: "::1"},
		{spec: "abc", bad: true},
		{spec: "3000:", bad: true},
		{spec: "3000:abc", bad: true},
		{spec: "", bad: true},
		{spec: "3000:8080@nope", bad: true},
	}
	for _, tc := range cases {
		t.Run(tc.spec, func(t *testing.T) {
			port, err := parsePortSpec(tc.spec)
			if tc.bad {
				if err == nil {
					t.Fatalf("parsePortSpec(%q) succeeded", tc.spec)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePortSpec(%q): %v", tc.spec, err)
			}
			if port.Guest != tc.guest || port.Host != tc.host || port.HostIP != tc.ip {
				t.Fatalf("parsePortSpec(%q) = %#v", tc.spec, port)
			}
		})
	}
}

func TestPortAddListRemove(t *testing.T) {
	app, _, out, errOut := newConfigFixture(t)
	if code := Run(context.Background(), app, []string{"port", "add", "3000:8080"}); code != 0 {
		t.Fatalf("add exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "recreate") {
		t.Fatalf("recreate hint missing:\n%s", out.String())
	}

	out.Reset()
	if code := Run(context.Background(), app, []string{"port", "list"}); code != 0 {
		t.Fatalf("list exit %d: %s", code, errOut.String())
	}
	for _, want := range []string{"Mode:", "auto", "3000", "8080", "127.0.0.1"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("list missing %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	if code := Run(context.Background(), app, []string{"port", "remove", "3000"}); code != 0 {
		t.Fatalf("remove exit %d: %s", code, errOut.String())
	}
	out.Reset()
	if code := Run(context.Background(), app, []string{"port", "list"}); code != 0 {
		t.Fatalf("list exit %d: %s", code, errOut.String())
	}
	if strings.Contains(out.String(), "8080") {
		t.Fatalf("removed port still listed:\n%s", out.String())
	}
}

func TestPortListExplicitGuestOnly(t *testing.T) {
	app, _, out, errOut := newConfigFixture(t)
	if code := Run(context.Background(), app, []string{"config", "set", "vm.port_forwarding", "explicit"}); code != 0 {
		t.Fatalf("mode exit %d: %s", code, errOut.String())
	}
	if code := Run(context.Background(), app, []string{"target", "add", "dev", "--port-base", "5000", "--", "serve"}); code != 0 {
		t.Fatalf("target exit %d: %s", code, errOut.String())
	}
	out.Reset()
	if code := Run(context.Background(), app, []string{"port", "list"}); code != 0 {
		t.Fatalf("list exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "guest-only (explicit)") {
		t.Fatalf("guest-only marker missing:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "5000") {
		t.Fatalf("effective port missing:\n%s", out.String())
	}
}

func TestPortRemoveUnknownAndInvalid(t *testing.T) {
	app, _, _, errOut := newConfigFixture(t)
	if code := Run(context.Background(), app, []string{"port", "remove", "3000"}); code != 1 {
		t.Fatalf("unknown remove exit = %d", code)
	}
	if !strings.Contains(errOut.String(), "not forwarded") {
		t.Fatalf("unexpected error:\n%s", errOut.String())
	}
	errOut.Reset()
	if code := Run(context.Background(), app, []string{"port", "remove", "abc"}); code != 1 {
		t.Fatalf("invalid remove exit = %d", code)
	}
	if !strings.Contains(errOut.String(), "invalid guest port") {
		t.Fatalf("unexpected error:\n%s", errOut.String())
	}
}
