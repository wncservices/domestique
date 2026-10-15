#!/usr/bin/env bash
# Print a `go test -run` regex selecting one shard of a package's top-level
# tests, so CI can split internal/api (most of the suite's wall time) across
# jobs.
#
#   scripts/test-shard.sh <shard 1..N> <N> [package]   (run from apps/api)
#   go test ./internal/api -race -run "$(scripts/test-shard.sh 2 3)"
#
# A test belongs to the shard its name hashes to, so the split is deterministic,
# does not move when unrelated tests are added, and shards cannot overlap or
# miss one: every name lands in exactly one. Subtests run with their parent.
set -euo pipefail

shard=${1:?shard index (1-based)}
total=${2:?number of shards}
pkg=${3:-./internal/api}

# -race matches how the tests are run, so listing reuses the build cache
# instead of compiling the package a second time.
go test -race -list '.*' "$pkg" |
  grep -E '^(Test|Example)' |
  awk -v shard="$shard" -v total="$total" '
    {
      h = 5381
      for (i = 1; i <= length($0); i++) {
        h = (h * 33 + index("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ_abcdefghijklmnopqrstuvwxyz", substr($0, i, 1))) % 1000003
      }
      if (h % total == shard - 1) names[n++] = $0
    }
    END {
      if (n == 0) { print "^$"; exit }
      out = "^("
      for (i = 0; i < n; i++) out = out (i ? "|" : "") names[i]
      print out ")$"
    }'
