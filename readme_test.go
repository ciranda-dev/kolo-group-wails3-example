package main

import (
	"bytes"
	"context"
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

// quickStart returns the configurations of the commands of the README that run the binary the Taskfile names. The data folder
// of each is a temporary one, the entry node is the one given (the README's is 127.0.0.1:7447), and the config file is the
// JSON block of the README, written to a temporary file.
func quickStart(t *testing.T, entryNode string) []Config {
	t.Helper()
	readme := readRepo(t, "README.md")
	name := regexp.MustCompile(`(?m)^  APP_NAME: "(.*)"`).FindStringSubmatch(readRepo(t, "Taskfile.yml"))
	if name == nil {
		t.Fatal("the Taskfile has no APP_NAME")
	}
	bin := "./bin/" + name[1]
	block := regexp.MustCompile("(?s)```json\n(.*?)```").FindStringSubmatch(readme)
	if block == nil {
		t.Fatal("the README has no config file")
	}
	welcome := filepath.Join(t.TempDir(), "welcome.txt")
	text := strings.ReplaceAll(strings.ReplaceAll(block[1], "127.0.0.1:7447", entryNode), "welcome.txt", filepath.ToSlash(welcome))
	configFile := filepath.Join(t.TempDir(), "headless.json")
	if err := os.WriteFile(configFile, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	var out []Config
	for _, line := range strings.Split(readme, "\n") {
		if !strings.HasPrefix(line, bin+" ") {
			continue
		}
		rest := regexp.MustCompile(`"\$\([^)]*\)"`).ReplaceAllString(strings.TrimPrefix(line, bin), "DATA")
		args := strings.Fields(strings.ReplaceAll(rest, "127.0.0.1:7447", entryNode))
		for i, a := range args {
			switch a {
			case "DATA":
				args[i] = filepath.Join(t.TempDir(), "data")
			case "headless.json":
				args[i] = configFile
			}
		}
		cfg, err := LoadConfig(args, noEnv, io.Discard)
		if err != nil {
			t.Fatalf("README command %q: %v", line, err)
		}
		out = append(out, cfg)
	}
	if len(out) == 0 {
		t.Fatalf("the README does not run %s", bin)
	}
	return out
}

// The quick start runs the binary the Taskfile names, with flags the app has.
func TestREADMEQuickStart(t *testing.T) {
	cmds := quickStart(t, "127.0.0.1:7447")
	headless := 0
	for _, cfg := range cmds {
		if cfg.Headless {
			headless++
			if cfg.Bootstrap != "127.0.0.1:7447" || cfg.WelcomeFile == "" || cfg.Listen != "127.0.0.1:0" {
				t.Errorf("the README's config file was not read: %+v", cfg)
			}
		}
		if _, err := cfg.resolve(); err != nil {
			t.Errorf("README command: %v", err)
		}
	}
	if headless != 1 || len(cmds) != 3 {
		t.Errorf("the README runs %d commands, %d of them headless: want two windows and one headless device", len(cmds), headless)
	}
	if m := regexp.MustCompile("KOLO_[A-Z]+_DATA").FindString(readRepo(t, "README.md")); m != "KOLO_GROUP_DATA" {
		t.Errorf("the README names the variable %q, the app reads KOLO_GROUP_DATA", m)
	}
}

// The quick start runs: two windows and a headless device start over an entry node on 127.0.0.1, and the first two talk.
func TestREADMEQuickStartRuns(t *testing.T) {
	server := newServer(t)
	var windows []*GroupService
	for _, cfg := range quickStart(t, server.Addr().String()) {
		if cfg.Headless {
			var out bytes.Buffer
			d, err := startHeadless(context.Background(), cfg, fastTune, &out)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			if !strings.Contains(out.String(), prefixKeyPackage) {
				t.Errorf("a headless device with no room does not print a key package: %q", out.String())
			}
			continue
		}
		s := NewGroupService(cfg)
		s.tune = fastTune
		info, err := s.Start()
		if err != nil {
			t.Fatal(err)
		}
		defer s.ServiceShutdown()
		if info.Network.Mode != "own" || len(info.Rooms) != 0 {
			t.Errorf("the window of the quick start: %+v", info)
		}
		windows = append(windows, s)
	}
	if len(windows) != 2 {
		t.Fatalf("%d windows", len(windows))
	}
	a, b := &testApp{t: t, svc: windows[0]}, &testApp{t: t, svc: windows[1]}
	room, err := a.svc.CreateRoom("Quick start", PolicyInfo{})
	if err != nil {
		t.Fatal(err)
	}
	invite(t, a, b, room.ID)
	if _, err := a.svc.Send(room.ID, "hello", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, wait, "the message at the second window", func() bool {
		got, _ := b.svc.Messages(room.ID, 0, 10)
		return len(got) == 1 && got[0].Body == "hello"
	})
}

func TestNoEmDashInTheDocs(t *testing.T) {
	for _, f := range markdownFiles(t) {
		if strings.ContainsRune(readRepo(t, f), rune(0x2014)) {
			t.Errorf("%s has an em dash", f)
		}
	}
}
