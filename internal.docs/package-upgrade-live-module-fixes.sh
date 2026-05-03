#!/usr/bin/env bash
# package-upgrade-live-module-fixes.sh
#
# Apply three fixes to /srv/vigo/stacks/stockpile/modules/package-upgrade.vgo
# (the live, root-owned copy) under the active vigo deployment.
#
# 1. apt-update gains `report_settled: true` so the daily cache refresh
#    no longer reports `Created` and trips drift detection.
# 2. reboot-if-needed `onlyif:` rewritten so the `/var/run/reboot-required`
#    flag-file path actually triggers (was inverted).
# 3. pkgin-upgrade `unless:` switched to a dry-run so the check no longer
#    runs the upgrade twice.
#
# Idempotent: each sed substitution targets exact pre-fix text and will
# silently no-op if the file's already patched. Run with sudo:
#
#   sudo bash internal.docs/package-upgrade-live-module-fixes.sh
#
# After running, follow up with `vigocli config publish` to push to the
# live tree.

set -euo pipefail

TARGET=/srv/vigo/stacks/stockpile/modules/package-upgrade.vgo

if [[ ! -f "$TARGET" ]]; then
    echo "error: $TARGET not found" >&2
    exit 1
fi

# Snapshot the pre-edit file once so a partial failure is recoverable.
BACKUP="${TARGET}.bak.$(date +%Y%m%d-%H%M%S)"
cp -p "$TARGET" "$BACKUP"
echo "backup: $BACKUP"

# 1. apt-update — add report_settled: true under its onlyif if not present.
#    Match the apt-update onlyif line exactly and append the new attribute.
if grep -q '^    report_settled: true' "$TARGET"; then
    echo "1/3 apt-update report_settled: already present, skipping"
else
    sed -i '/onlyif: find \/var\/lib\/apt\/lists -maxdepth 0 -mmin +1440/a\
    report_settled: true' "$TARGET"
    echo "1/3 apt-update: report_settled: true added"
fi

# 2. reboot-if-needed onlyif — rewrite the inverted shell expression.
sed -i 's|onlyif: test -f /var/run/reboot-required || needs-restarting -r 2>/dev/null; test \$? -eq 1|onlyif: (test -f /var/run/reboot-required) || (needs-restarting -r 2>/dev/null; test $? -eq 1)|' "$TARGET"
if grep -q 'onlyif: (test -f /var/run/reboot-required)' "$TARGET"; then
    echo "2/3 reboot-if-needed: onlyif rewritten"
else
    echo "2/3 reboot-if-needed: NOT rewritten (manual review needed — the live file may differ from the version this patch was built against)"
fi

# 3. pkgin-upgrade unless — switch to dry-run.
sed -i "s|unless: pkgin upgrade 2>/dev/null | grep -q 'nothing to do'|unless: pkgin -n upgrade 2>/dev/null | grep -q 'nothing to do'|" "$TARGET"
if grep -q "unless: pkgin -n upgrade" "$TARGET"; then
    echo "3/3 pkgin-upgrade: unless switched to dry-run (-n)"
else
    echo "3/3 pkgin-upgrade: NOT updated (manual review needed)"
fi

echo
echo "done. Verify with:"
echo "  diff $BACKUP $TARGET"
echo
echo "then publish:"
echo "  vigocli config publish"
