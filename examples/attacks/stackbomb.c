// Unbounded recursion: must end in a crash, never take the host with it.
#include <stdio.h>
static int recurse(int depth) {
    volatile char padding[4096];
    padding[0] = (char)depth;
    return recurse(depth + 1) + padding[0];
}
int main(void) { printf("%d\n", recurse(0)); }
