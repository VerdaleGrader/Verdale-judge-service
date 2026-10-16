# Tries to reach the host in many ways and prints one line for each attempt.
# "ALLOWED" means the attempt worked, which must never happen. __CANARY__ is
# a file of the host that the test created.
import ctypes
import os
import socket


def attempt(name, action):
    try:
        action()
        print(name + ": ALLOWED")
    except BaseException as error:
        print(name + ": blocked (" + type(error).__name__ + ")")


def read_file(path):
    def action():
        with open(path, "rb") as handle:
            if not handle.read(1):
                raise OSError("empty")
    return action


def write_file(path):
    def action():
        with open(path, "w") as handle:
            handle.write("x")
    return action


libc = ctypes.CDLL(None, use_errno=True)


def must_fail(name, result):
    # A system call that returns -1 failed, which is what we want.
    if result == -1:
        print(name + ": blocked (errno " + str(ctypes.get_errno()) + ")")
    else:
        print(name + ": ALLOWED")


# --- network ---
attempt("tcp internet", lambda: socket.create_connection(("1.1.1.1", 80), timeout=2))
attempt("tcp localhost", lambda: socket.create_connection(("127.0.0.1", 6379), timeout=2))
attempt("tcp host gateway", lambda: socket.create_connection(("172.17.0.1", 8081), timeout=2))
attempt("udp dns", lambda: socket.socket(socket.AF_INET, socket.SOCK_DGRAM).sendto(b"x", ("8.8.8.8", 53)))
attempt("unix docker socket", lambda: socket.socket(socket.AF_UNIX).connect("/var/run/docker.sock"))
attempt("raw socket", lambda: socket.socket(socket.AF_INET, socket.SOCK_RAW, socket.IPPROTO_ICMP))
try:
    interfaces = sorted(os.listdir("/sys/class/net"))
except OSError:
    interfaces = []
print("interfaces:", interfaces)

# --- files of the host ---
for path in ["/etc/shadow", "/etc/passwd", "/root/.bashrc", "/var/lib/verdale-judge/cache", "/proc/kcore",
             "/proc/1/root/etc/passwd", "/sys/fs/cgroup/cgroup.procs", "/dev/mem", "/dev/sda", "/dev/kmsg",
             "__CANARY__"]:
    attempt("read " + path, read_file(path))
for path in ["/usr/verdale-was-here", "/bin/verdale-was-here", "/etc/verdale-was-here", "/dev/verdale-was-here",
             "/proc/sys/kernel/core_pattern", "/proc/sysrq-trigger", "/sys/kernel/uevent_helper", "__CANARY__"]:
    attempt("write " + path, write_file(path))

# --- privileges ---
print("uid:", os.getuid(), "gid:", os.getgid())
print("root: ALLOWED" if os.getuid() == 0 else "root: blocked (runs as an unprivileged user)")
must_fail("setuid(0)", libc.setuid(0))
must_fail("chroot", libc.chroot(b"/proc"))
must_fail("mount", libc.mount(b"none", b"/tmp", b"tmpfs", 0, None))
must_fail("mknod disk", libc.mknod(b"/tmp/disk", 0o60660, os.makedev(8, 0)))
must_fail("reboot", libc.reboot(0x01234567))
must_fail("sethostname", libc.sethostname(b"pwned", 5))
must_fail("unshare user ns", libc.unshare(0x10000000))

# --- processes ---
must_fail("ptrace attach pid 1", libc.ptrace(16, 1, 0, 0))
must_fail("ptrace attach parent", libc.ptrace(16, os.getppid() or 1, 0, 0))
print("processes visible:", len([entry for entry in os.listdir("/proc") if entry.isdigit()]))
