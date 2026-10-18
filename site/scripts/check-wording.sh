#!/bin/sh
# Fails if the built site makes a claim we have ruled out: the "Avoid" column
# of the wording table in docs/ROADMAP.md, plus claims of regulatory approval
# that a compliance dossier cannot support.
#
# Usage: scripts/check-wording.sh <built site dir> <ROADMAP.md>
set -eu
site=${1:?usage: check-wording.sh SITE_DIR ROADMAP.md}
roadmap=${2:?usage: check-wording.sh SITE_DIR ROADMAP.md}

phrases=$(mktemp)
trap 'rm -f "$phrases"' EXIT

# Rows look like: | "100% identical reproducibility" | "..." |
sed -n '/^## Wording guidelines/,/^## /p' "$roadmap" |
  sed -n 's/^| *"\([^"]*\)" *|.*/\1/p' >"$phrases"
if [ ! -s "$phrases" ]; then
  echo "check-wording: found no phrases in the wording table of $roadmap" >&2
  exit 1
fi
cat >>"$phrases" <<'EOF'
certified by OJK
OJK-certified
OJK certified
approved by OJK
OJK-approved
tersertifikasi OJK
bersertifikat OJK
disetujui OJK
lolos audit OJK
dijamin lolos audit
guaranteed to pass
EOF

status=0
while IFS= read -r phrase; do
  [ -n "$phrase" ] || continue
  if hits=$(grep -rilF --include='*.html' -- "$phrase" "$site"); then
    echo "check-wording: \"$phrase\" appears in:" >&2
    echo "$hits" | sed 's/^/  /' >&2
    status=1
  fi
done <"$phrases"
[ "$status" -eq 0 ] && echo "check-wording: $(wc -l <"$phrases" | tr -d ' ') phrases, none found"
exit "$status"
