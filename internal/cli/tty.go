package cli

import (
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// hasTerminal reports whether this process has a controlling terminal.
func hasTerminal() bool {
	f, err := os.Open("/dev/tty")
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// terminalSize returns the columns and rows of the terminal w writes to, or
// zeros if it is not one.
func terminalSize(w io.Writer) (cols, rows int) {
	f, ok := w.(*os.File)
	if !ok {
		return 0, 0
	}
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return 0, 0
	}
	return int(ws.Col), int(ws.Row)
}

// parentGroup returns the process group of the process that started laterr,
// normally the interactive shell, or 0 if it cannot be determined.
func parentGroup() int {
	pgid, err := syscall.Getpgid(os.Getppid())
	if err != nil {
		return 0
	}
	return pgid
}

// foregroundGroup returns the process group that currently owns the
// controlling terminal, or 0 if there is no terminal to ask.
func foregroundGroup() int {
	f, err := os.Open("/dev/tty")
	if err != nil {
		return 0
	}
	defer f.Close()
	pgid, err := unix.IoctlGetInt(int(f.Fd()), unix.TIOCGPGRP)
	if err != nil {
		return 0
	}
	return pgid
}

// atPrompt reports whether the terminal is sitting at a shell prompt rather
// than running something in the foreground. Writing into a full-screen program
// such as an editor or a pager corrupts its display, so the follower holds its
// output back until this is true again.
//
// shell is the process group laterr was started from. The session leader
// counts as well: it is the shell the terminal was opened with, which covers
// laterr having been started from a script that has since exited. Without a
// terminal there is nothing to protect, and the answer is yes.
func atPrompt(shell int) bool {
	fg := foregroundGroup()
	if fg == 0 || fg == shell {
		return true
	}
	sid, err := unix.Getsid(0)
	return err == nil && fg == sid
}

// joinPrompt moves this process into the process group that owns the terminal,
// if the terminal is at a prompt (see atPrompt) and it is not there already.
// Ctrl-C is delivered to that group only, which is the shell's while it shows
// its prompt, and the follower is otherwise in a background group that never
// hears of it.
func joinPrompt(shell int) {
	fg := foregroundGroup()
	if fg == 0 || fg == syscall.Getpgrp() || !atPrompt(shell) {
		return
	}
	syscall.Setpgid(0, fg)
}

// redrawPrompt gets the shell to draw its prompt again. The follower writes
// below a prompt the shell has already drawn, which leaves the cursor on an
// empty line with no prompt, and the shell cannot know.
//
// There is no way to ask a shell for this from outside, but shells redraw the
// prompt, along with anything typed so far, when the terminal is resized. So
// the terminal is reported one row shorter for a moment and then put back.
// zsh ignores a SIGWINCH that comes without a change in size, so the signal
// alone would not do.
func redrawPrompt() {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return
	}
	defer f.Close()
	fd := int(f.Fd())
	ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
	if err != nil || ws.Row < 2 {
		return
	}
	// The follower is a background process, and changing the terminal from
	// the background stops the process unless SIGTTOU is ignored.
	signal.Ignore(syscall.SIGTTOU)
	short := *ws
	short.Row--
	if unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &short) != nil {
		return
	}
	// Long enough for the shell to see the first size before the second.
	time.Sleep(50 * time.Millisecond)
	// Put the size back unless the terminal was really resized meanwhile.
	if now, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ); err == nil && *now == short {
		unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, ws)
	}
}
