package scheduler

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khirayama/laterr/internal/store"
)

func TestLookPath(t *testing.T) {
	cwd := t.TempDir()
	write := func(rel string, mode os.FileMode) string {
		p := filepath.Join(cwd, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	first := write("a/tool", 0o755)
	write("b/tool", 0o755)
	write("a/data", 0o644)
	onlyB := write("b/data", 0o755)
	local := write("script.sh", 0o755)
	os.Mkdir(filepath.Join(cwd, "a", "dir"), 0o755)

	env := []string{"HOME=/h", "PATH=" + filepath.Join(cwd, "a") + ":b"}
	cases := []struct {
		name, want string
	}{
		{"tool", first},        // the first PATH entry wins
		{"data", onlyB},        // non-executables are skipped; relative entries resolve against cwd
		{"./script.sh", local}, // paths with a slash bypass PATH
		{local, local},
	}
	for _, c := range cases {
		if got, err := lookPath(c.name, cwd, env); err != nil || got != c.want {
			t.Errorf("%s: got %q, %v; want %q", c.name, got, err, c.want)
		}
	}
	for name, env := range map[string][]string{
		"missing":   env,
		"dir":       env,
		"script.sh": env, // the current directory is not searched implicitly
		"./a/data":  env, // not executable
		"./nope":    env,
		"tool":      {"HOME=/h"}, // no PATH at all
	} {
		if got, err := lookPath(name, cwd, env); err == nil {
			t.Errorf("%s: expected an error, got %q", name, got)
		} else if !strings.Contains(err.Error(), name) {
			t.Errorf("%s: error does not name the command: %v", name, err)
		}
	}
}

func TestOutcome(t *testing.T) {
	run := func(script string) (store.Status, *int, string) {
		cmd := exec.Command("sh", "-c", script)
		err := cmd.Run()
		return outcome(cmd.ProcessState, err)
	}
	if st, code, msg := run("exit 0"); st != store.Succeeded || code == nil || *code != 0 || msg != "" {
		t.Errorf("exit 0: %v %v %q", st, code, msg)
	}
	if st, code, _ := run("exit 3"); st != store.Failed || code == nil || *code != 3 {
		t.Errorf("exit 3: %v %v", st, code)
	}
	if st, code, msg := run("kill -KILL $$"); st != store.Failed || code != nil || !strings.Contains(msg, "signal") {
		t.Errorf("killed: %v %v %q", st, code, msg)
	}
}
