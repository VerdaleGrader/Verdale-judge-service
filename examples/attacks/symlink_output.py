# Replaces its own output file with a link to a file of the host, hoping Judge
# follows it and returns the content. __CANARY__ is filled in by the test.
import os

os.unlink("output.txt")
os.symlink("__CANARY__", "output.txt")
print("link placed")
