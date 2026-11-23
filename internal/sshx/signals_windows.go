//go:build windows

package sshx

import (
	"os"
	"os/signal"
	"sync"
	"syscall"

	"golang.org/x/sys/windows"
)

func signalNotify(ch chan<- os.Signal, sigs ...os.Signal) { signal.Notify(ch, sigs...) }
func signalStop(ch chan<- os.Signal)                      { signal.Stop(ch) }

func terminationSignals() []os.Signal { return []os.Signal{os.Interrupt} }

var (
	consoleMu       sync.Mutex
	consoleHandlers = map[uint64]func(){}
	consoleNextID   uint64
	consoleCallback uintptr
	kernel32DLL     = windows.NewLazySystemDLL("kernel32.dll")
	procSetCtrl     = kernel32DLL.NewProc("SetConsoleCtrlHandler")
)

// installConsoleClose installs (once) a CTRL_CLOSE_EVENT/CTRL_LOGOFF_EVENT/
// CTRL_SHUTDOWN_EVENT handler so --rm teardown also runs when the console
// window is closed (doc 04 §4). The callback is kept alive for the process
// lifetime, as required by syscall.NewCallback.
func installConsoleClose(fn func()) func() {
	consoleMu.Lock()
	defer consoleMu.Unlock()
	if consoleCallback == 0 {
		consoleCallback = syscall.NewCallback(func(ctrlType uint32) uintptr {
			switch ctrlType {
			case windows.CTRL_CLOSE_EVENT, windows.CTRL_LOGOFF_EVENT, windows.CTRL_SHUTDOWN_EVENT:
				consoleMu.Lock()
				handlers := make([]func(), 0, len(consoleHandlers))
				for _, h := range consoleHandlers {
					handlers = append(handlers, h)
				}
				consoleMu.Unlock()
				for _, h := range handlers {
					h()
				}
				return 1
			}
			return 0
		})
		ret, _, _ := procSetCtrl.Call(consoleCallback, 1)
		if ret == 0 {
			consoleCallback = 0
			return func() {}
		}
	}
	consoleNextID++
	id := consoleNextID
	consoleHandlers[id] = fn
	return func() {
		consoleMu.Lock()
		delete(consoleHandlers, id)
		consoleMu.Unlock()
	}
}
