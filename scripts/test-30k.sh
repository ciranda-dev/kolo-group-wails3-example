#!/usr/bin/env bash
# The 30,000-member tests: the gossip simulator at 30,000 nodes, the sizes of a room of 100,000 readers, and the room tests
# that build a group of 30,000 leaves in one process. They need the Kolo workspace (../go.work and ../kolo-gossip), about
# 13 GB of memory and hours, so no workflow runs them. --dry-run prints the commands and runs nothing.
set -euo pipefail
cd "$(dirname "$0")/.."

gossip="${KOLO_GOSSIP_DIR:-../kolo-gossip}"
leaves="${KOLO_GROUP_LEAVES:-30000}"
joiners="${KOLO_GROUP_JOINERS:-100}"
# Twice the 6.6 GB the simulator held at 30,000 nodes, so a run that grows past it stops instead of swapping.
maxheap="${KOLO_GOSSIP_MAXHEAP:-13200000000}"
scenarios="S1,S2,S3,S7,S9,S10,M1"
rooms="TestLargeCommitReachesSixtyFourMembers|TestManyJoinersFetchALargeTreeFromThreeDevices"

commands=(
  "cd $gossip && go run ./internal/sim/cmd/gossipsim -scenario $scenarios -n 30000 -seed 1 -score default -maxheap $maxheap"
  "KOLO_GROUP_MEASURE=1 go test -count=1 -timeout 1h -run TestMeasureReaders -v ./internal/room"
  "KOLO_GROUP_LEAVES=$leaves KOLO_GROUP_JOINERS=$joiners go test -p 1 -count=1 -timeout 4h -run '$rooms' -v ./internal/room"
)

if [ "${1:-}" = "--dry-run" ]; then
  printf '%s\n' "${commands[@]}"
  exit 0
fi

load=$(uptime | sed 's/.*load averages*: //' | tr -d ',' | awk '{print int($1)}')
if [ "$load" -ge 10 ] && [ -z "${KOLO_GROUP_FORCE:-}" ]; then
  echo "kolo-group: the load is $load: wait until it is under 10, or set KOLO_GROUP_FORCE=1" >&2
  exit 1
fi
[ -d "$gossip" ] || { echo "kolo-group: no kolo-gossip folder at $gossip: set KOLO_GOSSIP_DIR" >&2; exit 1; }

# A run that matches no test passes, so check that the room tests are in this checkout before the long steps start.
wanted=$(grep -o 'Test[A-Za-z]*' <<<"$rooms" | sort)
found=$(go test -list "$rooms" ./internal/room | grep '^Test' | sort || true)
if [ "$wanted" != "$found" ]; then
  echo "kolo-group: the room tests of 30,000 leaves are missing from this checkout (found: ${found:-none}); they come with the commit-by-reference change of internal/room" >&2
  exit 1
fi

for command in "${commands[@]}"; do
  echo "+ $command"
  bash -c "$command"
done
