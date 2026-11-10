package skills

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// GitHubAPI is where repositories are downloaded from; tests point it at
// a fake.
var GitHubAPI = "https://api.github.com"

// Source is a skill, or a folder of skills, on GitHub.
type Source struct {
	Owner, Repo string
	// Ref is a branch, tag or commit; empty is the default branch.
	Ref string
	// Dir is the folder in the repository, "" for its root.
	Dir string
}

func (s Source) String() string {
	out := s.Owner + "/" + s.Repo
	if s.Dir != "" {
		out += "/" + s.Dir
	}
	if s.Ref != "" {
		out += "@" + s.Ref
	}
	return out
}

// ParseSource reads where a skill is from:
//
//	https://github.com/owner/repo
//	https://github.com/owner/repo/tree/main/skills/pdf
//	https://github.com/owner/repo/blob/main/skills/pdf/SKILL.md
//	owner/repo, or owner/repo/skills/pdf
func ParseSource(s string) (Source, error) {
	s = strings.TrimSpace(s)
	bad := fmt.Errorf("%q is not a GitHub repository or folder: use https://github.com/owner/repo[/tree/ref/path] or owner/repo/path", s)
	if s == "" {
		return Source{}, bad
	}
	var parts []string
	if strings.Contains(s, "://") || strings.HasPrefix(s, "github.com/") {
		if !strings.Contains(s, "://") {
			s = "https://" + s
		}
		u, err := url.Parse(s)
		if err != nil || (u.Host != "github.com" && u.Host != "www.github.com") {
			return Source{}, bad
		}
		parts = split(u.Path)
		if len(parts) < 2 {
			return Source{}, bad
		}
		src := Source{Owner: parts[0], Repo: strings.TrimSuffix(parts[1], ".git")}
		if len(parts) >= 4 && (parts[2] == "tree" || parts[2] == "blob") {
			src.Ref = parts[3]
			dir := strings.Join(parts[4:], "/")
			if parts[2] == "blob" || strings.EqualFold(path.Base(dir), "SKILL.md") {
				dir = path.Dir(dir)
				if dir == "." {
					dir = ""
				}
			}
			src.Dir = dir
		} else if len(parts) > 2 {
			return Source{}, bad
		}
		return src, nil
	}
	parts = split(s)
	if len(parts) < 2 || strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~") || strings.HasPrefix(parts[0], ".") {
		return Source{}, bad
	}
	src := Source{Owner: parts[0], Repo: parts[1], Dir: strings.Join(parts[2:], "/")}
	if strings.EqualFold(path.Base(src.Dir), "SKILL.md") {
		src.Dir = strings.TrimSuffix(path.Dir(src.Dir), ".")
	}
	if r, ref, ok := strings.Cut(src.Repo, "@"); ok {
		src.Repo, src.Ref = r, ref
	}
	return src, nil
}

func split(p string) []string {
	var out []string
	for _, x := range strings.Split(p, "/") {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}

// maxDownload caps a repository's archive.
const maxDownload = 64 << 20

// Install downloads src and copies every skill in its folder (the folder
// itself when it holds a SKILL.md, or the skills in the folders under it)
// into ~/.caveira/skills, replacing ones of the same name. It says which
// went in.
func Install(ctx context.Context, src Source) ([]Skill, error) {
	u := fmt.Sprintf("%s/repos/%s/%s/tarball", GitHubAPI, url.PathEscape(src.Owner), url.PathEscape(src.Repo))
	if src.Ref != "" {
		u += "/" + url.PathEscape(src.Ref)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "caveira")
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("github.com/%s/%s is not there, or not public", src.Owner, src.Repo)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GitHub answered %s", resp.Status)
	}

	work, err := os.MkdirTemp("", "caveira-skill-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	if err := untar(io.LimitReader(resp.Body, maxDownload), work, src.Dir); err != nil {
		return nil, err
	}
	base := filepath.Join(work, filepath.FromSlash(src.Dir))
	if st, err := os.Stat(base); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("%s has no folder %s", src.Owner+"/"+src.Repo, src.Dir)
	}

	var dirs []string
	if _, err := os.Stat(filepath.Join(base, "SKILL.md")); err == nil {
		dirs = []string{base}
	} else {
		// A collection: skills one or two folders down (skills/pdf, pdf).
		for _, pattern := range []string{"*/SKILL.md", "*/*/SKILL.md"} {
			found, _ := filepath.Glob(filepath.Join(base, pattern))
			for _, f := range found {
				dirs = append(dirs, filepath.Dir(f))
			}
			if len(dirs) > 0 {
				break
			}
		}
	}
	if len(dirs) == 0 {
		return nil, fmt.Errorf("no SKILL.md in %s", src)
	}

	var out []Skill
	for _, d := range dirs {
		sk, err := Read(d)
		if err != nil {
			return out, err
		}
		name := filepath.Base(sk.Name)
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\:`) || reservedName(name) {
			return out, fmt.Errorf("%q cannot name a skill folder", sk.Name)
		}
		dst := filepath.Join(InstallDir(), name)
		if err := os.MkdirAll(InstallDir(), 0o755); err != nil {
			return out, err
		}
		staged := dst + ".new"
		os.RemoveAll(staged)
		if err := os.Rename(d, staged); err != nil {
			if err := copyDir(d, staged); err != nil {
				os.RemoveAll(staged)
				return out, err
			}
		}
		os.RemoveAll(dst)
		if err := os.Rename(staged, dst); err != nil {
			return out, err
		}
		sk.Dir, sk.Path = dst, filepath.Join(dst, "SKILL.md")
		sk.Where, sk.Installed, sk.Enabled = "~/.caveira/skills", true, true
		out = append(out, sk)
	}
	return out, nil
}

// untar unpacks the files under dir in a GitHub tarball (whose entries
// all sit in one top folder) into dst. Links and anything that would
// land outside dst are skipped.
func untar(r io.Reader, dst, dir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("reading the download: %w", err)
	}
	tr := tar.NewReader(gz)
	prefix := strings.Trim(dir, "/")
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading the download: %w", err)
		}
		_, rel, ok := strings.Cut(h.Name, "/")
		if !ok || rel == "" {
			continue
		}
		rel = path.Clean(rel)
		if strings.HasPrefix(rel, "../") || rel == ".." || path.IsAbs(rel) || !localName(rel) {
			continue
		}
		if prefix != "" && rel != prefix && !strings.HasPrefix(rel, prefix+"/") {
			continue
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if h.Mode&0o111 != 0 {
				mode = 0o755
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		}
	}
}

// localName says a path from an archive, in the slash form tar uses, stays
// under the folder it is unpacked into on any system. A tar entry can name
// anything, and Windows reads more into a name than the slashes: a
// backslash separates folders there (..\..\x climbs out), C:x and C:\x
// name a drive, a colon starts an NTFS data stream, and the names of
// devices (NUL, CON, COM1) are not files at all.
func localName(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		if part == ".." || strings.ContainsAny(part, `\:`) || strings.ContainsRune(part, 0) {
			return false
		}
		if reservedName(part) {
			return false
		}
	}
	return true
}

// reservedName says part is a name Windows keeps for a device, with or
// without an extension: NUL, CON, PRN, AUX, COM1 to COM9, LPT1 to LPT9.
func reservedName(part string) bool {
	base, _, _ := strings.Cut(part, ".")
	base = strings.ToUpper(strings.TrimRight(base, " "))
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9'
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, info.Mode().Perm())
	})
}
