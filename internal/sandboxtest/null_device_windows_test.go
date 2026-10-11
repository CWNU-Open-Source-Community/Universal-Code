//go:build windows

package sandboxtest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestAcquireHostMutexOpensExistingObject(t *testing.T) {
	name, err := windows.UTF16PtrFromString(localSandboxHostMutexName)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = windows.CloseHandle(handle) })

	release, err := acquireHostMutex()
	if err != nil {
		t.Fatalf("open the existing host-test mutex: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("release the existing host-test mutex: %v", err)
	}
}

func TestCanonicalRootReturnsStableFinalPath(t *testing.T) {
	root, err := CanonicalRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := CanonicalRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(root, resolved) {
		t.Fatalf("canonical root changed on repeat resolution: %q != %q", root, resolved)
	}
}

func TestPrepareSystemDrivePathRestoresRootWithoutPropagating(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	security := func(pathValue string) string {
		t.Helper()
		descriptor, err := windows.GetNamedSecurityInfo(pathValue, windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION)
		if err != nil || descriptor == nil {
			t.Fatalf("read DACL for %s: %v", filepath.Base(pathValue), err)
		}
		return descriptor.String()
	}
	rootBefore, childBefore := security(root), security(child)
	restore, changed, err := prepareSystemDrivePath(root)
	if err != nil {
		t.Fatal(err)
	}
	restored := false
	t.Cleanup(func() {
		if !restored {
			_ = restore()
		}
	})
	if !changed {
		t.Skip("temporary root already has both AppContainer metadata ACEs")
	}
	descriptor, err := windows.GetNamedSecurityInfo(root, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	allPackagesSID, restrictedPackagesSID, err := applicationPackageSIDs()
	if err != nil {
		t.Fatal(err)
	}
	if !systemDriveDACLReady(dacl, allPackagesSID, restrictedPackagesSID) {
		t.Fatal("temporary root did not receive both metadata ACEs")
	}
	if got := security(child); got != childBefore {
		t.Fatal("root-only metadata ACEs propagated to the child")
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	restored = true
	if got := security(root); got != rootBefore {
		t.Fatal("temporary root DACL was not restored exactly")
	}
	if got := security(child); got != childBefore {
		t.Fatal("child DACL changed during root restoration")
	}
}

func TestPrepareSystemDrivePathHostedFallbackHandlesBusyRootWithoutPropagating(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	security := func(pathValue string) string {
		t.Helper()
		descriptor, err := windows.GetNamedSecurityInfo(pathValue, windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION)
		if err != nil || descriptor == nil {
			t.Fatalf("read DACL for %s: %v", filepath.Base(pathValue), err)
		}
		return descriptor.String()
	}
	childBefore := security(child)
	pointer, err := windows.UTF16PtrFromString(root)
	if err != nil {
		t.Fatal(err)
	}
	busyHandle, err := windows.CreateFile(pointer, windows.GENERIC_READ,
		windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(busyHandle)
	directHandle, directErr := windows.CreateFile(pointer, windows.MAXIMUM_ALLOWED,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if directErr == nil {
		_ = windows.CloseHandle(directHandle)
		t.Fatal("MAXIMUM_ALLOWED unexpectedly bypassed the simulated sharing conflict")
	}
	if !errors.Is(directErr, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("simulate busy root: %v", directErr)
	}
	t.Setenv(prepareSystemDriveEnv, "1")
	t.Setenv("GITHUB_ACTIONS", "false")
	t.Setenv("RUNNER_ENVIRONMENT", "self-hosted")
	if _, _, err := prepareSystemDrivePath(root); !errors.Is(err,
		windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("non-hosted fallback did not fail closed: %v", err)
	}
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("RUNNER_ENVIRONMENT", "github-hosted")
	restore, changed, err := prepareSystemDrivePath(root)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("hosted fallback did not prepare the temporary root")
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.GetNamedSecurityInfo(root, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	allPackagesSID, restrictedPackagesSID, err := applicationPackageSIDs()
	if err != nil {
		t.Fatal(err)
	}
	if !systemDriveDACLReady(dacl, allPackagesSID, restrictedPackagesSID) {
		t.Fatal("hosted fallback did not persist both metadata ACEs")
	}
	if got := security(child); got != childBefore {
		t.Fatal("hosted fallback propagated root-only ACEs to the child")
	}
}

func TestDACLGrantsRestrictedPackagesRequiresCompleteNullDeviceAccess(t *testing.T) {
	sid, err := windows.StringToSid("S-1-15-2-2")
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name string
		sddl string
		want bool
	}{
		{name: "partial", sddl: "D:(A;;GR;;;S-1-15-2-2)", want: false},
		{name: "split complete", sddl: "D:(A;;GR;;;S-1-15-2-2)(A;;GWGX;;;S-1-15-2-2)", want: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			descriptor, err := windows.SecurityDescriptorFromString(testCase.sddl)
			if err != nil {
				t.Fatal(err)
			}
			dacl, _, err := descriptor.DACL()
			if err != nil {
				t.Fatal(err)
			}
			if got := daclGrantsRestrictedPackages(dacl, sid); got != testCase.want {
				t.Fatalf("complete access=%t, want %t", got, testCase.want)
			}
		})
	}
}

func TestNullDeviceSecurityRequiresBothPackageSIDsAndLowLabel(t *testing.T) {
	allPackagesSID, restrictedPackagesSID, err := applicationPackageSIDs()
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name      string
		daclSDDL  string
		labelSDDL string
		want      bool
	}{
		{name: "restricted packages only",
			daclSDDL:  "D:(A;;GRGWGX;;;S-1-15-2-2)",
			labelSDDL: "S:(ML;;NW;;;LW)", want: false},
		{name: "missing low label",
			daclSDDL: "D:(A;;GRGWGX;;;AC)(A;;GRGWGX;;;S-1-15-2-2)", want: false},
		{name: "complete",
			daclSDDL:  "D:(A;;GRGWGX;;;AC)(A;;GRGWGX;;;S-1-15-2-2)",
			labelSDDL: "S:(ML;;NW;;;LW)", want: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			descriptor, err := windows.SecurityDescriptorFromString(testCase.daclSDDL)
			if err != nil {
				t.Fatal(err)
			}
			dacl, _, err := descriptor.DACL()
			if err != nil {
				t.Fatal(err)
			}
			var label *windows.ACL
			if testCase.labelSDDL != "" {
				labelDescriptor, err := windows.SecurityDescriptorFromString(testCase.labelSDDL)
				if err != nil {
					t.Fatal(err)
				}
				label, _, err = labelDescriptor.SACL()
				if err != nil {
					t.Fatal(err)
				}
			}
			security := nullDeviceSecurity{dacl: dacl, label: label}
			if got := nullDeviceSecurityReady(security, allPackagesSID,
				restrictedPackagesSID); got != testCase.want {
				t.Fatalf("security ready=%t, want %t", got, testCase.want)
			}
		})
	}
}

func TestSystemDriveDACLRequiresBothPackageMetadataACEs(t *testing.T) {
	allPackagesSID, restrictedPackagesSID, err := applicationPackageSIDs()
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name string
		sddl string
		want bool
	}{
		{name: "all packages only", sddl: "D:(A;;0x120088;;;AC)", want: false},
		{name: "both", sddl: "D:(A;;0x120088;;;AC)(A;;0x120088;;;S-1-15-2-2)",
			want: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			descriptor, err := windows.SecurityDescriptorFromString(testCase.sddl)
			if err != nil {
				t.Fatal(err)
			}
			dacl, _, err := descriptor.DACL()
			if err != nil {
				t.Fatal(err)
			}
			if got := systemDriveDACLReady(dacl, allPackagesSID,
				restrictedPackagesSID); got != testCase.want {
				t.Fatalf("system-drive DACL ready=%t, want %t", got, testCase.want)
			}
		})
	}
}
