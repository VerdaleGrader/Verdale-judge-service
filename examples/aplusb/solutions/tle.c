#include <stdio.h>

int main(void) {
    volatile unsigned long long spin = 0;
    for (;;) spin++;
}
