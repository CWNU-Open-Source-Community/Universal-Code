#ifndef UC_CHROME_PROCESS_OBSERVATION_DARWIN_H
#define UC_CHROME_PROCESS_OBSERVATION_DARWIN_H

#include <errno.h>
#include <libproc.h>

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
