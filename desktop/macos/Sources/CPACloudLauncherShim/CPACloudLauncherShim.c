#include "CPACloudLauncherShim.h"

int cpa_cloud_flock(int descriptor, int operation) {
    return flock(descriptor, operation);
}
