#!/bin/sh
# Copy example module(s) from example-configs/ into stockpile/ for publishing.
#
# Usage:
#   use-example.sh <module-name>     Copy a single module
#   use-example.sh --all             Copy all example modules
#
# Examples:
#   use-example.sh openssh
#   use-example.sh openssh
#   use-example.sh --all

set -e

EXAMPLES_DIR="${VIGO_EXAMPLES_DIR:-/srv/vigo/stack/examples}"
STOCKPILE_DIR="${VIGO_STOCKPILE_DIR:-/srv/vigo/stack/stockpile}"

usage() {
    echo "Usage: $0 <module-name> | --all"
    echo ""
    echo "Copies example .vgo files from example-configs/ into stockpile/,"
    echo "preserving directory structure and stripping the .example suffix."
    echo ""
    echo "  $0 openssh              Copy modules/security/openssh.vgo.example"
    echo "  $0 openssh              Copy modules/security/openssh.vgo.example"
    echo "  $0 --all                Copy all example modules"
    exit 1
}

copy_example() {
    src="$1"
    rel="${src#${EXAMPLES_DIR}/}"
    # Strip .example suffix
    dest="${STOCKPILE_DIR}/${rel%.example}"

    if [ -f "$dest" ]; then
        echo "  skip  ${rel%.example} (already exists)"
        return
    fi

    mkdir -p "$(dirname "$dest")"
    cp "$src" "$dest"
    echo "  copy  ${rel%.example}"
}

if [ $# -eq 0 ]; then
    usage
fi

if [ "$1" = "--all" ]; then
    echo "Copying all examples to stockpile..."
    count=0
    find "$EXAMPLES_DIR" -name '*.vgo.example' -type f | sort | while IFS= read -r src; do
        copy_example "$src"
        count=$((count + 1))
    done
    echo "Done."
    exit 0
fi

# Find the named module in examples.
name="$1"
found=""
for match in $(find "$EXAMPLES_DIR" -name "${name}.vgo.example" -type f); do
    found="$match"
    break
done

if [ -z "$found" ]; then
    echo "No example found for '${name}'"
    echo ""
    echo "Available examples:"
    find "$EXAMPLES_DIR" -name '*.vgo.example' -type f | sort | while IFS= read -r f; do
        rel="${f#${EXAMPLES_DIR}/}"
        basename="${rel%.vgo.example}"
        echo "  $(basename "$basename")"
    done
    exit 1
fi

echo "Copying example to stockpile..."
copy_example "$found"
echo "Done. Run 'vigocli config publish' to activate."
