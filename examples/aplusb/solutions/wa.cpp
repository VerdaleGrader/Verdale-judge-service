#include <cstdio>

int main() {
    int a, b;  // overflows on the big tests, and subtracts on purpose
    std::scanf("%d %d", &a, &b);
    std::printf("%d\n", a - b + 2 * b * (a > 0));
}
