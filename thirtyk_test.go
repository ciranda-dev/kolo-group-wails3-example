package main

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// The 30,000-member tests run in one documented target: the Taskfile names it, it runs the script, and the script lists the
// simulator at 30,000 nodes and the room tests of 30,000 leaves.
func TestThirtyThousandTarget(t *testing.T) {
	taskfile := readRepo(t, "Taskfile.yml")
	if !regexp.MustCompile(`(?m)^  test-30k:\n(?:    .*\n)*?      - \./scripts/test-30k\.sh$`).MatchString(taskfile) {
		t.Fatal("the Taskfile has no test-30k target that runs scripts/test-30k.sh")
	}
	for _, f := range []string{"docs/build-and-test.md", "README.md"} {
		if !strings.Contains(readRepo(t, f), "task test-30k") {
			t.Errorf("%s does not name the target", f)
		}
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	out, err := exec.Command(bash, "scripts/test-30k.sh", "--dry-run").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 3 ||
		!strings.Contains(lines[0], "gossipsim") || !strings.Contains(lines[0], "-n 30000") ||
		!strings.Contains(lines[1], "TestMeasureReaders") ||
		!strings.Contains(lines[2], "KOLO_GROUP_LEAVES=30000") || !strings.Contains(lines[2], "KOLO_GROUP_JOINERS=100") {
		t.Fatalf("the commands of the target:\n%s", out)
	}
	if err := exec.Command(bash, "-n", "scripts/test-30k.sh").Run(); err != nil {
		t.Fatal(err)
	}
}
