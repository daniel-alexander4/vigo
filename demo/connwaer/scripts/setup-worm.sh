#!/bin/sh
# Create test files in the WORM volume, then sleep forever.
# The connwaer-node-1 container mounts this volume read-only at /mnt/worm.

set -e

echo "Setting up WORM test files..."

# Create test files with known content.
echo "WORM test file 1 - do not modify" > /worm/testfile1.txt
echo "WORM test file 2 - immutable record" > /worm/testfile2.txt
date -u > /worm/timestamp.txt

echo "WORM files created. Sleeping."
exec sleep infinity
