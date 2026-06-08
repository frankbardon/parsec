#!/usr/bin/env bash
# Stop hook: enforce `make lint` + `make test` after Claude turns that touched Go code.
# Exit 2 + stderr blocks Claude from declaring done; Claude reads stderr and must fix.
# No-op (exit 0) when no Go/Makefile/go.{mod,sum} changes exist in the working tree.

set -u
cd "$(dirname "$0")/../.." || exit 0

# Skip if working tree has no relevant changes vs HEAD.
if ! git diff --quiet -- '*.go' 'go.mod' 'go.sum' 'Makefile' 2>/dev/null \
   || ! git diff --cached --quiet -- '*.go' 'go.mod' 'go.sum' 'Makefile' 2>/dev/null \
   || git ls-files --others --exclude-standard -- '*.go' 'go.mod' 'go.sum' 'Makefile' 2>/dev/null | grep -q .; then
    :
else
    exit 0
fi

fail() {
    # Stop-hook contract: exit 2 with stderr feeds back to Claude as a blocking reason.
    printf '%s\n' "$1" >&2
    exit 2
}

LINT_OUT=$(make lint 2>&1)
LINT_RC=$?
if [ $LINT_RC -ne 0 ]; then
    fail "make lint failed (exit $LINT_RC). Fix lint errors before declaring done.

$LINT_OUT"
fi

TEST_OUT=$(make test 2>&1)
TEST_RC=$?
if [ $TEST_RC -ne 0 ]; then
    fail "make test failed (exit $TEST_RC). Fix failing tests before declaring done.

$TEST_OUT"
fi

exit 0
