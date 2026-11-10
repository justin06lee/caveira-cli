package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justin06lee/caveira/core/cloud"
)

// clearEnv empties every variable Load reads, and the ones it used to.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, v := range append(append([]string{baseURLVar, "CAVEIRA_CONFIRM", "CAVEIRA_API_KEY"}, modelVars...), effortVars...) {
		t.Setenv(v, "")
	}
}

func TestLoadFindsEnvFileUpTheTree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	clearEnv(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env.local"), []byte("# comment\nexport CAVEIRA_BASE_URL=\"http://localhost:11434/v1/\"\nCAVEIRA_MODEL=qwen3:8b # trailing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, ".env"), []byte("CAVEIRA_REASONING_EFFORT=high\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := Load(sub)
	if err != nil {
		t.Fatal(err)
	}
	if s.BaseURL != "http://localhost:11434/v1" || !s.Local() || s.APIKey != "" {
		t.Fatalf("local server: %+v", s)
	}
	if s.Model != "qwen3:8b" || s.ReasoningEffort != "high" {
		t.Fatalf("model %q effort %q", s.Model, s.ReasoningEffort)
	}

	// Environment beats files; config.json sits under them.
	if err := Save(Settings{Model: "cfg-model", BaseURL: "http://192.168.1.20:8080/v1"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAVEIRA_MODEL", "env-model")
	t.Setenv("CAVEIRA_BASE_URL", "http://127.0.0.1:1234/v1")
	s, err = Load(sub)
	if err != nil {
		t.Fatal(err)
	}
	if s.Model != "env-model" || s.BaseURL != "http://127.0.0.1:1234/v1" {
		t.Fatalf("precedence: %+v", s)
	}
}

func TestNoKeyIsRead(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	clearEnv(t)
	t.Setenv("OLD_API_KEY", "ak_env")
	t.Setenv("CAVEIRA_API_KEY", "ck_env")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("OLD_KEY=ak_file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// An older caveira's config.json, with a key.
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte(`{"api_key":"ak_cfg","model":"m"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.APIKey != "" || s.BaseURL != "" || s.Local() || s.OnAccount() || s.Model != "m" {
		t.Fatalf("signed out, with keys lying around: %+v", s)
	}

	// Saving the file again leaves the old key out of it.
	file, err := ReadFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(file); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path())
	if strings.Contains(string(b), "ak_cfg") {
		t.Fatalf("the key was written back: %s", b)
	}
}

func TestHostedBaseURLIsRefused(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	clearEnv(t)
	if err := cloud.Save(cloud.Auth{URL: "https://site.test", Token: "tok", Email: "a@b.c"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAVEIRA_BASE_URL", "https://openrouter.ai/api/v1")

	s, err := Load(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "CAVEIRA_BASE_URL: openrouter.ai is not on this machine or your network") {
		t.Fatalf("error %v", err)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Fatalf("not one line: %q", err)
	}
	// The hosted server gets nothing; the account is still there.
	if !s.OnAccount() || s.BaseURL != "https://site.test/api/v1" {
		t.Fatalf("settings: %+v", s)
	}
}

func TestSpecAndEffort(t *testing.T) {
	if Spec("qwen-3.5").ContextWindow != 262_144 || Spec("nope").ContextWindow != 128_000 {
		t.Fatal("spec")
	}
	if !ValidEffort("max") || ValidEffort("turbo") {
		t.Fatal("effort")
	}
}

func TestSignedInAccountCarriesTheRequests(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	clearEnv(t)
	dir := t.TempDir()

	s, _ := Load(dir)
	if s.OnAccount() || s.Local() || s.APIKey != "" || s.BaseURL != "" {
		t.Fatalf("signed out: %+v", s)
	}

	if err := cloud.Save(cloud.Auth{URL: "https://site.test", Token: "tok", Email: "a@b.c"}); err != nil {
		t.Fatal(err)
	}
	s, _ = Load(dir)
	if !s.OnAccount() || s.Local() || s.APIKey != "tok" || s.BaseURL != "https://site.test/api/v1" {
		t.Fatalf("signed in: %+v", s)
	}

	// --base-url takes over from the account, and the token stays behind.
	l, err := s.UseLocal("http://localhost:8080/v1/")
	if err != nil || !l.Local() || l.APIKey != "" || l.BaseURL != "http://localhost:8080/v1" {
		t.Fatalf("--base-url: %+v %v", l, err)
	}
	if _, err := s.UseLocal("https://api.openai.com/v1"); err == nil {
		t.Fatal("--base-url took a hosted server")
	}

	// A local server chosen on purpose is not taken over by the account.
	t.Setenv("CAVEIRA_BASE_URL", "http://localhost:11434/v1")
	s, _ = Load(dir)
	if s.OnAccount() || !s.Local() || s.APIKey != "" || s.BaseURL != "http://localhost:11434/v1" {
		t.Fatalf("own server: %+v", s)
	}
}

// A local server can have a key of its own, from config.json, a .env
// file, the environment or --api-key. It goes to that server only: with
// the account or a hosted base URL it is ignored, and it is not saved
// without a local base URL.
func TestLocalServerKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	clearEnv(t)
	dir := t.TempDir()
	if err := cloud.Save(cloud.Auth{URL: "https://site.test", Token: "tok", Email: "a@b.c"}); err != nil {
		t.Fatal(err)
	}

	// No local server: the key stays put and the account carries on.
	t.Setenv("CAVEIRA_API_KEY", "local-key")
	s, _ := Load(dir)
	if !s.OnAccount() || s.APIKey != "tok" {
		t.Fatalf("key without a local server: %+v", s)
	}
	if _, err := s.WithLocalKey("k"); err == nil {
		t.Fatal("--api-key went to the account")
	}

	// A hosted base URL is refused, and so the key goes nowhere.
	t.Setenv("CAVEIRA_BASE_URL", "https://openrouter.ai/api/v1")
	s, err := Load(dir)
	if err == nil || s.APIKey != "tok" || !s.OnAccount() {
		t.Fatalf("hosted with key: %+v %v", s, err)
	}

	// A local base URL gets it.
	t.Setenv("CAVEIRA_BASE_URL", "http://localhost:8080/v1")
	s, _ = Load(dir)
	if !s.Local() || s.APIKey != "local-key" || s.KeySource != "CAVEIRA_API_KEY" {
		t.Fatalf("local with env key: %+v", s)
	}
	k, err := s.WithLocalKey("flag")
	if err != nil || k.APIKey != "flag" || !k.Local() {
		t.Fatalf("--api-key: %+v %v", k, err)
	}

	// From config.json, and --base-url keeps it while dropping the token.
	t.Setenv("CAVEIRA_API_KEY", "")
	t.Setenv("CAVEIRA_BASE_URL", "")
	os.WriteFile(Path(), []byte(`{"base_url":"http://192.168.1.5:11434/v1","api_key":"cfg-key"}`), 0o600)
	s, _ = Load(dir)
	if !s.Local() || s.APIKey != "cfg-key" {
		t.Fatalf("config key: %+v", s)
	}
	os.WriteFile(Path(), []byte(`{"api_key":"cfg-key"}`), 0o600)
	s, _ = Load(dir)
	l, err := s.UseLocal("http://localhost:1234/v1")
	if err != nil || l.APIKey != "cfg-key" || !l.Local() {
		t.Fatalf("--base-url with config key: %+v %v", l, err)
	}

	// Saved with a local server it stays; without one it goes.
	file, _ := ReadFile()
	file.BaseURL = "http://localhost:1234/v1"
	Save(file)
	if b, _ := os.ReadFile(Path()); !strings.Contains(string(b), "cfg-key") {
		t.Fatalf("local key dropped: %s", b)
	}
	file.BaseURL = ""
	Save(file)
	if b, _ := os.ReadFile(Path()); strings.Contains(string(b), "cfg-key") {
		t.Fatalf("key saved with nowhere to go: %s", b)
	}
}

func TestSoftTokenLimitFromEnv(t *testing.T) {
	for v, want := range map[string]int{"200000": 200_000, "300_000": 300_000, " 150000 ": 150_000, "": 0, "lots": 0, "-5": 0} {
		var s Settings
		applyEnv(&s, func(name string) string {
			if name == "CAVEIRA_SOFT_TOKEN_LIMIT" {
				return v
			}
			return ""
		})
		if s.SoftTokenLimit != want {
			t.Errorf("CAVEIRA_SOFT_TOKEN_LIMIT=%q gave %d, want %d", v, s.SoftTokenLimit, want)
		}
	}
}
