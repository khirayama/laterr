package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/khirayama/laterr/internal/paths"
	"github.com/khirayama/laterr/internal/scheduler"
	"github.com/khirayama/laterr/internal/service"
)

func (a *app) uninstall(args []string) error {
	flags, positional, err := parseArgs("uninstall", args, "--purge")
	if err != nil {
		return err
	}
	if len(positional) > 0 {
		return usagef("unexpected argument for uninstall: %s", positional[0])
	}
	p, err := paths.Resolve()
	if err != nil {
		return err
	}
	purge := flags["--purge"]

	if err := a.confirmUninstall(p, purge); err != nil {
		return err
	}

	if paths.ServiceManaged() {
		path, removed, err := service.Uninstall()
		if err != nil {
			return fmt.Errorf("cannot remove the background service: %v", err)
		}
		if removed {
			fmt.Fprintf(a.stdout, "✓ Removed background service: %s\n", path)
		} else {
			fmt.Fprintln(a.stdout, "- No background service was installed.")
		}
	} else {
		fmt.Fprintf(a.stdout, "- Background service not touched (%s is set).\n", paths.HomeEnv)
	}

	// The scheduler stops its jobs and records them as interrupted on the way
	// out; let it finish before the data may be deleted under it.
	timeout := 15 * time.Second
	if !paths.ServiceManaged() {
		timeout = 0 // nothing was asked to stop it: look once
	}
	stopped := waitFor(timeout, func() bool {
		_, err := scheduler.Ping(p.Socket)
		return err != nil
	})
	if !stopped {
		if purge {
			return fmt.Errorf("a scheduler is still running for %s; stop it before purging", p.Dir)
		}
		fmt.Fprintln(a.stdout, "! A scheduler is still running; it was not started by the background service.")
	}

	if _, err := os.Stat(p.Dir); os.IsNotExist(err) {
		return nil // no data to remove or keep
	}
	if purge {
		if err := os.RemoveAll(p.Dir); err != nil {
			return err
		}
		fmt.Fprintf(a.stdout, "✓ Removed data: %s\n", p.Dir)
	} else {
		fmt.Fprintf(a.stdout, "- Kept job history and logs in %s\n  Remove them with `laterr uninstall --purge`.\n", p.Dir)
	}
	return nil
}

// confirmUninstall asks before going ahead when jobs are still pending or
// running: removing the service strands the former and stops the latter. It
// returns errSilent if the answer is no.
func (a *app) confirmUninstall(p paths.Paths, purge bool) error {
	st, err := openStore(p, false)
	if err != nil || st == nil {
		return err
	}
	pending, running, err := st.CountActive()
	st.Close()
	if err != nil {
		return err
	}
	if pending+running == 0 {
		return nil
	}
	var parts []string
	if pending > 0 {
		parts = append(parts, fmt.Sprintf("%d pending", pending))
	}
	if running > 0 {
		parts = append(parts, fmt.Sprintf("%d running", running))
	}
	fmt.Fprintf(a.stdout, "You have %s job(s).\n", strings.Join(parts, " and "))
	if running > 0 {
		fmt.Fprintln(a.stdout, "Running jobs will be stopped.")
	}
	if pending > 0 && !purge {
		fmt.Fprintln(a.stdout, "Pending jobs will not run unless laterr is set up again in time.")
	}
	fmt.Fprint(a.stdout, "Continue? [y/N] ")
	answer, _ := bufio.NewReader(a.stdin).ReadString('\n')
	if answer = strings.ToLower(strings.TrimSpace(answer)); answer != "y" && answer != "yes" {
		fmt.Fprintln(a.stdout, "Cancelled. Nothing was changed.")
		return errSilent
	}
	return nil
}
