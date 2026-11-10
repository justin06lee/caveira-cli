//go:build devserver

package config

import (
	"strings"
	"testing"
)

func TestCheckLocal(t *testing.T) {
	local := []string{
		"http://localhost:11434/v1",
		"http://LOCALHOST:11434/v1",
		"http://localhost.:11434",
		"http://127.0.0.1:1234/v1",
		"http://127.1.2.3:8000",
		"http://[::1]:8080/v1",
		"http://0.0.0.0:8000/v1",
		"http://10.0.0.5:8000/v1",
		"http://172.16.0.1/v1",
		"http://172.31.255.254:5000",
		"http://192.168.1.20:11434/v1",
		"https://192.168.1.20/v1",
		"http://[fd12:3456::1]:8000",
		"http://gpu-box.local:11434/v1",
		"http://[::ffff:192.168.0.2]:8000",
		"  http://localhost:11434/v1  ",
	}
	for _, u := range local {
		if err := CheckLocal(u); err != nil {
			t.Errorf("%s: %v", u, err)
		}
		if !IsLocal(u) {
			t.Errorf("%s: not local", u)
		}
	}

	hosted := []string{
		"https://api.example.com/v1",
		"https://api.openai.com/v1",
		"https://openrouter.ai/api/v1",
		"http://8.8.8.8/v1",
		"http://172.32.0.1/v1",        // just past 172.16/12
		"http://172.15.255.255/v1",    // just before it
		"http://192.169.0.1/v1",       // not 192.168
		"http://11.0.0.1/v1",          // not 10/8
		"http://[2001:4860::8888]/v1", // public v6
		"http://[::ffff:8.8.8.8]/v1",
		"http://localhost.evil.com/v1",
		"http://local/v1",
		"http://.local/v1",
		"http://evil.local.example.com/v1",
		"http://127.0.0.1.nip.io/v1",
		"http://2130706433/v1", // 127.0.0.1 as one number: a name, not looked up
		"http://localhost@evil.com/v1",
		"http://gpu-box:8000/v1", // a bare name could be anywhere
	}
	for _, u := range hosted {
		err := CheckLocal(u)
		if err == nil {
			t.Errorf("%s: taken", u)
			continue
		}
		if IsLocal(u) {
			t.Errorf("%s: local", u)
		}
		if msg := err.Error(); strings.Contains(msg, "\n") || !strings.Contains(msg, "caveira only runs local models") {
			t.Errorf("%s: message %q", u, msg)
		}
	}

	for _, u := range []string{"", "localhost:11434", "ftp://localhost/v1", "file:///tmp/x", "http://", "::"} {
		err := CheckLocal(u)
		if err == nil {
			t.Errorf("%q: taken", u)
			continue
		}
		if !strings.Contains(err.Error(), "is not a model server's address") {
			t.Errorf("%q: message %q", u, err)
		}
	}
	if IsLocal("") {
		t.Error("empty is local")
	}
}
