//go:build darwin && cgo

package browserruntime

/*
#cgo LDFLAGS: -framework CoreFoundation -framework Security
#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>
#include <libproc.h>
#include <mach/machine.h>
#include <spawn.h>
#include <signal.h>
#include <stdlib.h>
#include <stdio.h>
#include <string.h>
#include <sys/proc.h>
#include <sys/sysctl.h>
#include <sys/wait.h>
#include <unistd.h>
#include <errno.h>
#include <fcntl.h>
#include "chrome_process_observation_darwin.h"

// The publisher policy is fixed. A locally discovered signer is never trusted
// simply because its signature verifies. CDHash binds the admitted native slice.
static const char *uc_chrome_requirement =
  "anchor apple generic and identifier \"com.google.Chrome\" and "
  "certificate 1[field.1.2.840.113635.100.6.2.6] and "
  "certificate leaf[field.1.2.840.113635.100.6.1.13] and "
  "certificate leaf[subject.OU] = \"EQHXZ8M8AV\"";

static OSStatus uc_chrome_req(const char *digest, SecRequirementRef *requirement) {
  char *text = NULL;
  if (digest) asprintf(&text, "%s and cdhash H\"%s\"", uc_chrome_requirement, digest);
  else text = strdup(uc_chrome_requirement);
  if (!text) return errSecAllocate;
  CFStringRef string = CFStringCreateWithCString(NULL, text, kCFStringEncodingUTF8);
  free(text);
  if (!string) return errSecAllocate;
  OSStatus status = SecRequirementCreateWithString(string, kSecCSDefaultFlags, requirement);
  CFRelease(string);
  return status;
}

static OSStatus uc_chrome_cdhash(const char *bundle, char *out, size_t capacity) {
  CFURLRef url = CFURLCreateFromFileSystemRepresentation(NULL,
    (const UInt8 *)bundle, strlen(bundle), true);
  if (!url) return errSecAllocate;
  SecStaticCodeRef code = NULL;
  SecRequirementRef requirement = NULL;
  CFDictionaryRef info = NULL;
  OSStatus status = SecStaticCodeCreateWithPath(url, kSecCSDefaultFlags, &code);
  if (!status) status = uc_chrome_req(NULL, &requirement);
  if (!status) status = SecStaticCodeCheckValidity(code,
    kSecCSCheckAllArchitectures | kSecCSCheckNestedCode | kSecCSStrictValidate, requirement);
  if (!status) status = SecCodeCopySigningInformation(code, kSecCSSigningInformation, &info);
  if (!status) {
    CFTypeRef value = CFDictionaryGetValue(info, kSecCodeInfoUnique);
    if (!value || CFGetTypeID(value) != CFDataGetTypeID()) status = errSecCSInvalidObjectRef;
    else {
      CFDataRef data = (CFDataRef)value;
      CFIndex count = CFDataGetLength(data);
      if (count <= 0 || (size_t)count * 2 + 1 > capacity) status = errSecParam;
      else {
        const UInt8 *bytes = CFDataGetBytePtr(data);
        for (CFIndex i = 0; i < count; i++) snprintf(out + 2*i, 3, "%02x", bytes[i]);
      }
    }
  }
  if (info) CFRelease(info);
  if (requirement) CFRelease(requirement);
  if (code) CFRelease(code);
  CFRelease(url);
  return status;
}

static OSStatus uc_chrome_running_identity(pid_t pid, const char *digest) {
  CFNumberRef number = CFNumberCreate(NULL, kCFNumberIntType, &pid);
  const void *keys[] = { kSecGuestAttributePid };
  const void *values[] = { number };
  CFDictionaryRef attributes = CFDictionaryCreate(NULL, keys, values, 1,
    &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
  SecCodeRef code = NULL;
  SecRequirementRef requirement = NULL;
  OSStatus status = attributes ? SecCodeCopyGuestWithAttributes(NULL, attributes,
    kSecCSDefaultFlags, &code) : errSecAllocate;
  if (!status) status = uc_chrome_req(digest, &requirement);
  if (!status) status = SecCodeCheckValidity(code, kSecCSDefaultFlags, requirement);
  if (requirement) CFRelease(requirement);
  if (code) CFRelease(code);
  if (attributes) CFRelease(attributes);
  if (number) CFRelease(number);
  return status;
}

// Spawn remains suspended until the dynamic code satisfies the admitted hash.
// The caller owns this direct child; no waiter can recycle its PID first.
static int uc_chrome_spawn(const char *executable, const char *profile,
  const char *digest, pid_t *out_pid, int *out_stage) {
  if (getuid() == 0 || geteuid() != getuid()) return EPERM;
  char *data = NULL, *dump = NULL, *home = NULL, *temp = NULL, *log = NULL;
  if (asprintf(&data, "--user-data-dir=%s/Data", profile) < 0 ||
      asprintf(&dump, "BREAKPAD_DUMP_LOCATION=%s/Crashpad", profile) < 0 ||
      asprintf(&home, "HOME=%s/Home", profile) < 0 ||
      asprintf(&temp, "TMPDIR=%s/Temp/", profile) < 0 ||
      asprintf(&log, "%s/browser.stderr.log", profile) < 0) {
    free(data); free(dump); free(home); free(temp); free(log); return ENOMEM;
  }
  char *argv[] = {(char *)executable, "--headless=new", "--remote-debugging-address=127.0.0.1",
    "--remote-debugging-port=0", data, "--no-first-run", "--no-default-browser-check",
    "--disable-extensions", "--disable-sync", "--disable-component-update",
    "--disable-background-networking", "--use-mock-keychain", "--window-size=1280,900", "about:blank", NULL};
  char *env[] = {home, temp, dump, "PATH=/usr/bin:/bin", "LANG=en_US.UTF-8", NULL};
  posix_spawnattr_t attr;
  posix_spawn_file_actions_t files;
  int result = posix_spawnattr_init(&attr);
  if (result) goto done;
  result = posix_spawn_file_actions_init(&files);
  if (result) { posix_spawnattr_destroy(&attr); goto done; }
  sigset_t empty, defaults;
  sigemptyset(&empty); sigemptyset(&defaults);
  sigaddset(&defaults, SIGTERM); sigaddset(&defaults, SIGINT); sigaddset(&defaults, SIGPIPE);
  *out_stage = 1;
  if (!(result = posix_spawnattr_setpgroup(&attr, 0)) &&
      !(result = posix_spawnattr_setsigmask(&attr, &empty)) &&
      !(result = posix_spawnattr_setsigdefault(&attr, &defaults))) {
    result = posix_spawnattr_setflags(&attr, POSIX_SPAWN_SETPGROUP |
      POSIX_SPAWN_START_SUSPENDED | POSIX_SPAWN_CLOEXEC_DEFAULT |
      POSIX_SPAWN_SETSIGMASK | POSIX_SPAWN_SETSIGDEF);
  }
  if (!result) {
    cpu_type_t architecture =
#if defined(__arm64__)
      CPU_TYPE_ARM64;
#else
      CPU_TYPE_X86_64;
#endif
    size_t copied = 0;
    result = posix_spawnattr_setbinpref_np(&attr, 1, &architecture, &copied);
    if (!result && copied != 1) result = ENOEXEC;
  }
  if (!result) result = posix_spawn_file_actions_addopen(&files, STDIN_FILENO, "/dev/null", 0, 0);
  if (!result) result = posix_spawn_file_actions_addopen(&files, STDOUT_FILENO, "/dev/null", 1, 0);
  if (!result) result = posix_spawn_file_actions_addopen(&files, STDERR_FILENO, log, O_WRONLY | O_CREAT | O_TRUNC, 0600);
  if (!result) result = posix_spawn_file_actions_addchdir_np(&files, "/Applications/Google Chrome.app/Contents/MacOS");
  pid_t pid = 0;
  if (!result) result = posix_spawn(&pid, executable, &files, &attr, argv, env);
  posix_spawn_file_actions_destroy(&files);
  posix_spawnattr_destroy(&attr);
  if (!result) {
    *out_stage = 2;
    result = (int)uc_chrome_running_identity(pid, digest);
    if (!result && (getpgid(pid) != pid)) result = EPERM;
    char actual[PROC_PIDPATHINFO_MAXSIZE];
    if (!result && (proc_pidpath(pid, actual, sizeof(actual)) <= 0 || strcmp(actual, executable))) result = ESTALE;
    if (result) {
      kill(pid, SIGKILL);
      pid_t waited;
      do { waited = waitpid(pid, NULL, 0); } while (waited < 0 && errno == EINTR);
      // Preserve the owned PID if rejection cleanup itself failed. The caller
      // must retain its profile rather than issue a no-resource receipt.
      if (waited < 0) { *out_pid = pid; *out_stage = 3; }
    }
    else { *out_pid = pid; *out_stage = 0; }
  }
done:
  free(data); free(dump); free(home); free(temp); free(log);
  return result;
}

typedef struct { int pid, ppid, pgid, uid, zombie; uint64_t sec, usec, rss; } uc_proc_sample;
static int uc_chrome_sample(int pid, uc_proc_sample *out) {
  struct proc_bsdinfo bsd;
  errno = 0;
  if (proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &bsd, sizeof(bsd)) != sizeof(bsd)) {
    return errno == ENOENT ? ESRCH : (errno ? errno : EIO);
  }
  struct proc_taskinfo task;
  memset(&task, 0, sizeof(task));
  errno = 0;
  if (bsd.pbi_status != SZOMB && proc_pidinfo(pid, PROC_PIDTASKINFO, 0, &task, sizeof(task)) != sizeof(task)) {
    return errno == ENOENT ? ESRCH : (errno ? errno : EIO);
  }
  *out = (uc_proc_sample){pid, (int)bsd.pbi_ppid, (int)bsd.pbi_pgid, (int)bsd.pbi_uid,
    bsd.pbi_status == SZOMB, bsd.pbi_start_tvsec, bsd.pbi_start_tvusec, task.pti_resident_size};
  return 0;
}

// Parse only argc arguments for a matching executable; never export a process'
// environment or unrelated command line. Crashpad deliberately reparents itself.
static int uc_chrome_crashpad(const char *profile, int *pids, int capacity) {
  int all[8192];
  int total = uc_chrome_all_processes(all, 8192);
  if (total < 0) return total;
  char *expected = NULL;
  if (asprintf(&expected, "--database=%s/Crashpad", profile) < 0) return -ENOMEM;
  int found = 0;
  for (int i = 0; i < total; i++) {
    uc_proc_sample sample;
    if (uc_chrome_sample(all[i], &sample) || sample.uid != getuid() || sample.zombie) continue;
    char path[PROC_PIDPATHINFO_MAXSIZE];
    if (proc_pidpath(all[i], path, sizeof(path)) <= 0) continue;
    const char *prefix = "/Applications/Google Chrome.app/Contents/Frameworks/";
    const char *suffix = "/Helpers/chrome_crashpad_handler";
    size_t length = strlen(path), tail = strlen(suffix);
    if (strncmp(path, prefix, strlen(prefix)) || length < tail || strcmp(path + length - tail, suffix)) continue;
    size_t size = 1024 * 1024;
    char *args = malloc(size);
    if (!args) { free(expected); return -ENOMEM; }
    int mib[] = {CTL_KERN, KERN_PROCARGS2, all[i]};
    if (sysctl(mib, 3, args, &size, NULL, 0) || size <= sizeof(int)) { free(args); free(expected); return -EACCES; }
    int argc;
    memcpy(&argc, args, sizeof(argc));
    char *at = args + sizeof(int), *end = args + size;
    size_t skip = strnlen(at, end - at);
    at += skip;
    while (at < end && !*at) at++;
    int matched = 0;
    for (int arg = 0; arg < argc && arg < 4096 && at < end; arg++) {
      size_t len = strnlen(at, end - at);
      if (len == (size_t)(end - at)) break;
      if (!strcmp(at, expected)) matched = 1;
      at += len + 1;
    }
    free(args);
    if (matched) {
      if (found == capacity) { free(expected); return -ENOSPC; }
      pids[found++] = all[i];
    }
  }
  free(expected);
  return found;
}
static int uc_chrome_exited(int pid) {
  siginfo_t info;
  memset(&info, 0, sizeof(info));
  if (waitid(P_PID, pid, &info, WEXITED | WNOHANG | WNOWAIT)) return -errno;
  return info.si_pid == pid;
}
static int uc_chrome_wait(int pid, int *code) {
  int status;
  pid_t result;
  do { result = waitpid(pid, &status, 0); } while (result < 0 && errno == EINTR);
  if (result < 0) return errno;
  *code = WIFEXITED(status) ? WEXITSTATUS(status) : 128 + WTERMSIG(status);
  return 0;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

type darwinChromeStartError struct{ Stage, Status int }

func (e *darwinChromeStartError) Error() string {
	return fmt.Sprintf("Chrome native start stage %d: status %d", e.Stage, e.Status)
}
func darwinChromeRequirementRejected(err error) bool {
	var failure *darwinChromeStartError
	return errors.As(err, &failure) && failure.Stage == 2 && failure.Status == int(C.errSecCSReqFailed)
}

func darwinChromeCDHash(bundle string) (string, error) {
	path := C.CString(bundle)
	defer C.free(unsafe.Pointer(path))
	var out [129]C.char
	if status := C.uc_chrome_cdhash(path, &out[0], C.size_t(len(out))); status != 0 {
		return "", fmt.Errorf("Chrome static signature: OSStatus %d", int(status))
	}
	return C.GoString(&out[0]), nil
}

func darwinChromeSpawn(executable, profile, digest string) (int, error) {
	pid, err := darwinChromeSpawnSuspended(executable, profile, digest)
	if err != nil {
		return 0, err
	}
	if err = syscall.Kill(pid, syscall.SIGCONT); err != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		_, _ = darwinChromeWait(pid)
		return 0, err
	}
	return pid, nil
}

func darwinChromeSpawnSuspended(executable, profile, digest string) (int, error) {
	path, directory, hash := C.CString(executable), C.CString(profile), C.CString(digest)
	defer C.free(unsafe.Pointer(path))
	defer C.free(unsafe.Pointer(directory))
	defer C.free(unsafe.Pointer(hash))
	var pid, stage C.int
	status := C.uc_chrome_spawn(path, directory, hash, &pid, &stage)
	if status != 0 {
		return int(pid), &darwinChromeStartError{int(stage), int(status)}
	}
	return int(pid), nil
}

type darwinChromeProcessSample struct {
	PID, ParentPID, Group, UID           int
	Zombie                               bool
	StartSeconds, StartMicroseconds, RSS uint64
}

func darwinChromeSample(pid int) (darwinChromeProcessSample, error) {
	var sample C.uc_proc_sample
	if status := C.uc_chrome_sample(C.int(pid), &sample); status != 0 {
		return darwinChromeProcessSample{}, syscall.Errno(status)
	}
	return darwinChromeProcessSample{int(sample.pid), int(sample.ppid), int(sample.pgid), int(sample.uid), sample.zombie != 0,
		uint64(sample.sec), uint64(sample.usec), uint64(sample.rss)}, nil
}

func darwinChromeGroup(group int) ([]int, error) {
	var pids [512]C.int
	count := int(C.uc_chrome_group(C.int(group), &pids[0], C.int(len(pids))))
	if count < 0 {
		return nil, syscall.Errno(-count)
	}
	result := make([]int, 0, count)
	for _, pid := range pids[:count] {
		if pid > 0 {
			result = append(result, int(pid))
		}
	}
	return result, nil
}

func darwinChromeCrashpad(profile string) ([]int, error) {
	path := C.CString(profile)
	defer C.free(unsafe.Pointer(path))
	var pids [32]C.int
	count := int(C.uc_chrome_crashpad(path, &pids[0], C.int(len(pids))))
	if count < 0 {
		return nil, syscall.Errno(-count)
	}
	result := make([]int, count)
	for index, pid := range pids[:count] {
		result[index] = int(pid)
	}
	return result, nil
}

func darwinChromeChildren(pid int) ([]int, error) {
	var pids [512]C.int
	count := int(C.uc_chrome_children(C.int(pid), &pids[0], C.int(len(pids))))
	if count < 0 {
		return nil, syscall.Errno(-count)
	}
	result := make([]int, 0, count)
	for _, child := range pids[:count] {
		if child > 0 {
			result = append(result, int(child))
		}
	}
	return result, nil
}

func darwinChromeExited(pid int) (bool, error) {
	result := int(C.uc_chrome_exited(C.int(pid)))
	if result < 0 {
		return false, syscall.Errno(-result)
	}
	return result == 1, nil
}

func darwinChromeWait(pid int) (int, error) {
	var code C.int
	if status := C.uc_chrome_wait(C.int(pid), &code); status != 0 {
		return 0, syscall.Errno(status)
	}
	return int(code), nil
}
