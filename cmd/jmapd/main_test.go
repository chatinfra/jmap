package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestJmapdHelpAliasesMatch(t *testing.T) {
	aliases := [][]string{{"--help"}, {"-h"}, {"help"}}
	var first string
	for _, args := range aliases {
		stdout, stderr, err := runJmapdForTest(t, args...)
		if err != nil {
			t.Fatalf("jmapd %s failed: %v\nstderr:\n%s", strings.Join(args, " "), err, stderr)
		}
		if stderr != "" {
			t.Fatalf("jmapd %s stderr=%q", strings.Join(args, " "), stderr)
		}
		if stdout == "" {
			t.Fatalf("jmapd %s produced empty stdout", strings.Join(args, " "))
		}
		if first == "" {
			first = stdout
			continue
		}
		if stdout != first {
			t.Fatalf("jmapd %s stdout differs from --help\n--help:\n%s\ncurrent:\n%s", strings.Join(args, " "), first, stdout)
		}
	}
}

func TestJmapdHelpHasSectionedShape(t *testing.T) {
	stdout, stderr, err := runJmapdForTest(t, "--help")
	if err != nil {
		t.Fatalf("jmapd --help failed: %v\nstderr:\n%s", err, stderr)
	}
	assertSubstringsInOrder(t, stdout, []string{"USAGE", "ENVIRONMENT", "OUTPUT", "EXAMPLES"})
	for _, oldShape := range []string{"Usage:", "Required environment:", "Optional environment:", "commands:", "flags:", "schemas:"} {
		if strings.Contains(stdout, oldShape) {
			t.Fatalf("help contains old or structured shape %q:\n%s", oldShape, stdout)
		}
	}
	if strings.HasPrefix(strings.TrimSpace(stdout), "{") {
		t.Fatalf("help appears to be a JSON object:\n%s", stdout)
	}
}

func TestJmapdHelpDocumentsEnvironment(t *testing.T) {
	stdout, stderr, err := runJmapdForTest(t, "--help")
	if err != nil {
		t.Fatalf("jmapd --help failed: %v\nstderr:\n%s", err, stderr)
	}
	for _, want := range []string{
		"JMAP_BASE_URL", "JMAP_URL", "JMAP_USER", "JMAP_PASS", "JMAP_PASSWORD",
		"OPENCODE_BASE_URL", "OPENCODE_URL", "OPENCODE_PORT", "OPENCODE_HOST",
		"OPENCODE_DIRECTORY", "OPENCODE_DIR", "OPENCODE_AGENT", "AGENT_ID",
		"JMAPD_STATE_DIR", "STATE_DIR", "JMAP_POLL_INTERVAL", "60s",
		"JMAP_ALARM_WINDOW", "168h", "OPENCODE_PROMPT_TIMEOUT", "no timeout",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("help missing %q:\n%s", want, stdout)
		}
	}
}

func TestJmapdHelpDocumentsOutput(t *testing.T) {
	stdout, stderr, err := runJmapdForTest(t, "--help")
	if err != nil {
		t.Fatalf("jmapd --help failed: %v\nstderr:\n%s", err, stderr)
	}
	assertSubstringsInOrder(t, stdout, []string{"OUTPUT", "stdout:", "stderr:", "state:"})
	for _, want := range []string{"help text", "runtime logs", "configuration errors", "JMAPD_STATE_DIR"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("OUTPUT contract missing %q:\n%s", want, stdout)
		}
	}
}

func TestJmapdHelpBypassesMissingEnvironment(t *testing.T) {
	stdout, stderr, err := runJmapdForTest(t, "--help")
	if err != nil {
		t.Fatalf("jmapd --help failed without config env: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "ENVIRONMENT") {
		t.Fatalf("help stdout missing ENVIRONMENT:\n%s", stdout)
	}
	if stderr != "" {
		t.Fatalf("help emitted stderr without config env: %q", stderr)
	}
	if wantsHelp(nil) {
		t.Fatal("bare no-argument invocation must remain daemon startup, not explicit help")
	}
}

func TestJmapdHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_JMAPD_HELPER_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"jmapd"}, os.Args[i+1:]...)
			main()
			os.Exit(0)
		}
	}
	os.Exit(2)
}

func runJmapdForTest(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmdArgs := append([]string{"-test.run=TestJmapdHelperProcess", "--"}, args...)
	cmd := exec.Command(os.Args[0], cmdArgs...)
	cmd.Env = []string{"GO_WANT_JMAPD_HELPER_PROCESS=1"}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func assertSubstringsInOrder(t *testing.T, text string, wants []string) {
	t.Helper()
	previous := -1
	for _, want := range wants {
		index := strings.Index(text, want)
		if index < 0 {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
		if index <= previous {
			t.Fatalf("%q appears out of order in:\n%s", want, text)
		}
		previous = index
	}
}
