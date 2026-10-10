package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/khirayama/laterr/internal/paths"
	"github.com/khirayama/laterr/internal/store"
)

const defaultPruneAge = 30 * 24 * time.Hour

func (a *app) prune(args []string) error {
	const usage = "usage: laterr prune [--older-than <duration> | --all]"
	age, ageText, all, hasAge := defaultPruneAge, "30d", false, false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		text, ok, err := durationFlag(args, &i, "--older-than", "7d")
		switch {
		case err != nil:
			return err
		case ok:
			if age, err = parseDurationFlag("--older-than", text); err != nil {
				return err
			}
			ageText, hasAge = text, true
		case arg == "--all":
			all = true
		case strings.HasPrefix(arg, "-"):
			return usagef("unknown option for prune: %s", arg)
		default:
			return usagef("unexpected argument for prune: %s (%s)", arg, usage)
		}
	}
	if all && hasAge {
		return usagef("--all and --older-than cannot be combined")
	}
	if all {
		age = 0
	}

	p, err := paths.Resolve()
	if err != nil {
		return err
	}
	var jobs []store.Job
	st, err := openStore(p, false)
	if err != nil {
		return err
	}
	if st != nil {
		defer st.Close()
		if jobs, err = st.Prune(time.Now().Add(-age)); err != nil {
			return err
		}
	}
	for _, j := range jobs {
		// Only ever delete inside laterr's own log directory.
		if filepath.Dir(j.LogPath) != p.Logs {
			continue
		}
		if err := os.Remove(j.LogPath); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(a.stderr, "! Could not remove %s: %v\n", j.LogPath, unwrapPathError(err))
		}
	}
	switch {
	case len(jobs) > 0:
		logs := "their logs"
		if len(jobs) == 1 {
			logs = "its log"
		}
		fmt.Fprintf(a.stdout, "✓ Pruned %s and %s.\n", plural(int64(len(jobs)), "finished job"), logs)
	case all:
		fmt.Fprintln(a.stdout, "Nothing to prune: no finished jobs.")
	default:
		fmt.Fprintf(a.stdout, "Nothing to prune: no jobs finished more than %s ago. Use `laterr prune --all` to remove every finished job.\n", ageText)
	}
	return nil
}

func unwrapPathError(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
