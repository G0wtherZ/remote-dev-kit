package main

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A generated pipe name is not a trust boundary. Verify its owner and DACL
// before allowing the CLI to send account configuration to it.
func verifyUserPipe(name string) error {
	p, e := windows.UTF16PtrFromString(name)
	if e != nil {
		return e
	}
	h, e := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OVERLAPPED|windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION, 0)
	if e != nil {
		return e
	}
	defer windows.CloseHandle(h)
	user, e := windows.GetCurrentProcessToken().GetTokenUser()
	if e != nil {
		return e
	}
	sd, e := windows.GetSecurityInfo(h, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if e != nil {
		return e
	}
	owner, _, e := sd.Owner()
	if e != nil {
		return e
	}
	if !owner.Equals(user.User.Sid) {
		return errors.New("私网管道不属于当前用户，拒绝连接")
	}
	dacl, _, e := sd.DACL()
	if e != nil {
		return e
	}
	if dacl == nil {
		return errors.New("私网管道缺少访问控制列表")
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if e = windows.GetAce(dacl, index, &ace); e != nil {
			return e
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("私网管道存在不支持的权限条目")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if ace.Mask != 0 && !sid.Equals(user.User.Sid) {
			return errors.New("私网管道允许其他用户访问，拒绝连接")
		}
	}
	return nil
}
