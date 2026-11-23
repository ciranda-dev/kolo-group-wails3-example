package main

import (
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func noEnv(string) string { return "" }

func TestLoadConfigFlags(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadConfig([]string{"-data", dir, "-headless"}, noEnv, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != dir || !cfg.Headless {
		t.Fatalf("got %+v", cfg)
	}
}

func TestLoadConfigDataFromEnvironment(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadConfig(nil, func(k string) string {
		if k == "KOLO_GROUP_DATA" {
			return dir
		}
		return ""
	}, io.Discard)
	if err != nil || cfg.DataDir != dir {
		t.Fatalf("got %+v, %v", cfg, err)
	}
	flagged := filepath.Join(dir, "flag")
	cfg, err = LoadConfig([]string{"-data", flagged}, func(k string) string {
		if k == "KOLO_GROUP_DATA" {
			return dir
		}
		return ""
	}, io.Discard)
	if err != nil || cfg.DataDir != flagged {
		t.Fatalf("a flag must win over the environment: %+v, %v", cfg, err)
	}
}

func TestLoadConfigDefaultDataDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := LoadConfig(nil, noEnv, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(cfg.DataDir) != "kolo-group" {
		t.Fatalf("default data dir %q", cfg.DataDir)
	}
}

func TestLoadConfigRefusals(t *testing.T) {
	if _, err := LoadConfig([]string{"extra"}, noEnv, io.Discard); err == nil || !strings.HasPrefix(err.Error(), "kolo-group:") {
		t.Fatalf("a stray argument: %v", err)
	}
	if _, err := LoadConfig([]string{"-h"}, noEnv, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("-h: %v", err)
	}
	if _, err := LoadConfig([]string{"-nope"}, noEnv, io.Discard); err == nil {
		t.Fatal("an unknown flag was accepted")
	}
}

const (
	keyA = "aa00aa00aa00aa00aa00aa00aa00aa00aa00aa00aa00aa00aa00aa00aa00aa00"
	keyB = "bb00bb00bb00bb00bb00bb00bb00bb00bb00bb00bb00bb00bb00bb00bb00bb00"
)

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestConfigFileFlagEnvironmentOrder(t *testing.T) {
	file := writeFile(t, "kolo-group.json", `{"data": "/from/file", "bootstrap": "127.0.0.1:7447", "pin": ["127.0.0.1:7450@`+keyA+`"], "pin-ttl": "48h", "relay": ["127.0.0.1:7448@`+keyB+`"]}`)
	env := envOf(map[string]string{"KOLO_GROUP_DATA": "/from/env", "KOLO_GROUP_BOOTSTRAP": "127.0.0.1:1", "KOLO_GROUP_LISTEN": "127.0.0.1:9", "KOLO_GROUP_PIN_TTL": "1h", "KOLO_GROUP_LOOKUP_INTERVAL": "9s"})
	cfg, err := LoadConfig([]string{"-config", file}, env, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != "/from/file" || cfg.Bootstrap != "127.0.0.1:7447" || cfg.PinTTL != 48*time.Hour || len(cfg.Pins) != 1 || len(cfg.Relays) != 1 {
		t.Fatalf("the file must win over the environment: %+v", cfg)
	}
	if cfg.Listen != "127.0.0.1:9" || cfg.LookupInterval != 9*time.Second {
		t.Fatalf("what the file leaves out comes from the environment: %+v", cfg)
	}
	cfg, err = LoadConfig([]string{"-config", file, "-data", "/from/flag", "-pin", "127.0.0.1:7451@" + keyB, "-pin-ttl", "2h"}, env, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != "/from/flag" || cfg.PinTTL != 2*time.Hour || len(cfg.Pins) != 1 || cfg.Pins[0] != "127.0.0.1:7451@"+keyB {
		t.Fatalf("a flag must win over the file, and a list flag replaces the list: %+v", cfg)
	}
	set, err := cfg.resolve()
	if err != nil || len(set.pins) != 1 || len(set.relays) != 1 || set.relaysFrom != "-relay" || set.network().Mode != "own" || set.Listen != "127.0.0.1:9" {
		t.Fatalf("resolved: %+v, %v", set, err)
	}
}

func TestConfigFileRefusals(t *testing.T) {
	big := writeFile(t, "big.json", "{\"data\":\""+strings.Repeat("a", maxConfigFile)+"\"}")
	for name, path := range map[string]string{
		"an unknown key":     writeFile(t, "a.json", `{"daat": "x"}`),
		"the config key":     writeFile(t, "b.json", `{"config": "x"}`),
		"text after the end": writeFile(t, "c.json", `{"data": "x"} {}`),
		"a wrong type":       writeFile(t, "d.json", `{"pin": "127.0.0.1:1@`+keyA+`"}`),
		"a bad duration":     writeFile(t, "e.json", `{"pin-ttl": "soon"}`),
		"a file too large":   big,
		"a folder":           t.TempDir(),
		"no file":            filepath.Join(t.TempDir(), "missing.json"),
	} {
		_, err := LoadConfig([]string{"-config", path}, noEnv, io.Discard)
		var ce *ConfigError
		if !errors.As(err, &ce) || !errors.Is(err, ErrConfig) || !strings.HasPrefix(err.Error(), "kolo-group: -") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := LoadConfig([]string{"-pin-ttl", "soon"}, noEnv, io.Discard); !errors.Is(err, ErrConfig) {
		t.Errorf("a bad duration flag: %v", err)
	}
}

func TestResolveChecksPinsRelaysAndEntryNodes(t *testing.T) {
	resolve := func(c Config) error { c.DataDir = t.TempDir(); _, err := c.resolve(); return err }
	good := "127.0.0.1:7450@" + keyA
	for name, c := range map[string]Config{
		"no @":                  {Pins: []string{"127.0.0.1:7450"}},
		"port 0":                {Pins: []string{"127.0.0.1:0@" + keyA}},
		"an IPv6 pin":           {Pins: []string{"[::1]:7450@" + keyA}},
		"a short key":           {Pins: []string{"127.0.0.1:7450@aa"}},
		"a key at two places":   {Pins: []string{good, "127.0.0.1:7451@" + keyA}},
		"a bad entry node":      {Bootstrap: "nope"},
		"an IPv6 entry node":    {Bootstrap: "[::1]:7447"},
		"a bad relay":           {Relays: []string{"x"}},
		"a negative pin ttl":    {PinTTL: -time.Hour},
		"a negative lookup gap": {LookupInterval: -time.Second},
		"an empty pin file":     {PinFiles: []string{writeFile(t, "pins.txt", "# nothing\n")}},
		"a missing pin file":    {PinFiles: []string{filepath.Join(t.TempDir(), "x")}},
		"a missing relay file":  {RelayFiles: []string{filepath.Join(t.TempDir(), "x")}},
	} {
		if err := resolve(c); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
	file := writeFile(t, "pins.txt", "\xef\xbb\xbf# the team\n"+good+"\n127.0.0.1:7451@"+keyB+" # second\n"+good+"\n")
	set, err := Config{DataDir: t.TempDir(), PinFiles: []string{file}, Pins: []string{good}}.resolve()
	if err != nil || len(set.pins) != 2 {
		t.Fatalf("pins from a file, repeats dropped: %+v, %v", set.pins, err)
	}
	if set.Listen != publicListen || set.network().Mode != "public" || set.PinTTL != defaultPinTTL || set.LookupInterval != defaultLookupInterval {
		t.Fatalf("defaults: %+v", set)
	}
	if set, _ := (Config{Bootstrap: "10.1.2.3:7447"}).resolve(); set.Listen != publicListen {
		t.Fatalf("listen for a remote entry node: %q", set.Listen)
	}
	if set, _ := (Config{Bootstrap: "127.0.0.1:7447", Listen: "127.0.0.1:5"}).resolve(); set.Listen != "127.0.0.1:5" {
		t.Fatalf("a given listen address: %q", set.Listen)
	}
}

func TestRelaysFromTheEnvironmentOnlyWhenNoFlagGivesOne(t *testing.T) {
	relay := "127.0.0.1:7448@" + keyB
	cfg, err := LoadConfig(nil, envOf(map[string]string{"KOLO_GROUP_RELAYS": relay}), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	set, err := cfg.resolve()
	if err != nil || len(set.relays) != 1 || set.relaysFrom != "KOLO_GROUP_RELAYS" {
		t.Fatalf("%+v, %v", set, err)
	}
	cfg, _ = LoadConfig([]string{"-relay", "127.0.0.1:7449@" + keyA}, envOf(map[string]string{"KOLO_GROUP_RELAYS": relay}), io.Discard)
	set, err = cfg.resolve()
	if err != nil || len(set.relays) != 1 || set.relaysFrom != "-relay" || set.relays[0].Addr != "127.0.0.1:7449" {
		t.Fatalf("%+v, %v", set, err)
	}
	bad := Config{Relays: []string{"127.0.0.1:1"}, relaysEnv: true}
	if _, err = bad.resolve(); err == nil || !strings.Contains(err.Error(), "KOLO_GROUP_RELAYS") {
		t.Fatalf("a bad relay from the environment must say where it came from: %v", err)
	}
}

func TestDataFolderIsPrivateAndHasOneLayout(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := checkDataDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := checkDataDir(dir); err != nil {
		t.Fatalf("a folder made by the app: %v", err)
	}
	if st, _ := os.Stat(dir); runtime.GOOS != "windows" && st.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	if err := os.WriteFile(filepath.Join(dir, "layout.json"), []byte(`{"layout":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkDataDir(dir); err == nil || !strings.Contains(err.Error(), "another -data") {
		t.Fatalf("another layout: %v", err)
	}
	if runtime.GOOS != "windows" {
		open := t.TempDir()
		if err := os.Chmod(open, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := checkDataDir(open); err == nil || !strings.Contains(err.Error(), "chmod 700") {
			t.Fatalf("an open folder: %v", err)
		}
	}
	if _, err := NewGroupService(Config{DataDir: dir}).Start(); err == nil || !strings.HasPrefix(err.Error(), "kolo-group:") {
		t.Fatalf("Start over another layout: %v", err)
	}
}
