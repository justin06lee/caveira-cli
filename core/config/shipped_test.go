//go:build !devserver

package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/justin06lee/caveira/core/cloud"
)

// A shipped build runs on the caveira account only: every way to name a
// model server or key of one's own, or another caveira site, is ignored,
// and an account file edited to point elsewhere still goes to caveira.app.
func TestShippedRunsOnTheAccountOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	work := t.TempDir()
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(Path(), `{"base_url":"http://localhost:11434/v1","api_key":"sk-own","model":"glm-5.2"}`)
	write(filepath.Join(work, ".env"), "CAVEIRA_BASE_URL=http://127.0.0.1:8080/v1\nCAVEIRA_API_KEY=sk-dotenv\n")
	t.Setenv("CAVEIRA_BASE_URL", "http://192.168.1.5:8000/v1")
	t.Setenv("CAVEIRA_API_KEY", "sk-env")
	t.Setenv("CAVEIRA_CLOUD_URL", "http://127.0.0.1:9999")
	t.Setenv("CAVEIRA_MODEL", "")

	s, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if s.BaseURL != "" || s.APIKey != "" || s.LocalKey != "" || s.Local() || s.OnAccount() {
		t.Fatalf("signed out, it should have nowhere to send: %+v", s)
	}
	if s.Model != "glm-5.2" {
		t.Fatalf("model %q, want the file's glm-5.2", s.Model)
	}
	if _, err := s.UseLocal("http://localhost:11434/v1"); !errors.Is(err, ErrAccountOnly) {
		t.Fatalf("UseLocal: %v", err)
	}
	if _, err := s.WithLocalKey("sk-flag"); !errors.Is(err, ErrAccountOnly) {
		t.Fatalf("WithLocalKey: %v", err)
	}
	if got := cloud.URL(); got != cloud.DefaultURL {
		t.Fatalf("CAVEIRA_CLOUD_URL was taken: %q", got)
	}

	// Signed in, with the account file's address edited to another server.
	if err := cloud.Save(cloud.Auth{URL: "http://127.0.0.1:9999", Token: "cav_tok", Email: "a@example.com"}); err != nil {
		t.Fatal(err)
	}
	s, err = Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if !s.OnAccount() || s.BaseURL != cloud.DefaultURL+"/api/v1" || s.APIKey != "cav_tok" {
		t.Fatalf("signed in, it should go to caveira.app: %+v", s)
	}
}
