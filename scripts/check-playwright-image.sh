#!/bin/sh
# The e2e image's browsers come from its base image, the test runner from
# apps/e2e/package-lock.json, and Playwright refuses to launch when the two
# differ. Nothing builds that image on a pull request, so a lockfile bump on
# its own (Renovate's "web dependencies", 2026-11-13: 1.62.1 -> 1.65.0) only
# failed at the post-promotion gate, aborting a healthy rollout. This makes
# the mismatch a CI failure instead.
set -eu
cd "$(dirname "$0")/.."

image=$(sed -n 's#^FROM mcr\.microsoft\.com/playwright:v\([0-9.]*\)-.*#\1#p' apps/e2e/Dockerfile)
locked=$(node -p 'require("./apps/e2e/package-lock.json").packages["node_modules/@playwright/test"].version')

if [ -z "$image" ]; then
  echo "apps/e2e/Dockerfile: no mcr.microsoft.com/playwright base image found" >&2
  exit 1
fi
if [ "$image" != "$locked" ]; then
  echo "apps/e2e: the Dockerfile's Playwright image is v$image but package-lock.json has @playwright/test $locked." >&2
  echo "Move both to the same version." >&2
  exit 1
fi
echo "apps/e2e: Playwright $locked in both the image and the lockfile"
