//go:build beamtest

package control

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// CheckPrivate reports whether the file or directory at path is this user's
// alone as MkdirPrivate makes it: a protected DACL whose one ACE grants this
// user, inherited by what is made in it (D70). For tests.
func CheckPrivate(path string) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	return userOnlyDACL(sd, windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE)
}

// userOnlyDACL checks that sd's DACL is protected and has one ACE, which
// allows this user, with inherit's flags.
func userOnlyDACL(sd *windows.SECURITY_DESCRIPTOR, inherit uint8) error {
	me, err := userSID()
	if err != nil {
		return err
	}
	control, _, err := sd.Control()
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	switch {
	case err != nil:
		return err
	case control&windows.SE_DACL_PROTECTED == 0 || dacl == nil || dacl.AceCount != 1:
		return errors.New("not a protected DACL with one ACE")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		return err
	}
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !sid.Equals(me) ||
		ace.Header.AceFlags&(windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE) != inherit {
		return fmt.Errorf("ACE %+v for %s, want this user's, inherit flags %#x", ace.Header, sid, inherit)
	}
	return nil
}
