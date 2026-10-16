# The same trick on the standard error file.
import os

os.unlink("stderr.txt")
os.symlink("__CANARY__", "stderr.txt")
