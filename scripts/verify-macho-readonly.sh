#!/bin/sh
# Smoke test for the macOS __DATA_CONST regression (issue #176).
#
# macOS 15 Sequoia and 26 Tahoe's dyld ABORT a process at load time if the
# __DATA_CONST segment is missing the SG_READ_ONLY (0x10) segment flag:
#
#   dyld: __DATA_CONST segment missing SG_READ_ONLY flag in <binary>
#
# Apple's native linker (ld-prime) sets that flag; the osxcross/cctools ld64
# used by goreleaser-cross did not — so a cross-compiled cask shipped broken
# while every locally `go build`-ed binary worked. This guard fails the
# release if a darwin binary is produced without the flag, so the broken
# shape can never ship again.
#
# Usage: verify-macho-readonly.sh <mach-o-binary>
set -eu

bin="${1:?usage: verify-macho-readonly.sh <mach-o-binary>}"
[ -f "$bin" ] || { echo "FATAL: $bin does not exist" >&2; exit 1; }

# Pull the SEGMENT-level flags for __DATA_CONST out of `otool -l`. A segment
# block is "cmd LC_SEGMENT_64 ... segname __DATA_CONST ... flags 0xNN"; the
# section sub-blocks that follow also carry a segname field, so we capture the
# FIRST flags line after the segment's segname and stop — that is the segment
# command's flags, not a section's.
flags="$(otool -l "$bin" | awk '
  /^Load command/                                    { inseg=0; seg="" }
  $1=="cmd" && ($2=="LC_SEGMENT_64" || $2=="LC_SEGMENT") { inseg=1 }
  inseg && $1=="segname" && seg==""                  { seg=$2 }
  inseg && $1=="flags" && seg=="__DATA_CONST"        { print $2; exit }
')"

if [ -z "$flags" ]; then
  echo "FATAL: no __DATA_CONST segment found in $bin (is it a Mach-O?)" >&2
  exit 1
fi

# otool prints segment flags as hex (e.g. 0x10 / 0x00000010); some builds
# render the symbolic name. Accept either form.
case "$flags" in
  *READ_ONLY*) echo "ok: $bin __DATA_CONST carries SG_READ_ONLY ($flags)"; exit 0 ;;
esac

# Shell arithmetic understands the 0x prefix; SG_READ_ONLY is 0x10.
if [ $(( flags & 0x10 )) -eq 0 ]; then
  echo "FATAL: $bin __DATA_CONST flags=$flags missing SG_READ_ONLY (0x10)." >&2
  echo "       This binary would abort under the macOS 15+/Tahoe dyld (issue #176)." >&2
  exit 1
fi

echo "ok: $bin __DATA_CONST carries SG_READ_ONLY (flags=$flags)"
