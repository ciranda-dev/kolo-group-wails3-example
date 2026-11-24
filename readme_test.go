package main

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func markdownFiles(t *testing.T) []string {
	t.Helper()
	files := []string{"README.md", "CHANGELOG.md", "CONTRIBUTING.md", "SECURITY.md", "SUPPORT.md"}
	docs, err := filepath.Glob("docs/*.md")
	if err != nil {
		t.Fatal(err)
	}
	return append(files, docs...)
}

// Every relative link and image of the README header and the docs points at a file that exists.
func TestRelativeLinksResolve(t *testing.T) {
	link := regexp.MustCompile(`(?:\]\(|href="|src=")([^)"#\s]+)`)
	for _, f := range markdownFiles(t) {
		for _, m := range link.FindAllStringSubmatch(readRepo(t, f), -1) {
			target := m[1]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(f), filepath.FromSlash(target))); err != nil {
				t.Errorf("%s links to %s: %v", f, target, err)
			}
		}
	}
}

func TestREADMEHeaderAnchors(t *testing.T) {
	readme := readRepo(t, "README.md")
	for _, anchor := range regexp.MustCompile(`href="#([^"]+)"`).FindAllStringSubmatch(readme, -1) {
		if !strings.Contains(strings.ToLower(readme), "## "+anchor[1]) {
			t.Errorf("README links to #%s, which has no heading", anchor[1])
		}
	}
}

// The quick start runs the binary the Taskfile names, with flags the app has.
func TestREADMEQuickStart(t *testing.T) {
	readme := readRepo(t, "README.md")
	name := regexp.MustCompile(`(?m)^  APP_NAME: "(.*)"`).FindStringSubmatch(readRepo(t, "Taskfile.yml"))
	if name == nil {
		t.Fatal("the Taskfile has no APP_NAME")
	}
	bin := "./bin/" + name[1]
	found := false
	for _, line := range strings.Split(readme, "\n") {
		if !strings.HasPrefix(line, bin+" ") {
			continue
		}
		found = true
		rest := regexp.MustCompile(`"\$\([^)]*\)"`).ReplaceAllString(strings.TrimPrefix(line, bin), t.TempDir())
		args := strings.Fields(rest)
		if _, err := LoadConfig(args, noEnv, io.Discard); err != nil {
			t.Errorf("README command %q: %v", line, err)
		}
	}
	if !found {
		t.Errorf("the README does not run %s", bin)
	}
	if m := regexp.MustCompile("KOLO_[A-Z]+_DATA").FindString(readme); m != "KOLO_GROUP_DATA" {
		t.Errorf("the README names the variable %q, the app reads KOLO_GROUP_DATA", m)
	}
}

func TestNoEmDashInTheDocs(t *testing.T) {
	for _, f := range markdownFiles(t) {
		if strings.ContainsRune(readRepo(t, f), rune(0x2014)) {
			t.Errorf("%s has an em dash", f)
		}
	}
}
