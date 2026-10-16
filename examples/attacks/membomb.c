// Memory bomb: allocates and touches memory until the control group kills it.
#include <stdlib.h>
#include <string.h>
int main(void) {
    for (;;) {
        char *block = malloc(1 << 20);
        if (block) memset(block, 1, 1 << 20);
    }
}
