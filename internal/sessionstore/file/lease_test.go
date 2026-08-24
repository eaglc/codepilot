package file

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestStateLeaseRejectsSecondWriterAndPreservesDiagnostics(t *testing.T) {
	root := t.TempDir()
	first, err := AcquireStateLease(root)
	if err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}
	owner := first.Owner()
	if owner.PID != os.Getpid() || owner.OwnerID == "" || owner.AcquiredAt.IsZero() || owner.ReleasedAt != nil {
		t.Fatalf("first owner = %#v", owner)
	}
	second, err := AcquireStateLease(root)
	if second != nil || !errors.Is(err, ErrStateInUse) {
		t.Fatalf("second lease = %#v, err = %v", second, err)
	}
	var inUse *LeaseInUseError
	if !errors.As(err, &inUse) || inUse.Owner.OwnerID != owner.OwnerID || inUse.Owner.PID != owner.PID {
		t.Fatalf("in-use diagnostics = %#v", inUse)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("release first lease: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("release first lease twice: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".codepilot-writer.lock")); err != nil {
		t.Fatalf("lock marker was deleted: %v", err)
	}
	third, err := AcquireStateLease(root)
	if err != nil {
		t.Fatalf("acquire lease after release: %v", err)
	}
	if third.Owner().OwnerID == owner.OwnerID {
		t.Fatal("new lease reused the previous owner identity")
	}
	if err := third.Close(); err != nil {
		t.Fatalf("release third lease: %v", err)
	}
}

func TestStateLeaseIsReleasedByOperatingSystemAfterAbnormalExit(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(t.TempDir(), "lease-ready")
	command := exec.Command(os.Args[0], "-test.run=^TestStateLeaseHelperProcess$")
	command.Env = append(os.Environ(),
		"CODEPILOT_LEASE_HELPER=1",
		"CODEPILOT_LEASE_ROOT="+root,
		"CODEPILOT_LEASE_MARKER="+marker,
	)
	if err := command.Start(); err != nil {
		t.Fatalf("start lease helper: %v", err)
	}
	defer func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lease helper did not acquire the lock")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if lease, err := AcquireStateLease(root); lease != nil || !errors.Is(err, ErrStateInUse) {
		t.Fatalf("parent acquired live child lease: lease=%#v err=%v", lease, err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatalf("kill lease helper: %v", err)
	}
	_ = command.Wait()
	command.Process = nil
	deadline = time.Now().Add(5 * time.Second)
	for {
		lease, err := AcquireStateLease(root)
		if err == nil {
			if closeErr := lease.Close(); closeErr != nil {
				t.Fatalf("release post-crash lease: %v", closeErr)
			}
			break
		}
		if !errors.Is(err, ErrStateInUse) || time.Now().After(deadline) {
			t.Fatalf("lease did not recover after child exit: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStateLeaseHelperProcess(t *testing.T) {
	if os.Getenv("CODEPILOT_LEASE_HELPER") != "1" {
		return
	}
	lease, err := AcquireStateLease(os.Getenv("CODEPILOT_LEASE_ROOT"))
	if err != nil {
		os.Exit(41)
	}
	if err := os.WriteFile(os.Getenv("CODEPILOT_LEASE_MARKER"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		os.Exit(42)
	}
	_ = lease
	select {}
}
