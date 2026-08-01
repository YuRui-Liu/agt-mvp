//go:build windows

package mocksvc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWindowsFileStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "state.json")
	store, err := OpenFileStore(path)
	if err != nil {
		t.Fatalf("open file store: %v", err)
	}
	identity := Identity{
		SubjectID: "sub-windows",
		KuAIID:    "KUAI-WINDOWS234",
		CreatedAt: time.Date(2026, 7, 29, 9, 30, 0, 0, time.UTC),
	}
	if err := store.Put(identity); err != nil {
		t.Fatalf("put: %v", err)
	}
	reopened, err := OpenFileStore(path)
	if err != nil {
		t.Fatalf("reopen file store: %v", err)
	}
	got, ok, err := reopened.Get(identity.SubjectID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !ok || got != identity {
		t.Fatalf("Get = (%#v, %v), want (%#v, true)", got, ok, identity)
	}
}

func TestWindowsFileStoreCreatesPrivateACL(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "private")
	if _, err := OpenFileStore(filepath.Join(parent, "state.json")); err != nil {
		t.Fatalf("open file store: %v", err)
	}
	if err := validateWindowsDirectoryACL(parent); err != nil {
		t.Fatalf("new directory ACL is not private: %v", err)
	}
}

func TestWindowsFileStoreRejectsBroadExistingACL(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatalf("mkdir shared directory: %v", err)
	}
	if err := setWindowsDirectoryDACL(parent, "D:P(A;OICI;FA;;;WD)"); err != nil {
		t.Fatalf("set broad ACL: %v", err)
	}
	if _, err := OpenFileStore(filepath.Join(parent, "state.json")); err == nil {
		t.Fatal("OpenFileStore accepted Everyone full-access ACL")
	}
}

func TestWindowsFileStoreRejectsUnsafeExistingACLs(t *testing.T) {
	tests := []struct {
		name string
		sddl string
	}{
		{
			name: "specific other SID full access",
			sddl: "D:P(A;OICI;FA;;;OW)(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;BU)",
		},
		{
			name: "Everyone can change DACL",
			sddl: "D:P(A;OICI;FA;;;OW)(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;;WD;;;WD)",
		},
		{
			name: "unprotected DACL",
			sddl: "D:(A;OICI;FA;;;OW)(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := filepath.Join(t.TempDir(), "unsafe")
			if err := os.Mkdir(parent, 0o700); err != nil {
				t.Fatalf("mkdir unsafe directory: %v", err)
			}
			if err := setWindowsDirectoryDACL(parent, test.sddl); err != nil {
				t.Fatalf("set unsafe ACL: %v", err)
			}
			if _, err := OpenFileStore(filepath.Join(parent, "state.json")); err == nil {
				t.Fatalf("OpenFileStore accepted ACL %q", test.sddl)
			}
		})
	}
}

func TestValidateWindowsOwnerSIDRejectsUntrustedOwnerWithOwnerRights(t *testing.T) {
	currentUser := []byte("current-user")
	localSystem := []byte("local-system")
	administrators := []byte("administrators")
	otherOwner := []byte("specific-other-owner")

	err := validateWindowsOwnerSID(
		otherOwner,
		[][]byte{currentUser, localSystem, administrators},
	)
	if err == nil {
		t.Fatal("validateWindowsOwnerSID accepted an untrusted actual owner")
	}
	if !strings.Contains(err.Error(), "unapproved owner SID") {
		t.Fatalf("validateWindowsOwnerSID error = %q, want unapproved owner SID", err)
	}
}
