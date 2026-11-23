//go:build windows

package sshx

import (
	"io"
	"net"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

// dialNamedPipe opens the Windows OpenSSH agent's named pipe with CreateFile;
// the stdlib has no "npipe" network, and this keeps the dependency set small.
func dialNamedPipe(path string) (net.Conn, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return &pipeConn{h: h}, nil
}

type pipeConn struct {
	h    windows.Handle
	once sync.Once
}

func (c *pipeConn) Read(p []byte) (int, error) {
	var done uint32
	if err := windows.ReadFile(c.h, p, &done, nil); err != nil {
		return 0, err
	}
	if done == 0 {
		return 0, io.EOF
	}
	return int(done), nil
}

func (c *pipeConn) Write(p []byte) (int, error) {
	var done uint32
	if err := windows.WriteFile(c.h, p, &done, nil); err != nil {
		return 0, err
	}
	return int(done), nil
}

func (c *pipeConn) Close() error {
	var err error
	c.once.Do(func() { err = windows.CloseHandle(c.h) })
	return err
}

func (c *pipeConn) LocalAddr() net.Addr  { return pipeAddr{} }
func (c *pipeConn) RemoteAddr() net.Addr { return pipeAddr{} }

func (c *pipeConn) SetDeadline(time.Time) error      { return nil }
func (c *pipeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *pipeConn) SetWriteDeadline(time.Time) error { return nil }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "npipe" }
func (pipeAddr) String() string  { return `\\.\pipe\openssh-ssh-agent` }
