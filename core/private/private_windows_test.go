package private

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The sign-in token must not be readable by the other accounts on the
// machine. A file made in a folder under the profile inherits that
// folder's access list, which names Administrators and SYSTEM besides the
// user; the file WriteFile makes has the user alone and inherits nothing.
func TestWriteFileIsForItsOwnerAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := WriteFile(path, []byte("secret")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "secret" {
		t.Fatalf("content %q", b)
	}
	// A second write replaces it and keeps it private.
	if err := WriteFile(path, []byte("secret2")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error("the temporary file is left behind")
	}

	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	ctl, _, _ := sd.Control()
	if ctl&windows.SE_DACL_PROTECTED == 0 {
		t.Error("the access list still inherits from the folder")
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		t.Fatalf("no access list: %v", err)
	}
	if acl.AceCount != 1 {
		t.Fatalf("%d entries in the access list, want 1", acl.AceCount)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(acl, 0, &ace); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)); !sid.Equals(user.User.Sid) {
		t.Errorf("the one entry is for %v, not the user %v", sid, user.User.Sid)
	}
}
