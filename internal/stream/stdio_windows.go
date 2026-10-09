package stream

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// stdio is a process's standard streams: three pipes, each a named pipe of
// the user's alone with one instance, its end here overlapped, so that the
// runtime's poller serves it and a close cancels what is pending on it, and
// the child's end synchronous and inheritable, as a program expects of its
// standard handles.
type stdio struct {
	stdin          *os.File
	stdout, stderr *os.File
	child          [3]windows.Handle
}

func stdioPipes() (*stdio, error) {
	sa, err := userOnly()
	if err != nil {
		return nil, err
	}
	p := &stdio{}
	var errs [3]error
	p.stdin, p.child[0], errs[0] = pipePair(sa, false)
	p.stdout, p.child[1], errs[1] = pipePair(sa, true)
	p.stderr, p.child[2], errs[2] = pipePair(sa, true)
	if err := errors.Join(errs[:]...); err != nil {
		return nil, errors.Join(err, p.closeParent(), p.closeChild())
	}
	return p, nil
}

// closeChild closes the child's ends here, once it holds them.
func (p *stdio) closeChild() error {
	var err error
	for _, h := range p.child {
		if h != 0 {
			err = errors.Join(err, windows.CloseHandle(h))
		}
	}
	return err
}

func (p *stdio) closeParent() error {
	var err error
	for _, f := range []*os.File{p.stdin, p.stdout, p.stderr} {
		if f != nil {
			err = errors.Join(err, f.Close())
		}
	}
	return err
}

// pipePair makes a pipe and returns its end here, which reads if in, and the
// child's end.
func pipePair(sa *windows.SecurityAttributes, in bool) (*os.File, windows.Handle, error) {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // never fails
	name, err := windows.UTF16PtrFromString(`\\.\pipe\beam-stdio-` + hex.EncodeToString(b))
	if err != nil {
		return nil, 0, err
	}
	access, childAccess := uint32(windows.PIPE_ACCESS_OUTBOUND), uint32(windows.GENERIC_READ)
	if in {
		access, childAccess = windows.PIPE_ACCESS_INBOUND, windows.GENERIC_WRITE
	}
	h, err := windows.CreateNamedPipe(name, access|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		1, pipeBuffer, pipeBuffer, 0, sa)
	if err != nil {
		return nil, 0, err
	}
	inheritable := windows.SecurityAttributes{InheritHandle: 1}
	inheritable.Length = uint32(unsafe.Sizeof(inheritable))
	child, err := windows.CreateFile(name, childAccess, 0, &inheritable, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, 0, errors.Join(err, windows.CloseHandle(h))
	}
	return os.NewFile(uintptr(h), "stdio"), child, nil
}

// pipeBuffer is each pipe's buffer, either way.
const pipeBuffer = 64 << 10

// userOnly is security attributes with a protected DACL that grants this
// user everything and no one else anything.
func userOnly() (*windows.SecurityAttributes, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + u.User.Sid.String() + ")")
	if err != nil {
		return nil, err
	}
	sa := &windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(*sa))
	return sa, nil
}
