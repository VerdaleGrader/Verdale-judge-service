# Opens files until the system says no, then reports how many it got.
handles = []
try:
    while True:
        handles.append(open("/dev/null"))
except OSError as error:
    print("open files:", len(handles), type(error).__name__)
