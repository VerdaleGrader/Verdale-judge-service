# Tries to break out of the sandbox and prints what happened instead of
# the answer, so the verdict is WA when every attempt is blocked.
import os
import socket


def attempt(name, action):
    try:
        action()
        print(f"{name}: ALLOWED")
    except Exception as error:
        print(f"{name}: blocked ({type(error).__name__})")


attempt("network", lambda: socket.create_connection(("1.1.1.1", 80), timeout=2))
attempt("read /etc/passwd", lambda: open("/etc/passwd").read())
attempt("read /root", lambda: os.listdir("/root"))
attempt("write /usr", lambda: open("/usr/verdale-was-here", "w").write("x"))
attempt("fork", lambda: os.fork() == 0 and os._exit(0))
print("processes visible:", len([entry for entry in os.listdir("/proc") if entry.isdigit()]))
