//go:build windows

// Package sandboxtest contains Windows host fixtures used only by real Local
// Sandbox conformance tests.
package sandboxtest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	localSandboxHostMutexName = `Local\TraverseBoard.LocalSandbox.Test.Host.v1`
	prepareSystemDriveEnv     = "TRAVERSE_BOARD_LOCAL_SANDBOX_TEST_HOST_PREP"
	systemDriveMetadataAccess = windows.ACCESS_MASK(windows.FILE_READ_ATTRIBUTES |
		windows.FILE_READ_EA | windows.READ_CONTROL | windows.SYNCHRONIZE)
	systemMandatoryLabelACEType   = 0x11
	systemMandatoryLabelNoWriteUp = 0x1
)

var setFileSecurityWProc = windows.NewLazySystemDLL("advapi32.dll").NewProc(
	"SetFileSecurityW",
)

// PrepareHost supplies the Windows host ACLs required by LPAC subprocesses. It
// returns an exact restoration function except for the explicitly gated,
// disposable GitHub-hosted runner fallback. A named mutex serializes test
// packages while either machine-wide security descriptor is changed.
func PrepareHost() (func() error, error) {
	releaseMutex, err := acquireHostMutex()
	if err != nil {
		return nil, err
	}
	fail := func(cause error, restores ...func() error) (func() error, error) {
		return nil, errors.Join(cause, runRestores(restores...), releaseMutex())
	}

	restoreDrive := func() error { return nil }
	driveChanged := false
	if os.Getenv(prepareSystemDriveEnv) == "1" {
		restoreDrive, driveChanged, err = prepareSystemDrive()
		if err != nil {
			return fail(fmt.Errorf("prepare system-drive metadata ACL: %w", err))
		}
	}
	restoreNull, nullChanged, err := prepareNullDevice()
	if err != nil {
		return fail(fmt.Errorf("prepare null-device security: %w", err), restoreDrive)
	}
	if !driveChanged && !nullChanged {
		return func() error { return nil }, releaseMutex()
	}
	return func() error {
		return errors.Join(runRestores(restoreNull, restoreDrive), releaseMutex())
	}, nil
}

// CanonicalRoot resolves an existing test directory to the final local path
// expected by the Local Sandbox root-pinning boundary. GitHub-hosted Windows
// runners may expose temporary directories through an 8.3 path alias.
func CanonicalRoot(value string) (string, error) {
	raw := filepath.Clean(value)
	if !filepath.IsAbs(raw) {
		return "", errors.New("canonical test root must be absolute")
	}
	info, err := os.Lstat(raw)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.Join(err, errors.New("canonical test root must be a directory"))
	}
	pointer, err := windows.UTF16PtrFromString(raw)
	if err != nil {
		return "", err
	}
	handle, err := windows.CreateFile(pointer, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	attributes, err := windows.GetFileAttributes(pointer)
	if err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return "", errors.Join(err, errors.New("canonical test root cannot be a reparse point"))
	}
	buffer := make([]uint16, 512)
	for {
		length, err := windows.GetFinalPathNameByHandle(handle, &buffer[0],
			uint32(len(buffer)), 0)
		if err != nil {
			return "", err
		}
		if int(length) < len(buffer) {
			resolved := filepath.Clean(strings.TrimPrefix(
				windows.UTF16ToString(buffer[:length]), `\\?\`))
			if !filepath.IsAbs(resolved) {
				return "", errors.New("canonical test root is not local and absolute")
			}
			return resolved, nil
		}
		buffer = make([]uint16, int(length)+1)
	}
}

func prepareSystemDrive() (func() error, bool, error) {
	windowsDirectory, err := windows.GetWindowsDirectory()
	if err != nil {
		return nil, false, err
	}
	volume := filepath.VolumeName(windowsDirectory)
	if len(volume) != 2 || volume[1] != ':' {
		return nil, false, errors.New("resolve Windows system drive")
	}
	return prepareSystemDrivePath(volume + `\`)
}

func prepareSystemDrivePath(root string) (func() error, bool, error) {
	descriptor, err := windows.GetNamedSecurityInfo(root, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION)
	if err != nil || descriptor == nil {
		return nil, false, errors.Join(err, errors.New("read system-drive DACL"))
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return nil, false, errors.Join(err, errors.New("resolve system-drive DACL"))
	}
	allPackagesSID, restrictedPackagesSID, err := applicationPackageSIDs()
	if err != nil {
		return nil, false, err
	}
	if systemDriveDACLReady(dacl, allPackagesSID, restrictedPackagesSID) {
		return func() error { return nil }, false, nil
	}

	pointer, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return nil, false, err
	}
	// MAXIMUM_ALLOWED makes SetSecurityInfo suppress child ACL propagation.
	// Some Windows Server 2025 hosted runners keep a conflicting root handle;
	// only those disposable GitHub-hosted test machines use the persistent,
	// non-propagating fallback below.
	handle, err := windows.CreateFile(pointer, windows.MAXIMUM_ALLOWED,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
			!githubHostedLocalSandboxTest() {
			return nil, false, err
		}
		if err := persistHostedSystemDriveDACL(root, descriptor, dacl,
			allPackagesSID, restrictedPackagesSID); err != nil {
			return nil, false, err
		}
		return func() error { return nil }, true, nil
	}
	fail := func(cause error) (func() error, bool, error) {
		return nil, false, errors.Join(cause, windows.CloseHandle(handle))
	}
	descriptor, err = windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION)
	if err != nil || descriptor == nil {
		return fail(errors.Join(err, errors.New("read system-drive DACL")))
	}
	dacl, _, err = descriptor.DACL()
	if err != nil || dacl == nil {
		return fail(errors.Join(err, errors.New("resolve system-drive DACL")))
	}
	if systemDriveDACLReady(dacl, allPackagesSID, restrictedPackagesSID) {
		return func() error { return nil }, false, windows.CloseHandle(handle)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return fail(err)
	}
	originalSDDL := descriptor.String()
	if originalSDDL == "" {
		return fail(errors.New("serialize system-drive DACL"))
	}
	entries := []windows.EXPLICIT_ACCESS{
		explicitAccess(allPackagesSID, systemDriveMetadataAccess),
		explicitAccess(restrictedPackagesSID, systemDriveMetadataAccess),
	}
	preparedDACL, err := windows.ACLFromEntries(entries, dacl)
	if err != nil {
		return fail(err)
	}
	information := daclSecurityInformation(control)
	if err := windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, information,
		nil, nil, preparedDACL, nil); err != nil {
		return fail(err)
	}
	restore := func() error {
		original, err := windows.SecurityDescriptorFromString(originalSDDL)
		if err != nil {
			return errors.Join(err, windows.CloseHandle(handle))
		}
		originalDACL, _, err := original.DACL()
		if err != nil || originalDACL == nil {
			return errors.Join(err, errors.New("restore system-drive DACL"),
				windows.CloseHandle(handle))
		}
		return errors.Join(windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT,
			information, nil, nil, originalDACL, nil), windows.CloseHandle(handle))
	}
	runtime.KeepAlive(allPackagesSID)
	runtime.KeepAlive(restrictedPackagesSID)
	return restore, true, nil
}

func githubHostedLocalSandboxTest() bool {
	return os.Getenv(prepareSystemDriveEnv) == "1" &&
		os.Getenv("GITHUB_ACTIONS") == "true" &&
		os.Getenv("RUNNER_ENVIRONMENT") == "github-hosted"
}

func persistHostedSystemDriveDACL(root string, descriptor *windows.SECURITY_DESCRIPTOR,
	dacl *windows.ACL, allPackagesSID, restrictedPackagesSID *windows.SID,
) error {
	if descriptor == nil || dacl == nil || allPackagesSID == nil ||
		restrictedPackagesSID == nil {
		return errors.New("invalid system-drive host fixture")
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return err
	}
	preparedDACL, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		explicitAccess(allPackagesSID, systemDriveMetadataAccess),
		explicitAccess(restrictedPackagesSID, systemDriveMetadataAccess),
	}, dacl)
	if err != nil {
		return err
	}
	return setFileDACL(root, daclSecurityInformation(control), control, preparedDACL)
}

func setFileDACL(pathValue string, information windows.SECURITY_INFORMATION,
	control windows.SECURITY_DESCRIPTOR_CONTROL, dacl *windows.ACL,
) error {
	pointer, err := windows.UTF16PtrFromString(pathValue)
	if err != nil {
		return err
	}
	descriptor, err := windows.NewSecurityDescriptor()
	if err != nil {
		return err
	}
	if err := descriptor.SetDACL(dacl, true, false); err != nil {
		return err
	}
	inheritanceControl := windows.SECURITY_DESCRIPTOR_CONTROL(
		windows.SE_DACL_AUTO_INHERIT_REQ | windows.SE_DACL_AUTO_INHERITED |
			windows.SE_DACL_PROTECTED)
	if err := descriptor.SetControl(inheritanceControl,
		control&inheritanceControl); err != nil {
		return err
	}
	selfRelative, err := descriptor.ToSelfRelative()
	if err != nil {
		return err
	}
	// SetFileSecurity applies a directory DACL without propagating it to
	// existing children. This legacy API is confined to a disposable hosted
	// runner because it does not preserve AUTO_INHERITED on the target itself.
	result, _, callErr := setFileSecurityWProc.Call(uintptr(unsafe.Pointer(pointer)),
		uintptr(information), uintptr(unsafe.Pointer(selfRelative)))
	runtime.KeepAlive(pointer)
	runtime.KeepAlive(descriptor)
	runtime.KeepAlive(selfRelative)
	runtime.KeepAlive(dacl)
	if result != 0 {
		return nil
	}
	if callErr == windows.ERROR_SUCCESS {
		return windows.ERROR_GEN_FAILURE
	}
	return callErr
}

type nullDeviceSecurity struct {
	dacl      *windows.ACL
	label     *windows.ACL
	daclSDDL  string
	labelSDDL string
}

func prepareNullDevice() (func() error, bool, error) {
	pointer, err := windows.UTF16PtrFromString(`\\.\NUL`)
	if err != nil {
		return nil, false, err
	}
	allPackagesSID, restrictedPackagesSID, err := applicationPackageSIDs()
	if err != nil {
		return nil, false, err
	}
	open := func(access uint32) (windows.Handle, error) {
		return windows.CreateFile(pointer, access,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	}

	handle, err := open(windows.GENERIC_READ | windows.READ_CONTROL)
	if err != nil {
		return nil, false, err
	}
	security, err := readNullDeviceSecurity(handle)
	closeErr := windows.CloseHandle(handle)
	if err != nil {
		return nil, false, errors.Join(err, closeErr)
	}
	if nullDeviceSecurityReady(security, allPackagesSID, restrictedPackagesSID) {
		return func() error { return nil }, false, closeErr
	}
	if closeErr != nil {
		return nil, false, closeErr
	}

	writeAccess := uint32(windows.GENERIC_READ | windows.READ_CONTROL)
	if !nullDeviceDACLReady(security.dacl, allPackagesSID, restrictedPackagesSID) {
		writeAccess |= windows.WRITE_DAC
	}
	if !nullDeviceLabelReady(security.label) {
		writeAccess |= windows.WRITE_OWNER
	}
	handle, err = open(writeAccess)
	if err != nil {
		return nil, false, err
	}
	security, err = readNullDeviceSecurity(handle)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return nil, false, err
	}
	if nullDeviceSecurityReady(security, allPackagesSID, restrictedPackagesSID) {
		return func() error { return nil }, false, windows.CloseHandle(handle)
	}

	daclChanged := !nullDeviceDACLReady(security.dacl, allPackagesSID,
		restrictedPackagesSID)
	labelChanged := !nullDeviceLabelReady(security.label)
	rollback := func() error {
		return errors.Join(
			restoreNullDeviceDACL(handle, security.daclSDDL, daclChanged),
			restoreNullDeviceLabel(handle, security.labelSDDL, labelChanged),
		)
	}
	if daclChanged {
		entries := []windows.EXPLICIT_ACCESS{
			explicitAccess(allPackagesSID, windows.GENERIC_READ|windows.GENERIC_WRITE|
				windows.GENERIC_EXECUTE),
			explicitAccess(restrictedPackagesSID, windows.GENERIC_READ|windows.GENERIC_WRITE|
				windows.GENERIC_EXECUTE),
		}
		preparedDACL, prepareErr := windows.ACLFromEntries(entries, security.dacl)
		if prepareErr != nil {
			_ = windows.CloseHandle(handle)
			return nil, false, prepareErr
		}
		if err := windows.SetSecurityInfo(handle, windows.SE_KERNEL_OBJECT,
			windows.DACL_SECURITY_INFORMATION, nil, nil, preparedDACL, nil); err != nil {
			_ = windows.CloseHandle(handle)
			return nil, false, err
		}
	}
	if labelChanged {
		labelDescriptor, labelErr := windows.SecurityDescriptorFromString("S:(ML;;NW;;;LW)")
		if labelErr != nil {
			rollbackErr := rollback()
			closeErr := windows.CloseHandle(handle)
			return nil, false, errors.Join(labelErr, rollbackErr, closeErr)
		}
		label, _, labelErr := labelDescriptor.SACL()
		if labelErr != nil || label == nil {
			rollbackErr := rollback()
			closeErr := windows.CloseHandle(handle)
			return nil, false, errors.Join(labelErr, errors.New("prepare null-device label"),
				rollbackErr, closeErr)
		}
		if labelErr := windows.SetSecurityInfo(handle, windows.SE_KERNEL_OBJECT,
			windows.LABEL_SECURITY_INFORMATION, nil, nil, nil, label); labelErr != nil {
			rollbackErr := rollback()
			closeErr := windows.CloseHandle(handle)
			return nil, false, errors.Join(labelErr, rollbackErr, closeErr)
		}
	}
	restore := func() error {
		restoreErr := rollback()
		closeErr := windows.CloseHandle(handle)
		return errors.Join(restoreErr, closeErr)
	}
	runtime.KeepAlive(allPackagesSID)
	runtime.KeepAlive(restrictedPackagesSID)
	return restore, true, nil
}

func readNullDeviceSecurity(handle windows.Handle) (nullDeviceSecurity, error) {
	var value nullDeviceSecurity
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_KERNEL_OBJECT,
		windows.DACL_SECURITY_INFORMATION)
	if err != nil || descriptor == nil {
		return value, errors.Join(err, errors.New("read null-device DACL"))
	}
	value.dacl, _, err = descriptor.DACL()
	if err != nil || value.dacl == nil {
		return value, errors.Join(err, errors.New("resolve null-device DACL"))
	}
	value.daclSDDL = descriptor.String()
	if value.daclSDDL == "" {
		return value, errors.New("serialize null-device DACL")
	}
	labelDescriptor, labelErr := windows.GetSecurityInfo(handle, windows.SE_KERNEL_OBJECT,
		windows.LABEL_SECURITY_INFORMATION)
	if errors.Is(labelErr, windows.ERROR_OBJECT_NOT_FOUND) {
		return value, nil
	}
	if labelErr != nil || labelDescriptor == nil {
		return value, errors.Join(labelErr, errors.New("read null-device label"))
	}
	value.label, _, err = labelDescriptor.SACL()
	if err != nil {
		return value, err
	}
	value.labelSDDL = labelDescriptor.String()
	return value, nil
}

func restoreNullDeviceDACL(handle windows.Handle, sddl string, changed bool) error {
	if !changed {
		return nil
	}
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return errors.Join(err, errors.New("restore null-device DACL"))
	}
	return windows.SetSecurityInfo(handle, windows.SE_KERNEL_OBJECT,
		windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

func restoreNullDeviceLabel(handle windows.Handle, sddl string, changed bool) error {
	if !changed {
		return nil
	}
	if sddl == "" {
		sddl = "S:"
	}
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	label, _, err := descriptor.SACL()
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(handle, windows.SE_KERNEL_OBJECT,
		windows.LABEL_SECURITY_INFORMATION, nil, nil, nil, label)
}

func applicationPackageSIDs() (*windows.SID, *windows.SID, error) {
	allPackagesSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAnyPackageSid)
	if err != nil {
		return nil, nil, err
	}
	restrictedPackagesSID, err := windows.StringToSid("S-1-15-2-2")
	if err != nil {
		return nil, nil, err
	}
	return allPackagesSID, restrictedPackagesSID, nil
}

func explicitAccess(sid *windows.SID, access windows.ACCESS_MASK) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{AccessPermissions: access,
		AccessMode:  windows.GRANT_ACCESS,
		Inheritance: windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(sid)}}
}

func daclSecurityInformation(control windows.SECURITY_DESCRIPTOR_CONTROL) windows.SECURITY_INFORMATION {
	if control&windows.SE_DACL_PROTECTED != 0 {
		return windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
	}
	return windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION
}

func systemDriveDACLReady(dacl *windows.ACL, allPackagesSID,
	restrictedPackagesSID *windows.SID,
) bool {
	return daclGrantsAccess(dacl, allPackagesSID, systemDriveMetadataAccess) &&
		daclGrantsAccess(dacl, restrictedPackagesSID, systemDriveMetadataAccess)
}

func nullDeviceSecurityReady(security nullDeviceSecurity, allPackagesSID,
	restrictedPackagesSID *windows.SID,
) bool {
	return nullDeviceDACLReady(security.dacl, allPackagesSID, restrictedPackagesSID) &&
		nullDeviceLabelReady(security.label)
}

func nullDeviceDACLReady(dacl *windows.ACL, allPackagesSID,
	restrictedPackagesSID *windows.SID,
) bool {
	required := windows.ACCESS_MASK(windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE |
		windows.FILE_GENERIC_EXECUTE)
	return daclGrantsAccess(dacl, allPackagesSID, required) &&
		daclGrantsAccess(dacl, restrictedPackagesSID, required)
}

func nullDeviceLabelReady(sacl *windows.ACL) bool {
	if sacl == nil {
		return false
	}
	lowSID, err := windows.CreateWellKnownSid(windows.WinLowLabelSid)
	if err != nil {
		return false
	}
	for index := uint32(0); index < uint32(sacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(sacl, index, &ace); err != nil || ace == nil ||
			ace.Header.AceType != systemMandatoryLabelACEType ||
			ace.Mask&systemMandatoryLabelNoWriteUp == 0 {
			continue
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if aceSID.IsValid() && aceSID.Equals(lowSID) {
			return true
		}
	}
	return false
}

func daclGrantsRestrictedPackages(dacl *windows.ACL, sid *windows.SID) bool {
	required := windows.ACCESS_MASK(windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE |
		windows.FILE_GENERIC_EXECUTE)
	return daclGrantsAccess(dacl, sid, required)
}

func daclGrantsAccess(dacl *windows.ACL, sid *windows.SID,
	required windows.ACCESS_MASK,
) bool {
	if dacl == nil || sid == nil || !sid.IsValid() {
		return false
	}
	var granted windows.ACCESS_MASK
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil || ace == nil ||
			ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if aceSID.IsValid() && aceSID.Equals(sid) {
			granted |= nullDeviceAccessMask(ace.Mask)
		}
	}
	return granted&required == required
}

func nullDeviceAccessMask(mask windows.ACCESS_MASK) windows.ACCESS_MASK {
	if mask&windows.GENERIC_READ != 0 {
		mask = mask&^windows.GENERIC_READ | windows.FILE_GENERIC_READ
	}
	if mask&windows.GENERIC_WRITE != 0 {
		mask = mask&^windows.GENERIC_WRITE | windows.FILE_GENERIC_WRITE
	}
	if mask&windows.GENERIC_EXECUTE != 0 {
		mask = mask&^windows.GENERIC_EXECUTE | windows.FILE_GENERIC_EXECUTE
	}
	return mask
}

func acquireHostMutex() (func() error, error) {
	// Windows mutex ownership belongs to an OS thread, not a Go goroutine.
	// TestMain invokes both this function and its returned release closure.
	runtime.LockOSThread()
	pointer, err := windows.UTF16PtrFromString(localSandboxHostMutexName)
	if err != nil {
		runtime.UnlockOSThread()
		return nil, err
	}
	handle, err := windows.CreateMutex(nil, false, pointer)
	// An existing named mutex still returns a valid handle to wait on.
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		runtime.UnlockOSThread()
		return nil, err
	}
	status, err := windows.WaitForSingleObject(handle, 10*60*1000)
	if err != nil || (status != windows.WAIT_OBJECT_0 && status != windows.WAIT_ABANDONED) {
		_ = windows.CloseHandle(handle)
		runtime.UnlockOSThread()
		return nil, errors.Join(err, fmt.Errorf("wait for Local Sandbox host-test mutex: %d",
			status))
	}
	return func() error {
		releaseErr := errors.Join(windows.ReleaseMutex(handle), windows.CloseHandle(handle))
		runtime.UnlockOSThread()
		return releaseErr
	}, nil
}

func runRestores(restores ...func() error) error {
	var result error
	for _, restore := range restores {
		if restore != nil {
			result = errors.Join(result, restore())
		}
	}
	return result
}
