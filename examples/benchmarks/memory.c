// Memory bandwidth: walks a 96 MB array with a stride, many times.
#include <stdio.h>
#include <stdlib.h>
int main(void) {
    size_t size = 96u << 20;
    unsigned char *data = malloc(size);
    for (size_t index = 0; index < size; index++) data[index] = (unsigned char)index;
    unsigned long sum = 0;
    for (int pass = 0; pass < 60; pass++)
        for (size_t index = 0; index < size; index += 64) sum += data[(index * 7) % size];
    printf("%lu\n", sum);
}
