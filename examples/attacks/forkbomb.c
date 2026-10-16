// Fork bomb: every process forks forever. The sandbox allows one process.
#include <unistd.h>
int main(void) {
    for (;;) fork();
}
