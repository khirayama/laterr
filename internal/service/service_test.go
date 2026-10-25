package service

import (
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

func TestLaunchdPlist(t *testing.T) {
	got := launchdPlist("/opt/a&b <x>/laterr", "/Users/me/Library/Application Support/laterr/scheduler.log")
	// Must be well-formed XML with the path escaped, not injected as markup.
	dec := xml.NewDecoder(strings.NewReader(got))
	var texts []string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("plist is not well-formed: %v\n%s", err, got)
		}
		if c, ok := tok.(xml.CharData); ok {
			if s := strings.TrimSpace(string(c)); s != "" {
				texts = append(texts, s)
			}
		}
	}
	joined := strings.Join(texts, "|")
	for _, want := range []string{
		"Label|com.laterr.scheduler",
		"ProgramArguments|/opt/a&b <x>/laterr|daemon",
		"StandardErrorPath|/Users/me/Library/Application Support/laterr/scheduler.log",
		"KeepAlive", "RunAtLoad",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("plist lacks %q:\n%s", want, joined)
		}
	}
}

func TestSystemdUnitFile(t *testing.T) {
	got := systemdUnitFile(`/home/me/my "bin" $x 100%/laterr`)
	want := `ExecStart="/home/me/my \"bin\" $$x 100%%/laterr" daemon` + "\n"
	if !strings.Contains(got, want) {
		t.Errorf("unit file lacks %q:\n%s", want, got)
	}
	for _, s := range []string{"[Unit]", "[Service]", "Restart=always", "[Install]", "WantedBy=default.target"} {
		if !strings.Contains(got, s) {
			t.Errorf("unit file lacks %q", s)
		}
	}
}

// An unchanged, possibly running scheduler must not be restarted: that would
// interrupt its jobs.
func TestSystemdStartVerb(t *testing.T) {
	if got := systemdStartVerb(false); got != "start" {
		t.Errorf("unchanged unit: %q", got)
	}
	if got := systemdStartVerb(true); got != "restart" {
		t.Errorf("changed unit: %q", got)
	}
}

func TestDefinedBinaryRoundTrip(t *testing.T) {
	for _, bin := range []string{
		"/opt/homebrew/bin/laterr",
		"/opt/a&b <x>/laterr",
		`/home/me/my "bin" $x 100%\dir/laterr`,
		"/home/me/line\nEnvironment=INJECTED\r\ttab/laterr",
		`/home/me/literal\n\r\t/laterr`,
	} {
		if got, err := definedBinary("darwin", launchdPlist(bin, "/tmp/log")); err != nil || got != bin {
			t.Errorf("launchd %q: got %q, %v", bin, got, err)
		}
		if got, err := definedBinary("linux", systemdUnitFile(bin)); err != nil || got != bin {
			t.Errorf("systemd %q: got %q, %v", bin, got, err)
		}
		if strings.Contains(systemdUnitFile(bin), "\nEnvironment=") {
			t.Errorf("path injected a systemd directive: %q", bin)
		}
	}
	if _, err := definedBinary("darwin", "garbage"); err == nil {
		t.Error("expected an error for an unrecognized definition")
	}
}

func TestProtectedDir(t *testing.T) {
	const home = "/Users/me"
	cases := map[string]string{
		"/Users/me/Documents":                     "Documents",
		"/Users/me/Documents/work/app":            "Documents",
		"/Users/me/Desktop":                       "Desktop",
		"/Users/me/Downloads/x":                   "Downloads",
		"/Users/me/Library/Mobile Documents/a/b":  "Library/Mobile Documents",
		"/Users/me/Library/CloudStorage/Dropbox":  "Library/CloudStorage",
		"/Users/me/Documents2":                    "",
		"/Users/me/projects/Documents":            "",
		"/Users/me":                               "",
		"/Users/other/Documents":                  "",
		"/tmp":                                    "",
		"/Users/me/Library/Application Support/x": "",
	}
	for dir, want := range cases {
		if got := ProtectedDir("darwin", home, dir); got != want {
			t.Errorf("%s: got %q, want %q", dir, got, want)
		}
	}
	if got := ProtectedDir("linux", "/home/me", "/home/me/Documents"); got != "" {
		t.Errorf("linux: got %q", got)
	}
}
