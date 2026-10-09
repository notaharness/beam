package stream

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// stdio is a process's standard streams: three anonymous pipes, the child's
// ends inheritable. A write the process is not reading ends with the
// process, which the stream's end kills.
type stdio struct {
	stdin          *os.File
	stdout, stderr *os.File
	child          [3]windows.Handle
}

func stdioPipes() (*stdio, error) {
	p := &stdio{}
	var errs [3]error
	p.stdin, p.child[0], errs[0] = pipePair(false)
	p.stdout, p.child[1], errs[1] = pipePair(true)
	p.stderr, p.child[2], errs[2] = pipePair(true)
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

// pipePair makes an anonymous pipe and returns its end here, which reads if
// reads, and the child's end, inheritable.
func pipePair(reads bool) (*os.File, windows.Handle, error) {
	var r, w windows.Handle
	if err := windows.CreatePipe(&r, &w, nil, 0); err != nil {
		return nil, 0, err
	}
	here, child := w, r
	if reads {
		here, child = r, w
	}
	if err := windows.SetHandleInformation(child, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
		return nil, 0, errors.Join(err, windows.CloseHandle(r), windows.CloseHandle(w))
	}
	return os.NewFile(uintptr(here), "stdio"), child, nil
}
