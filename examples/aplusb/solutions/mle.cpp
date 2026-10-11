#include <cstdio>
#include <vector>

int main() {
    std::vector<char> block(256u << 20);  // 256 MB, far above the 64 MB limit
    for (size_t i = 0; i < block.size(); i += 4096) block[i] = 1;
    std::printf("%d\n", block[4096]);
}
