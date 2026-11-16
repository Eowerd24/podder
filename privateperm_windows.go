//go:build windows

package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var reOpenFile = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReOpenFile")

func reopenPrivateDirectoryHandle(file *os.File, desiredAccess uint32) (windows.Handle, error) {
	handle, _, callErr := reOpenFile.Call(
		file.Fd(),
		uintptr(desiredAccess),
		uintptr(windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE),
		uintptr(windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS),
	)
	if windows.Handle(handle) == windows.InvalidHandle {
		if callErr != nil && callErr != syscall.Errno(0) {
			return windows.InvalidHandle, callErr
		}
		return windows.InvalidHandle, syscall.EINVAL
	}
	return windows.Handle(handle), nil
}

func windowsPrivatePrincipals() ([]*windows.SID, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return nil, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	system, err := windows.StringToSid("S-1-5-18")
	if err != nil {
		return nil, err
	}
	administrators, err := windows.StringToSid("S-1-5-32-544")
	if err != nil {
		return nil, err
	}
	return []*windows.SID{user.User.Sid, system, administrators}, nil
}

func windowsPrivateACL(principals []*windows.SID) (*windows.ACL, error) {
	entries := make([]windows.EXPLICIT_ACCESS, 0, len(principals))
	for i, sid := range principals {
		kind := windows.TRUSTEE_TYPE(windows.TRUSTEE_IS_GROUP)
		if i == 0 {
			kind = windows.TRUSTEE_IS_USER
		}
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  kind,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		})
	}
	return windows.ACLFromEntries(entries, nil)
}

func securePrivateDirectoryPermissions(file *os.File, path string) error {
	principals, err := windowsPrivatePrincipals()
	if err != nil {
		return err
	}
	acl, err := windowsPrivateACL(principals)
	if err != nil {
		return err
	}
	handle, err := reopenPrivateDirectoryHandle(file, windows.READ_CONTROL|windows.WRITE_DAC)
	if err != nil {
		return fmt.Errorf("reopen private directory handle for ACL update: %w", err)
	}
	defer windows.CloseHandle(handle)
	return windows.SetSecurityInfo(
		handle, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil,
	)
}

func preparePrivateRootPermissions(file *os.File, path string) error {
	if err := securePrivateDirectoryPermissions(file, path); err != nil {
		return err
	}
	principals, err := windowsPrivatePrincipals()
	if err != nil {
		return err
	}
	descriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()), windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || !owner.Equals(principals[0]) {
		return fmt.Errorf("storage root is not owned by the current Windows user: %s", path)
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("storage root ACL inheritance is not protected: %s", path)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("storage root has no private DACL: %s", path)
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil || ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("storage root has an unsupported ACL entry: %s", path)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		allowed := false
		for _, principal := range principals {
			if sid.Equals(principal) {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("storage root grants access to another Windows principal: %s", path)
		}
	}
	return nil
}
