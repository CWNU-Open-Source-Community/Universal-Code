#include <errno.h>
#include <libproc.h>
#include <stdio.h>
#include <string.h>
#include <sys/proc.h>
#include <sys/sysctl.h>

static int injected_count;
static int injected_errno;
static int args_error;
static size_t args_size;
static int identity_error;
static int zombie;
static unsigned start_microseconds;
static unsigned uid;
static int mock_group(pid_t group, void *pids, int bytes) {
  if (injected_errno) errno = injected_errno;
  return injected_count;
}
static int mock_children(pid_t pid, void *pids, int bytes) {
  if (injected_errno) errno = injected_errno;
  return injected_count;
}
static int mock_all(void *pids, int bytes) {
  if (injected_errno) errno = injected_errno;
  return injected_count;
}
static int mock_pidinfo(int pid, int flavor, uint64_t arg, void *output, int bytes) {
  if (identity_error) {
    errno = identity_error;
    return 0;
  }
  struct proc_bsdinfo sample = {
    .pbi_status = zombie ? SZOMB : 0, .pbi_uid = uid, .pbi_start_tvusec = start_microseconds
  };
  memcpy(output, &sample, sizeof(sample));
  return sizeof(sample);
}
static int mock_sysctl(int *mib, unsigned length, void *output, size_t *size, void *input, size_t input_size) {
  if (args_error) {
    errno = args_error;
    return -1;
  }
  *size = args_size;
  return 0;
}
#define proc_listpgrppids mock_group
#define proc_listchildpids mock_children
#define proc_listallpids mock_all
#define proc_pidinfo mock_pidinfo
#define sysctl mock_sysctl
#include "chrome_process_observation_darwin.h"

int main(void) {
  const int cases[][3] = {{0, EACCES, -EACCES}, {0, ENOMEM, -ENOMEM},
    {-1, EPERM, -EPERM}, {-1, 0, -EIO}, {0, 0, 0}, {3, 0, 3}, {8, 0, -ENOSPC}};
  int pids[8];
  for (unsigned i = 0; i < sizeof(cases) / sizeof(cases[0]); i++) {
    injected_count = cases[i][0]; injected_errno = cases[i][1];
    // A stale errno from an earlier observation must not poison success.
    errno = EBUSY;
    int group = uc_chrome_group(123, pids, 8);
    errno = EBUSY;
    int children = uc_chrome_children(123, pids, 8);
    errno = EBUSY;
    int all = uc_chrome_all_processes(pids, 8);
    if (group != cases[i][2] || children != cases[i][2] || all != cases[i][2]) {
      fprintf(stderr, "case %u group=%d children=%d all=%d expected=%d\n", i, group, children, all, cases[i][2]);
      return 1;
    }
  }
  const struct {
    int args_error, args_size, identity_error, zombie, start_microseconds, uid, want;
  } args_cases[] = {
    {0, 32, 0, 0, 10, 501, 1},
    {EINVAL, 0, ESRCH, 0, 0, 0, 0},
    {EIO, 0, 0, 1, 10, 501, 0},
    {EACCES, 0, 0, 0, 10, 501, -EACCES},
    {0, 32, 0, 0, 11, 501, 0},
    {0, 32, 0, 0, 10, 502, -EACCES},
    {0, 32, EPERM, 0, 0, 0, -EPERM},
    {0, 0, 0, 0, 10, 501, -EIO},
  };
  uc_proc_sample before = {.pid = 123, .uid = 501, .usec = 10};
  char args[32];
  for (unsigned i = 0; i < sizeof(args_cases) / sizeof(args_cases[0]); i++) {
    args_error = args_cases[i].args_error;
    args_size = args_cases[i].args_size;
    identity_error = args_cases[i].identity_error;
    zombie = args_cases[i].zombie;
    start_microseconds = args_cases[i].start_microseconds;
    uid = args_cases[i].uid;
    size_t size = sizeof(args);
    int result = uc_chrome_crashpad_args(&before, args, &size);
    if (result != args_cases[i].want) {
      fprintf(stderr, "crash handler case %u returned %d, expected %d\n", i, result, args_cases[i].want);
      return 1;
    }
  }
  puts("native C PID enumeration: failures preserved, empty success distinguished");
  puts("native C crash handler observation: exit races and live permission errors distinguished");
  return 0;
}
