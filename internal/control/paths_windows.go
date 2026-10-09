package control

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// configDir is %LOCALAPPDATA%\beam (docs/02).
func configDir(getenv func(string) string) (string, error) {
	if getenv("LOCALAPPDATA") == "" {
		return "", errors.New("no BEAM_CONFIG_DIR or LOCALAPPDATA")
	}
	return filepath.Join(getenv("LOCALAPPDATA"), "beam"), nil
}

// pipePrefix begins every local named pipe's name.
const pipePrefix = `\\.\pipe\`

// maxPipe is the longest pipe name Windows takes, in characters.
const maxPipe = 256

// defaultSocket is the pipe named for dir (docs/06): beam- and the first 16
// bytes of the SHA-256 of its absolute path, lowercased, in hex: a pipe per
// directory, and one for every spelling of a directory, as NTFS ignores case.
func defaultSocket(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	sum := sha256.Sum256([]byte(strings.ToLower(dir)))
	return pipePrefix + "beam-" + hex.EncodeToString(sum[:16])
}

// checkSocket refuses a socket that is not a local pipe's name.
func checkSocket(sock string) error {
	name, ok := strings.CutPrefix(sock, pipePrefix)
	switch {
	case !ok || name == "" || strings.Contains(name, `\`):
		return fmt.Errorf("not a pipe name: %s; set BEAM_SOCKET to %s<name>", sock, pipePrefix)
	case len(utf16.Encode([]rune(sock))) > maxPipe:
		return fmt.Errorf("pipe name too long (at most %d characters): %s; set BEAM_SOCKET to a shorter one", maxPipe, sock)
	}
	return nil
}

// MkdirPrivate makes dir, and any parent it lacks, for this user alone: each
// directory it makes has a protected DACL that grants this user, and what is
// made in it, everything, and no one else anything. One that exists is left
// as it is.
func MkdirPrivate(dir string) error {
	st, err := os.Stat(dir)
	switch {
	case err == nil && st.IsDir():
		return nil
	case err == nil:
		return &fs.PathError{Op: "mkdir", Path: dir, Err: windows.ERROR_DIRECTORY}
	}
	if parent := filepath.Dir(dir); parent != dir {
		if err := MkdirPrivate(parent); err != nil {
			return err
		}
	}
	sa, err := userOnly("OICI")
	if err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	if err := windows.CreateDirectory(p, sa); err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return &fs.PathError{Op: "mkdir", Path: dir, Err: err}
	}
	return nil
}

// userOnly is security attributes owned by this user, with a protected DACL
// that grants this user everything and no one else anything; inherit is the
// ACE's inheritance flags.
func userOnly(inherit string) (*windows.SecurityAttributes, error) {
	sid, err := userSID()
	if err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString(fmt.Sprintf("O:%[1]sD:P(A;%[2]s;GA;;;%[1]s)", sid, inherit))
	if err != nil {
		return nil, err
	}
	sa := &windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(*sa))
	return sa, nil
}

// userSID is the SID of the user this process runs as.
func userSID() (*windows.SID, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return u.User.Sid, nil
}
