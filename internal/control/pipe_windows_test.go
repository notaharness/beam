package control

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// testPipe is a pipe name no other test uses.
func testPipe(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	rand.Read(b)
	return pipePrefix + "beam-test-" + hex.EncodeToString(b)
}

func serve(t *testing.T, path string) net.Listener {
	t.Helper()
	ln, err := listen(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}

// docs/06: a client talks only to a daemon its own user runs, by the owner
// of the pipe it connected to.
func TestDialOwnPipeOnly(t *testing.T) {
	path := testPipe(t)
	ln := serve(t, path)
	go func() {
		if c, err := ln.Accept(); err == nil {
			t.Cleanup(func() { c.Close() })
		}
	}()
	c, err := Dial(Paths{Socket: path})
	if err != nil {
		t.Fatalf("dial this user's pipe: %v", err)
	}
	defer c.Close()
	system, _ := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err := listenedBy(c.c.(*pipeConn), system); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("a pipe another user owns: %v, want a permission error", err)
	}
	me, _ := userSID()
	if err := listenedBy(c.c.(*pipeConn), me); err != nil {
		t.Errorf("a pipe this user owns: %v", err)
	}
}

// docs/06: a starting daemon takes over no pipe name that is held: by
// another daemon of this user's, or by a pipe another owner made, which
// clients refuse too. Naming Administrators the owner takes an elevated
// process, as CI's is.
func TestListenHeldPipe(t *testing.T) {
	path := testPipe(t)
	serve(t, path)
	if _, err := listen(path); err == nil || !strings.Contains(err.Error(), "another daemon is listening") {
		t.Errorf("listen on a pipe this user serves: %v", err)
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("naming Administrators the owner of a pipe needs an elevated process")
	}
	other := testPipe(t)
	sd, err := windows.SecurityDescriptorFromString("O:BAD:(A;;GA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	sa := &windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(*sa))
	name, _ := windows.UTF16PtrFromString(other)
	for range 2 { // an instance waiting for each client
		h, err := windows.CreateNamedPipe(name, windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED,
			windows.PIPE_TYPE_BYTE, windows.PIPE_UNLIMITED_INSTANCES, 512, 512, 0, sa)
		if err != nil {
			t.Fatal(err)
		}
		defer windows.CloseHandle(h)
		ev, _ := windows.CreateEvent(nil, 1, 0, nil)
		defer windows.CloseHandle(ev)
		ov := &windows.Overlapped{HEvent: ev}
		if err := windows.ConnectNamedPipe(h, ov); !errors.Is(err, windows.ERROR_IO_PENDING) {
			t.Fatalf("waiting for a client: %v", err)
		}
		defer windows.CancelIoEx(h, ov)
	}
	if _, err := listen(other); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("listen on a pipe Administrators own: %v, want a permission error", err)
	}
	if _, err := Dial(Paths{Socket: other}); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("dial a pipe Administrators own: %v, want a permission error", err)
	}
}

// The pipe carries bytes both ways at once, each end sees the other's close
// as the end of what it reads, after what was written before it, deadlines
// hold, and Close ends a waiting Accept.
func TestPipeConn(t *testing.T) {
	path := testPipe(t)
	ln := serve(t, path)
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- c
	}()
	c, err := openSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s := <-accepted
	if s == nil {
		t.Fatal("no connection accepted")
	}
	s.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, err := s.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("a read with nothing sent: %v, want the deadline", err)
	}
	s.SetReadDeadline(time.Time{})
	go s.Write([]byte("from the daemon\n"))
	if _, err := c.Write([]byte("from the client\n")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 16)
	if _, err := io.ReadFull(s, got); err != nil || string(got) != "from the client\n" {
		t.Errorf("the daemon read %q, %v", got, err)
	}
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "from the daemon\n" {
		t.Errorf("the client read %q, %v", got, err)
	}
	s.Write([]byte("last"))
	s.Close()
	if b, err := io.ReadAll(c); err != nil || string(b) != "last" {
		t.Errorf("after the daemon's close the client read %q, %v; want last, then the end", b, err)
	}
	waiting := make(chan error, 1)
	go func() { _, err := ln.Accept(); waiting <- err }()
	time.Sleep(100 * time.Millisecond)
	ln.Close()
	select {
	case err := <-waiting:
		if !errors.Is(err, net.ErrClosed) {
			t.Errorf("Accept after Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close left Accept waiting")
	}
	if _, err := openSocket(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("dial after Close: %v, want no such pipe", err)
	}
}

// docs/06: a client that leaves what the daemon writes unread is cut off by
// the write deadline, on a pipe as on a socket.
func TestPipeWriteDeadline(t *testing.T) {
	path := testPipe(t)
	ln := serve(t, path)
	accepted := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); accepted <- c }()
	c, err := openSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s := <-accepted
	defer s.Close()
	s.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
	chunk := make([]byte, 64<<10)
	for range 64 {
		if _, err = s.Write(chunk); err != nil {
			break
		}
	}
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("writing to a client that reads nothing: %v, want the deadline", err)
	}
}

// docs/06: the lock admits one daemon to a directory at a time.
func TestLockOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beam.lock")
	unlock, err := lock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock(path); !errors.Is(err, ErrLocked) {
		t.Errorf("a second lock: %v, want ErrLocked", err)
	}
	unlock()
	again, err := lock(path)
	if err != nil {
		t.Fatalf("the lock once released: %v", err)
	}
	again()
}

// docs/02: the directories beam makes are this user's alone: a protected
// DACL with one ACE, granting this user everything, inherited by what is
// made in them.
func TestMkdirPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "beam", "run")
	if err := MkdirPrivate(dir); err != nil {
		t.Fatal(err)
	}
	me, _ := userSID()
	for _, d := range []string{filepath.Dir(dir), dir} {
		sd, err := windows.GetNamedSecurityInfo(d, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		control, _, _ := sd.Control()
		dacl, _, _ := sd.DACL()
		if control&windows.SE_DACL_PROTECTED == 0 || dacl == nil || dacl.AceCount != 1 {
			t.Fatalf("%s: control %#x, %v ACEs; want a protected DACL with one", d, control, dacl)
		}
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, 0, &ace); err != nil {
			t.Fatal(err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !sid.Equals(me) ||
			ace.Header.AceFlags&(windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE) == 0 {
			t.Errorf("%s: ACE %+v for %s, want this user's, inherited", d, ace.Header, sid)
		}
	}
	if err := MkdirPrivate(dir); err != nil {
		t.Errorf("a directory that exists: %v", err)
	}
}

// docs/06 table: the pipe a directory names by default, and BEAM_SOCKET.
func TestResolvePipe(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "beam")
	pipe := defaultSocket(dir)
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string // the socket; "" for an error
	}{
		{"BEAM_CONFIG_DIR", map[string]string{"BEAM_CONFIG_DIR": dir}, pipe},
		{"another spelling of it", map[string]string{"BEAM_CONFIG_DIR": strings.ToUpper(dir)}, pipe},
		{"LOCALAPPDATA", map[string]string{"LOCALAPPDATA": base}, pipe},
		{"neither", nil, ""},
		{"a pipe", map[string]string{"BEAM_CONFIG_DIR": dir, "BEAM_SOCKET": `\\.\pipe\mine`}, `\\.\pipe\mine`},
		{"a path", map[string]string{"BEAM_CONFIG_DIR": dir, "BEAM_SOCKET": `C:\beam.sock`}, ""},
		{"a remote pipe", map[string]string{"BEAM_CONFIG_DIR": dir, "BEAM_SOCKET": `\\host\pipe\mine`}, ""},
		{"a nested name", map[string]string{"BEAM_CONFIG_DIR": dir, "BEAM_SOCKET": `\\.\pipe\a\b`}, ""},
		{"no name", map[string]string{"BEAM_CONFIG_DIR": dir, "BEAM_SOCKET": `\\.\pipe\`}, ""},
		{"too long", map[string]string{"BEAM_CONFIG_DIR": dir, "BEAM_SOCKET": `\\.\pipe\` + strings.Repeat("x", 248)}, ""},
		{"the longest", map[string]string{"BEAM_CONFIG_DIR": dir, "BEAM_SOCKET": `\\.\pipe\` + strings.Repeat("x", 247)},
			`\\.\pipe\` + strings.Repeat("x", 247)},
	} {
		p, err := ResolvePaths(func(k string) string { return tc.env[k] })
		if got := p.Socket; tc.want == "" && err == nil || tc.want != "" && (err != nil || got != tc.want) {
			t.Errorf("%s: %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
	if other := defaultSocket(filepath.Join(base, "other")); other == pipe {
		t.Errorf("two directories share %s", pipe)
	}
}
