package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/khirayama/laterr/internal/paths"
	"github.com/khirayama/laterr/internal/service"
	"github.com/khirayama/laterr/internal/store"
)

// An outdated service is only replaced while replacing it interrupts nothing.
func TestOutdatedIdleService(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv(paths.HomeEnv, "")
	dir := filepath.Join(home, "data")
	p := paths.Paths{Dir: dir, DB: filepath.Join(dir, "laterr.db"), Logs: filepath.Join(dir, "logs"), SchedulerLog: filepath.Join(dir, "scheduler.log")}

	bin := filepath.Join(home, "laterr")
	if err := os.WriteFile(bin, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	def, err := service.DefinitionPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(def), 0o755); err != nil {
		t.Fatal(err)
	}
	// A definition an older laterr wrote. (Install is not used here: it would
	// talk to the real service, which has the same name.)
	old := `<plist><dict><key>ProgramArguments</key><array><string>` + bin + `</string><string>daemon</string></array><key>ProcessType</key><string>Background</string></dict></plist>`
	if runtime.GOOS != "darwin" {
		old = "[Service]\nExecStart=\"" + bin + "\" daemon\n"
	}
	if err := os.WriteFile(def, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := outdatedIdleService(p); got != bin {
		t.Errorf("no data yet: %q", got)
	}

	st, err := openStore(p, true)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	add := func(at time.Time) store.Job {
		t.Helper()
		j, err := st.Create(store.NewJob{Argv: []string{"true"}, Cwd: home, CreatedAt: time.Now(), ScheduledAt: at, Grace: time.Hour}, p.LogFile)
		if err != nil {
			t.Fatal(err)
		}
		return j
	}
	later := add(time.Now().Add(time.Hour))
	if got := outdatedIdleService(p); got != bin {
		t.Errorf("job due later: %q", got)
	}
	soon := add(time.Now().Add(10 * time.Second))
	if got := outdatedIdleService(p); got != "" {
		t.Errorf("job due soon: %q", got)
	}
	if err := st.MarkRunning(soon.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := outdatedIdleService(p); got != "" {
		t.Errorf("job running: %q", got)
	}
	if err := st.Cancel(later.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := outdatedIdleService(p); got != "" {
		t.Errorf("job still running: %q", got)
	}

	t.Setenv(paths.HomeEnv, dir)
	if got := outdatedIdleService(p); got != "" {
		t.Errorf("service not managed: %q", got)
	}
}
