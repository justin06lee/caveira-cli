package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/wailsapp/wails/v2/pkg/menu"
)

func labels(m *menu.Menu) []string {
	var out []string
	for _, it := range m.Items {
		switch {
		case it.IsSeparator():
			out = append(out, "-")
		case it.Role != 0:
			out = append(out, "role")
		default:
			l := it.Label
			if it.Accelerator != nil {
				l += " "
				for _, m := range it.Accelerator.Modifiers {
					l += string(m) + "+"
				}
				l += it.Accelerator.Key
			}
			out = append(out, l)
		}
	}
	return out
}

func TestAppMenu(t *testing.T) {
	bar := appMenu(NewApp("test"))
	var tops []string
	subs := map[string][]string{}
	for _, it := range bar.Items {
		tops = append(tops, it.Label)
		if it.SubMenu != nil {
			subs[it.Label] = labels(it.SubMenu)
		}
	}
	// The first menu is the app's, whatever its title says.
	if got := strings.Join(tops, ","); got != "caveira,File,,View,,Help" {
		t.Fatalf("menu bar %q", got)
	}
	want := map[string]string{
		"caveira": "About caveira|-|Settings… cmdorctrl+,|-|Hide caveira cmdorctrl+h|Hide Others cmdorctrl+optionoralt+h|Show All|-|Quit caveira cmdorctrl+q",
		"File":    "New Chat cmdorctrl+n|Open Project… cmdorctrl+o|-|Import from Other Agents…",
		"Help":    "caveira Help|Report a Problem…|-|Check for Updates…",
	}
	for name, w := range want {
		if got := strings.Join(subs[name], "|"); got != w {
			t.Errorf("%s menu\n got %s\nwant %s", name, got, w)
		}
	}
}

func TestUpdateAnswer(t *testing.T) {
	cases := []struct {
		updatable, disabled bool
		latest              string
		err                 error
		title               string
		offer               bool
	}{
		{false, false, "", nil, "caveira 0.3.1", false},
		{true, true, "", nil, "Update checks are off", false},
		{true, false, "", errors.New("offline"), "Couldn't check for updates", false},
		{true, false, "", nil, "You're up to date", false},
		{true, false, "0.4.0", nil, "caveira 0.4.0 is out", true},
	}
	for _, c := range cases {
		title, msg, offer := updateAnswer("0.3.1", c.updatable, c.disabled, c.latest, c.err)
		if title != c.title || offer != c.offer || msg == "" {
			t.Errorf("%+v: %q %q %v", c, title, msg, offer)
		}
	}
}
