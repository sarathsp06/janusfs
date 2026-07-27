package main

import (
	"context"
	"fmt"
	"os/exec"
	"testing"
	"time"
)

func TestWatchdogTriggersUnmount(t *testing.T) {
	// 1. Start a short-lived process
	cmd := exec.Command("sleep", "0.1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start process: %v", err)
	}

	// 2. Mock the unmount command to catch the unmount call
	oldUnmount := unmountCommand
	defer func() { unmountCommand = oldUnmount }()

	unmountCalled := make(chan string, 10)
	unmountCommand = func(name string, args []string, timeoutSec int) error {
		unmountCalled <- fmt.Sprintf("%s %v", name, args)
		return nil
	}

	// Mock osExit to prevent test runner from exiting
	oldExit := osExit
	defer func() { osExit = oldExit }()
	osExit = func(code int) {}

	// 3. Start the watchdog in a goroutine
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runWatchdog(ctx, cmd.Process.Pid, "/tmp/dummy-mountpoint")

	// 4. Wait for the process to finish
	_ = cmd.Wait()

	// 5. Verify the watchdog detected the death and attempted to unmount
	select {
	case call := <-unmountCalled:
		t.Logf("watchdog triggered unmount call: %s", call)
	case <-time.After(3 * time.Second):
		t.Errorf("expected watchdog to trigger unmount, but it timed out")
	}
}
