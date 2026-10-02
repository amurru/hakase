package channel

import (
	"path/filepath"
	"testing"
)

func TestServiceDriverLabels(t *testing.T) {
	svc, err := NewService(Deps{StatePath: filepath.Join(t.TempDir(), "channels.json")})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	// Default driver keeps the telegram label (byte-identical behavior).
	if got := svc.Driver(); got == nil {
		t.Fatal("default driver must exist")
	}
	if got := svc.DriverFor("telegram"); got != svc.Driver() {
		t.Error("DriverFor(telegram) must return the default driver")
	}
	// A second transport gets its own labeled driver, stable across calls.
	d1 := svc.DriverFor("discord")
	d2 := svc.DriverFor("discord")
	if d1 == nil || d1 != d2 {
		t.Fatal("DriverFor(discord) must be stable and non-nil")
	}
	if d1 == svc.Driver() {
		t.Error("discord driver must differ from the telegram driver")
	}
}
