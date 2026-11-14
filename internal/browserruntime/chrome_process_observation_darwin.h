#ifndef UC_CHROME_PROCESS_OBSERVATION_DARWIN_H
#define UC_CHROME_PROCESS_OBSERVATION_DARWIN_H

#include <errno.h>
#include <libproc.h>
#include <stddef.h>
#include <stdint.h>
#include <sys/proc.h>
#include <sys/sysctl.h>

typedef struct { int pid, ppid, pgid, uid, zombie; uint64_t sec, usec, rss; } uc_proc_sample;

static int uc_chrome_process_identity(int pid, uc_proc_sample *out) {
  struct proc_bsdinfo bsd;
  errno = 0;
  if (proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &bsd, sizeof(bsd)) != sizeof(bsd)) {
    return errno == ENOENT ? ESRCH : (errno ? errno : EIO);
  }
  *out = (uc_proc_sample){pid, (int)bsd.pbi_ppid, (int)bsd.pbi_pgid, (int)bsd.pbi_uid,
    bsd.pbi_status == SZOMB, bsd.pbi_start_tvsec, bsd.pbi_start_tvusec, 0};
  return 0;
}

// The handler can exit after proc_pidpath but before KERN_PROCARGS2 reads its
// argument buffer. Require identity evidence before dropping that candidate.
// An unreadable process with the same identity still blocks cleanup.
static int uc_chrome_crashpad_args(const uc_proc_sample *before, char *args, size_t *size) {
  int mib[] = {CTL_KERN, KERN_PROCARGS2, before->pid};
  errno = 0;
  int rc = sysctl(mib, 3, args, size, NULL, 0);
  int args_errno = rc ? (errno ? errno : EIO) : (*size <= sizeof(int) ? EIO : 0);
  uc_proc_sample after;
  int status = uc_chrome_process_identity(before->pid, &after);
  if (status == ESRCH || (status == 0 && (after.zombie ||
      before->sec != after.sec || before->usec != after.usec))) return 0;
  if (status) return -status;
  if (before->uid != after.uid) return -EACCES;
  if (args_errno) return -args_errno;
  return 1;
}

// Apple's proc_listpids maps a failed syscall to zero while retaining errno;
// libproc's convenience wrappers then return PID counts rather than byte counts.
// Clear errno before each call so a real empty result is distinguishable from a
// failed observation, and never turn failure into verified empty process scope.
static int uc_chrome_pid_count(int count, int observed_errno, int capacity) {
  if (count < 0 || (count == 0 && observed_errno)) return -(observed_errno ? observed_errno : EIO);
  if (count >= capacity) return -ENOSPC;
  return count;
}
static int uc_chrome_group(int group, int *pids, int capacity) {
  errno = 0;
  int count = proc_listpgrppids(group, pids, capacity * sizeof(int));
  return uc_chrome_pid_count(count, errno, capacity);
}
static int uc_chrome_children(int pid, int *pids, int capacity) {
  errno = 0;
  int count = proc_listchildpids(pid, pids, capacity * sizeof(int));
  return uc_chrome_pid_count(count, errno, capacity);
}
static int uc_chrome_all_processes(int *pids, int capacity) {
  errno = 0;
  int count = proc_listallpids(pids, capacity * sizeof(int));
  return uc_chrome_pid_count(count, errno, capacity);
}

#endif
