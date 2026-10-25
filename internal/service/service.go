// Package service registers the scheduler as a per-user background service:
// a launchd LaunchAgent on macOS, a systemd user unit on Linux. Neither needs
// root.
package service

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	launchdLabel = "com.laterr.scheduler"
	systemdUnit  = "laterr.service"
)

// DefinitionPath returns where the service definition file lives.
func DefinitionPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"), nil
	case "linux":
		dir := os.Getenv("XDG_CONFIG_HOME")
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(home, ".config")
		}
		return filepath.Join(dir, "systemd", "user", systemdUnit), nil
	}
	return "", fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
}

// Installed reports whether the service definition file exists.
func Installed() bool {
	path, err := DefinitionPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// BinaryPath returns the laterr executable the service should run. Package
// managers such as Homebrew install each version under its own directory and
// keep a symlink on PATH pointing at the current one; preferring that symlink
// keeps the service working across upgrades.
func BinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return exe, nil
	}
	if onPath, err := exec.LookPath(filepath.Base(exe)); err == nil {
		if onPath, err = filepath.Abs(onPath); err == nil {
			if target, err := filepath.EvalSymlinks(onPath); err == nil && target == real {
				return onPath, nil
			}
		}
	}
	return exe, nil
}

// Install writes the service definition and starts the scheduler. It is safe
// to call when the service is already installed. created reports whether the
// definition file was newly created.
func Install(binary, schedulerLog string) (path string, created bool, err error) {
	path, err = DefinitionPath()
	if err != nil {
		return "", false, err
	}
	var want string
	if runtime.GOOS == "darwin" {
		want = launchdPlist(binary, schedulerLog)
	} else {
		want = systemdUnitFile(binary)
	}
	old, readErr := os.ReadFile(path)
	created = os.IsNotExist(readErr)
	changed := string(old) != want
	if changed {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return path, false, err
		}
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			return path, false, err
		}
	}
	if runtime.GOOS == "darwin" {
		err = startLaunchd(path, changed && !created)
	} else {
		err = startSystemd(changed)
	}
	return path, created, err
}

// Uninstall stops the scheduler and removes the service definition. removed
// reports whether there was a definition file to remove.
func Uninstall() (path string, removed bool, err error) {
	path, err = DefinitionPath()
	if err != nil {
		return "", false, err
	}
	// Stopping is best effort: the service may not be loaded at all.
	if runtime.GOOS == "darwin" {
		run("launchctl", "bootout", launchdTarget())
	} else {
		run("systemctl", "--user", "disable", "--now", systemdUnit)
	}
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return path, false, nil
	}
	if err != nil {
		return path, false, err
	}
	if runtime.GOOS == "linux" {
		run("systemctl", "--user", "daemon-reload")
	}
	return path, true, nil
}

var (
	plistProgram = regexp.MustCompile(`(?s)<key>ProgramArguments</key>\s*<array>\s*(<string>.*?</string>)`)
	unitProgram  = regexp.MustCompile(`(?m)^ExecStart="((?:[^"\\]|\\.)*)" daemon$`)
)

// DefinedBinary returns the executable the installed service definition runs.
func DefinedBinary() (string, error) {
	path, err := DefinitionPath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return definedBinary(runtime.GOOS, string(data))
}

func definedBinary(goos, def string) (string, error) {
	if goos == "darwin" {
		if m := plistProgram.FindStringSubmatch(def); m != nil {
			var bin string
			if err := xml.Unmarshal([]byte(m[1]), &bin); err == nil {
				return bin, nil
			}
		}
	} else if m := unitProgram.FindStringSubmatch(def); m != nil {
		return strings.NewReplacer(`\\`, `\`, `\"`, `"`, `\n`, "\n", `\r`, "\r", `\t`, "\t", `%%`, `%`, `$$`, `$`).Replace(m[1]), nil
	}
	return "", errors.New("unrecognized format")
}

// RestartCommand returns the shell command that restarts the scheduler.
func RestartCommand() string {
	switch runtime.GOOS {
	case "darwin":
		return "launchctl kickstart -k " + launchdTarget()
	case "linux":
		return "systemctl --user restart " + systemdUnit
	}
	return ""
}

// LogHint tells where the scheduler's own output can be read.
func LogHint(schedulerLog string) string {
	if runtime.GOOS == "linux" {
		return "journalctl --user -u " + systemdUnit
	}
	return schedulerLog
}

func run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return text, fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), text)
	}
	return text, nil
}

// --- launchd ---

func launchdDomain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }
func launchdTarget() string { return launchdDomain() + "/" + launchdLabel }

func launchdLoaded() bool {
	_, err := run("launchctl", "print", launchdTarget())
	return err == nil
}

func launchdPlist(binary, schedulerLog string) string {
	esc := func(s string) string {
		var b bytes.Buffer
		xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + launchdLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + esc(binary) + `</string>
		<string>daemon</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>` + esc(schedulerLog) + `</string>
	<key>StandardErrorPath</key>
	<string>` + esc(schedulerLog) + `</string>
</dict>
</plist>
`
}

func startLaunchd(plist string, reload bool) error {
	if reload && launchdLoaded() {
		// The loaded service still uses the old definition. Unloading is
		// asynchronous, so wait for it before loading the new one.
		run("launchctl", "bootout", launchdTarget())
		for i := 0; i < 50 && launchdLoaded(); i++ {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if launchdLoaded() {
		_, err := run("launchctl", "kickstart", launchdTarget())
		return err
	}
	var err error
	for i := 0; i < 5; i++ {
		if _, err = run("launchctl", "bootstrap", launchdDomain(), plist); err == nil || launchdLoaded() {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return err
}

// --- systemd ---

func systemdUnitFile(binary string) string {
	// Quote the path for systemd: C-style escapes inside double quotes, with
	// "%" and "$" doubled so they are not expanded.
	q := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`, `%`, `%%`, `$`, `$$`).Replace(binary)
	return `[Unit]
Description=laterr scheduler (runs commands later)

[Service]
ExecStart="` + q + `" daemon
Restart=always
RestartSec=1

[Install]
WantedBy=default.target
`
}

// systemdStartVerb picks how to bring the unit up. A scheduler that is already
// running is only replaced when its definition changed: restarting it stops
// the jobs it is running, and this is also reached when a live scheduler
// merely failed to answer in time.
func systemdStartVerb(changed bool) string {
	if changed {
		return "restart"
	}
	return "start"
}

func startSystemd(changed bool) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errors.New("systemctl not found: laterr needs systemd user services on Linux")
	}
	for _, args := range [][]string{
		{"--user", "daemon-reload"},
		{"--user", "enable", systemdUnit},
		{"--user", systemdStartVerb(changed), systemdUnit},
	} {
		if _, err := run("systemctl", args...); err != nil {
			return err
		}
	}
	return nil
}

// LingerEnabled reports whether the user's systemd services keep running
// while the user is logged out. known is false when it cannot be determined
// (including on systems other than Linux, where the question does not apply).
func LingerEnabled() (enabled, known bool) {
	if runtime.GOOS != "linux" {
		return false, false
	}
	u, err := user.Current()
	if err != nil {
		return false, false
	}
	out, err := run("loginctl", "show-user", u.Username, "--property=Linger")
	if err != nil {
		return false, false
	}
	switch strings.TrimPrefix(out, "Linger=") {
	case "yes":
		return true, true
	case "no":
		return false, true
	}
	return false, false
}

// --- macOS TCC ---

// ProtectedDir returns the macOS privacy-protected folder (as a path relative
// to home, e.g. "Documents") that contains dir, or "" if there is none. A
// background service touching these folders triggers a permission dialog or is
// denied outright.
func ProtectedDir(goos, home, dir string) string {
	if goos != "darwin" || home == "" {
		return ""
	}
	for _, name := range []string{
		"Documents", "Desktop", "Downloads",
		filepath.Join("Library", "Mobile Documents"), // iCloud Drive
		filepath.Join("Library", "CloudStorage"),     // third-party cloud folders
	} {
		p := filepath.Join(home, name)
		if dir == p || strings.HasPrefix(dir, p+string(filepath.Separator)) {
			return name
		}
	}
	return ""
}
