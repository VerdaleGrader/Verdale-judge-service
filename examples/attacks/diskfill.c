// Fills the disk with many files, each under the per-file size limit, so only
// the time limit and the clean-up stand between it and a full disk.
#include <stdio.h>
#include <string.h>
static char chunk[512 * 1024];
int main(void) {
    memset(chunk, 'x', sizeof chunk);
    for (int index = 0;; index++) {
        char name[64];
        snprintf(name, sizeof name, "/tmp/fill-%d", index);
        FILE *file = fopen(name, "w");
        if (!file) continue;
        for (int block = 0; block < 1; block++) fwrite(chunk, 1, sizeof chunk, file);
        fclose(file);
    }
}
