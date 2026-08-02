//go:build !windows

package safeopen

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const fifoOpenDeadline = 300 * time.Millisecond

func TestBoundRootFileOpensRejectFIFOWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "session.json")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	bound, err := Bind(root)
	if err != nil {
		t.Fatal(err)
	}
	defer bound.Close()

	tests := []struct {
		name string
		open func() error
	}{
		{name: "Open", open: func() error {
			file, err := bound.Open("session.json", 1024)
			if file != nil {
				file.Close()
			}
			return err
		}},
		{name: "OpenWithPathIdentity", open: func() error {
			file, _, err := bound.OpenWithPathIdentity("session.json", 1024)
			if file != nil {
				file.Close()
			}
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err, blocked, recoveryErr := awaitFIFOOpen(fifo, test.open)
			if recoveryErr != nil {
				t.Fatal(recoveryErr)
			}
			if blocked {
				t.Fatalf("open blocked until a writer connected; eventual err=%v", err)
			}
			if err == nil || err.Error() != "safeopen: invalid source file" {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestFIFORejectionDoesNotLeakDescriptors(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "session.json")
	regular := filepath.Join(root, "regular.json")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(regular, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	bound, err := Bind(root)
	if err != nil {
		t.Fatal(err)
	}
	defer bound.Close()
	before, counted := openDescriptorCount()
	for i := 0; i < 1024; i++ {
		open := func() error {
			if i%2 == 0 {
				file, err := bound.Open("session.json", 1024)
				if file != nil {
					file.Close()
				}
				return err
			}
			file, _, err := bound.OpenWithPathIdentity("session.json", 1024)
			if file != nil {
				file.Close()
			}
			return err
		}
		err, blocked, recoveryErr := awaitFIFOOpen(fifo, open)
		if recoveryErr != nil {
			t.Fatal(recoveryErr)
		}
		if blocked {
			t.Fatalf("iteration %d blocked until a writer connected", i)
		}
		if err == nil || err.Error() != "safeopen: invalid source file" {
			t.Fatalf("iteration %d err=%v", i, err)
		}
	}
	if counted {
		after, ok := openDescriptorCount()
		if !ok {
			t.Fatal("descriptor count disappeared")
		}
		if delta := after - before; delta > 8 {
			t.Fatalf("open descriptors grew by %d", delta)
		}
	}
	file, err := bound.Open("regular.json", 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil || string(data) != "safe" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

func TestRegularFileOpensReturnBlockingDescriptorsAndIdentity(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "parent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "parent", "session.json"), []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	bound, err := Bind(root)
	if err != nil {
		t.Fatal(err)
	}
	defer bound.Close()
	want, err := bound.PathIdentity("parent")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		open func() (*os.File, []Identity, error)
	}{
		{name: "Open", open: func() (*os.File, []Identity, error) {
			file, err := bound.Open(filepath.Join("parent", "session.json"), 1024)
			return file, nil, err
		}},
		{name: "OpenWithPathIdentity", open: func() (*os.File, []Identity, error) {
			return bound.OpenWithPathIdentity(filepath.Join("parent", "session.json"), 1024)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			file, identities, err := test.open()
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
			if err != nil {
				t.Fatal(err)
			}
			if flags&unix.O_NONBLOCK != 0 {
				t.Fatalf("returned flags=%#x", flags)
			}
			data, err := io.ReadAll(file)
			if err != nil || string(data) != "safe" {
				t.Fatalf("data=%q err=%v", data, err)
			}
			if test.name == "OpenWithPathIdentity" {
				if len(identities) != len(want) {
					t.Fatalf("identities=%v want=%v", identities, want)
				}
				for i := range want {
					if identities[i] != want[i] {
						t.Fatalf("identities=%v want=%v", identities, want)
					}
				}
			}
		})
	}
}

func awaitFIFOOpen(fifo string, open func() error) (error, bool, error) {
	result := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		result <- open()
	}()
	<-started
	timer := time.NewTimer(fifoOpenDeadline)
	defer timer.Stop()
	select {
	case err := <-result:
		return err, false, nil
	case <-timer.C:
	}
	writer, err := unix.Open(fifo, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, true, fmt.Errorf("open fallback FIFO writer: %w", err)
	}
	_ = unix.Close(writer)
	select {
	case err := <-result:
		return err, true, nil
	case <-time.After(2 * time.Second):
		return nil, true, errors.New("FIFO open remained blocked after fallback writer")
	}
}

func openDescriptorCount() (int, bool) {
	for _, path := range []string{"/proc/self/fd", "/dev/fd"} {
		entries, err := os.ReadDir(path)
		if err == nil {
			return len(entries), true
		}
	}
	return 0, false
}
