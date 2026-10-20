#include <errno.h>
#include <libproc.h>
#include <stdio.h>

static int injected_count;
static int injected_errno;
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
#define proc_listpgrppids mock_group
#define proc_listchildpids mock_children
#define proc_listallpids mock_all
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
  puts("native C PID enumeration: failures preserved, empty success distinguished");
  return 0;
}
