//go:build windows

package recoverymaterial

import (
	"os"

	"golang.org/x/sys/windows"
)

func hardenRecoveryPath(path string, directory bool) error {
	mode := os.FileMode(0o600)
	inheritance := uint32(0)
	if directory {
		mode = 0o700
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	entries := make([]windows.EXPLICIT_ACCESS, 0, 3)
	for _, trustee := range []struct {
		sid  *windows.SID
		kind windows.TRUSTEE_TYPE
	}{{user.User.Sid, windows.TRUSTEE_IS_USER}, {system, windows.TRUSTEE_IS_USER}, {admins, windows.TRUSTEE_IS_GROUP}} {
		entries = append(entries, windows.EXPLICIT_ACCESS{AccessPermissions: windows.GENERIC_ALL, AccessMode: windows.GRANT_ACCESS, Inheritance: inheritance, Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: trustee.kind, TrusteeValue: windows.TrusteeValueFromSID(trustee.sid)}})
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func recoveryPathIsReparsePoint(path string) (bool, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	attributes, err := windows.GetFileAttributes(pointer)
	if err != nil {
		return false, err
	}
	return attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
}

func syncRecoveryDirectory(path string) error {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(pointer, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	err = windows.FlushFileBuffers(handle)
	if err == windows.ERROR_ACCESS_DENIED || err == windows.ERROR_INVALID_HANDLE || err == windows.ERROR_INVALID_FUNCTION {
		return nil
	}
	return err
}
