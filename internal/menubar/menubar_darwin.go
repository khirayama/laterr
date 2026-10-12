//go:build darwin

package menubar

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// AppKit is reached through the Objective-C runtime rather than cgo, which
// laterr does without (so that every release target builds from anywhere).

// AppKit only works on the thread the process started on. Package
// initialization runs there, and locking keeps the main goroutine on it.
func init() { runtime.LockOSThread() }

const (
	activationPolicyAccessory = 1  // NSApplicationActivationPolicyAccessory: no Dock icon
	variableStatusItemLength  = -1 // NSVariableStatusItemLength
	imageLeft                 = 2  // NSImageLeft
)

var (
	selAlloc     = objc.RegisterName("alloc")
	selInit      = objc.RegisterName("init")
	selRetain    = objc.RegisterName("retain")
	selTag       = objc.RegisterName("tag")
	selTick      = objc.RegisterName("tick:")
	selRunJob    = objc.RegisterName("runJob:")
	selCancelJob = objc.RegisterName("cancelJob:")
	selStopJob   = objc.RegisterName("stopJob:")
	selHide      = objc.RegisterName("hide:")
)

func send(id objc.ID, sel string, args ...any) objc.ID {
	return id.Send(objc.RegisterName(sel), args...)
}

func class(name string) objc.ID { return objc.ID(objc.GetClass(name)) }

func nsString(s string) objc.ID {
	return send(class("NSString"), "stringWithUTF8String:", s)
}

type bar struct {
	c      Config
	status objc.ID // NSStatusBar
	item   objc.ID // NSStatusItem
	menu   objc.ID // NSMenu
	target objc.ID // receives the timer and the menu's actions
	jobs   []Job
}

// Run shows the icon until Config.Jobs says there is nothing left to show, or
// the user hides it. Once the icon is up, that ends the process: Run only
// returns if there was nothing to show, or no way to show it. It must be
// called from the main goroutine.
func Run(c Config) error {
	jobs, show := c.Jobs()
	if !show {
		return nil
	}
	if _, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_GLOBAL|purego.RTLD_NOW); err != nil {
		return fmt.Errorf("cannot load AppKit: %v", err)
	}
	if !objc.Send[bool](class("NSThread"), objc.RegisterName("isMainThread")) {
		return errors.New("the menu bar icon must be started on the main thread")
	}
	b := &bar{c: c, jobs: jobs}

	cls, err := objc.RegisterClass("LaterrMenuTarget", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
		{Cmd: selTick, Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) { b.tick() }},
		{Cmd: objc.RegisterName("menuNeedsUpdate:"), Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) { b.fill() }},
		{Cmd: selRunJob, Fn: func(_ objc.ID, _ objc.SEL, sender objc.ID) { b.c.RunNow(tag(sender)) }},
		{Cmd: selCancelJob, Fn: func(_ objc.ID, _ objc.SEL, sender objc.ID) { b.c.Cancel(tag(sender)) }},
		{Cmd: selStopJob, Fn: func(_ objc.ID, _ objc.SEL, sender objc.ID) { b.c.Stop(tag(sender)) }},
		{Cmd: selHide, Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) {
			b.c.Hide()
			b.quit()
		}},
	})
	if err != nil {
		return err
	}
	b.target = objc.ID(cls).Send(selAlloc).Send(selInit)

	app := send(class("NSApplication"), "sharedApplication")
	send(app, "setActivationPolicy:", activationPolicyAccessory)

	b.status = send(class("NSStatusBar"), "systemStatusBar")
	b.item = send(b.status, "statusItemWithLength:", float64(variableStatusItemLength)).Send(selRetain)
	button := send(b.item, "button")
	if image := send(class("NSImage"), "imageWithSystemSymbolName:accessibilityDescription:", nsString("clock"), nsString("laterr")); image != 0 {
		send(button, "setImage:", image)
		send(button, "setImagePosition:", imageLeft)
	}
	send(button, "setToolTip:", nsString("laterr"))

	// The menu is filled in when it is opened (menuNeedsUpdate:), so that it
	// never changes under the pointer.
	b.menu = send(class("NSMenu"), "alloc").Send(selInit)
	send(b.menu, "setAutoenablesItems:", false)
	send(b.menu, "setDelegate:", b.target)
	send(b.item, "setMenu:", b.menu)
	b.title()

	send(class("NSTimer"), "scheduledTimerWithTimeInterval:target:selector:userInfo:repeats:",
		float64(1), b.target, selTick, objc.ID(0), true)
	send(app, "run")
	return nil
}

func tag(sender objc.ID) int64 { return objc.Send[int64](sender, selTag) }

// tick looks at the jobs again. It does not run while the menu is open.
func (b *bar) tick() {
	jobs, show := b.c.Jobs()
	if !show {
		b.quit()
		return
	}
	b.jobs = jobs
	b.title()
}

// title puts the number of jobs next to the icon.
func (b *bar) title() {
	send(send(b.item, "button"), "setTitle:", nsString(fmt.Sprint(len(b.jobs))))
}

// quit takes the icon away and ends the process. The application's run loop
// cannot simply be left from a timer or a menu action.
func (b *bar) quit() {
	send(b.status, "removeStatusItem:", b.item)
	os.Exit(0)
}

func (b *bar) fill() {
	if jobs, show := b.c.Jobs(); show {
		b.jobs = jobs
		b.title()
	}
	send(b.menu, "removeAllItems")
	for _, j := range b.jobs {
		line := b.add(b.menu, j.Title, 0, 0)
		sub := send(class("NSMenu"), "alloc").Send(selInit)
		send(sub, "setAutoenablesItems:", false)
		if j.Running {
			b.add(sub, "Stop Job", selStopJob, j.ID)
		} else {
			b.add(sub, "Run Now", selRunJob, j.ID)
			b.add(sub, "Cancel Job", selCancelJob, j.ID)
		}
		send(line, "setSubmenu:", sub)
	}
	if len(b.jobs) == 0 {
		send(b.add(b.menu, "No pending or running jobs", 0, 0), "setEnabled:", false)
	}
	send(b.menu, "addItem:", send(class("NSMenuItem"), "separatorItem"))
	b.add(b.menu, "Hide Menu Bar Icon", selHide, 0)
}

// add appends an item to menu. With an action, picking it sends that action
// to the target, and the action finds id as the item's tag.
func (b *bar) add(menu objc.ID, title string, action objc.SEL, id int64) objc.ID {
	item := send(class("NSMenuItem"), "alloc")
	item = send(item, "initWithTitle:action:keyEquivalent:", nsString(title), action, nsString(""))
	if action != 0 {
		send(item, "setTarget:", b.target)
		send(item, "setTag:", id)
	}
	send(menu, "addItem:", item)
	return item
}
