// Pure CPU: a chain of dependent integer operations, so nothing else matters.
#include <stdio.h>
int main(void) {
    unsigned long value = 88172645463325252UL;
    for (long step = 0; step < 400000000L; step++) {
        value ^= value << 13;
        value ^= value >> 7;
        value ^= value << 17;
    }
    printf("%lu\n", value);
}
