# Thread bomb: threads count against the process limit, so this must fail fast.
import threading
import time

started = 0
try:
    while True:
        threading.Thread(target=time.sleep, args=(60,), daemon=True).start()
        started += 1
except Exception as error:
    print("threads started:", started, type(error).__name__)
