package main

import (
	"errors"
	"flag"
	"io"
	"path/filepath"
	"strings"
	"testing"
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
	cfg, err = LoadConfig([]string{"-data", flagged}, func(string) string { return dir }, io.Discard)
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
