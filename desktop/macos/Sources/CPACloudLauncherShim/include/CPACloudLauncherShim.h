#ifndef CPA_CLOUD_LAUNCHER_SHIM_H
#define CPA_CLOUD_LAUNCHER_SHIM_H

// sys/file.h declares the BSD flock(2) function. Calling it through this
// uniquely named shim avoids Swift's Darwin.flock function/struct collision.
#include <sys/file.h>

int cpa_cloud_flock(int descriptor, int operation);

#endif
