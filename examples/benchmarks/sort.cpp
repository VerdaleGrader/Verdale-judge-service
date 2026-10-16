// CPU and cache: sorts two million numbers.
#include <algorithm>
#include <cstdio>
#include <vector>
int main() {
    std::vector<unsigned> numbers(2000000);
    unsigned seed = 12345;
    for (auto &number : numbers) {
        seed = seed * 1664525u + 1013904223u;
        number = seed;
    }
    std::sort(numbers.begin(), numbers.end());
    std::printf("%u\n", numbers[numbers.size() / 2]);
}
