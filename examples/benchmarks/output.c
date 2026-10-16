// Output and formatting: writes about 30 MB of numbers to standard output.
#include <stdio.h>
int main(void) {
    for (int line = 0; line < 3000000; line++) printf("%d\n", line * 7);
}
