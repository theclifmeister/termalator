#!/bin/sh
# Prints a go test -run pattern for shard I of N of the e2e scenarios
# matching PATTERN: the scenario names, sorted, dealt round-robin.
#
#   scripts/e2e-shard.sh '^TestSmoke' 2/3
set -eu
pattern=$1
i=${2%/*}
n=${2#*/}
case "$i/$n" in
*[!0-9/]* | /* | */) echo "e2e-shard: want I/N, got $2" >&2; exit 2 ;;
esac
if [ "$n" -lt 1 ] || [ "$i" -lt 1 ] || [ "$i" -gt "$n" ]; then
	echo "e2e-shard: want 1 <= I <= N, got $2" >&2
	exit 2
fi
names=$(${GO:-go} test -list "$pattern" ./internal/e2e | grep '^Test' | sort |
	awk -v i="$i" -v n="$n" '(NR - 1) % n == i - 1' | paste -sd '|' -)
if [ -z "$names" ]; then
	echo "e2e-shard: no scenarios in shard $2 of $pattern" >&2
	exit 1
fi
printf '^(%s)$\n' "$names"
