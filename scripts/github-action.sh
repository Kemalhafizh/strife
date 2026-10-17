#!/usr/bin/env bash
# Runs the strife CLI on behalf of the GitHub Action defined in action.yml.
# It lives in a script, not inline YAML, so it can be linted and tested
# locally:
#
#   STRIFE_ARGS="sim --seeds 1..10" STRIFE_BIN=bin/strife scripts/github-action.sh
#
# Inputs (environment): STRIFE_ARGS (required), STRIFE_BIN, STRIFE_OUT,
# STRIFE_JUNIT, STRIFE_SUMMARY, plus GitHub's GITHUB_OUTPUT and
# GITHUB_STEP_SUMMARY when present. Arguments are split on whitespace and are
# never evaluated by the shell, so quoting and globbing do not apply.
#
# The script always exits 0 after recording the outcome; the action's final
# step decides whether the job fails, so reports are uploaded either way.
set -uo pipefail

if [ -z "${STRIFE_ARGS:-}" ]; then
  echo "::error title=Strife::the 'command' input is empty"
  echo "exit-code=2" >>"${GITHUB_OUTPUT:-/dev/null}"
  echo "outcome=usage-error" >>"${GITHUB_OUTPUT:-/dev/null}"
  exit 0
fi

bin=${STRIFE_BIN:-strife}
out=${STRIFE_OUT-strife-out}
junit=${STRIFE_JUNIT-strife-junit.xml}
summary=${STRIFE_SUMMARY:-true}

read -r -a args <<<"$STRIFE_ARGS"
sub=${args[0]}
rest=("${args[@]:1}")

# CI outputs go right after the subcommand: Go's flag parsing stops at the
# first positional argument (e.g. `check history.jsonl`), and flags given by
# the user later on still win because the last occurrence of a flag counts.
extra=()
case $sub in
sim | campaign | shrink | check | run)
  if [ -n "$junit" ]; then
    mkdir -p "$(dirname "$junit")"
    extra+=(--junit "$junit")
  fi
  if [ "$summary" = true ] && [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    extra+=(--summary "$GITHUB_STEP_SUMMARY")
  fi
  if [ -n "$out" ]; then
    if [ "$sub" = check ]; then
      extra+=(--out "$out/check")
    else
      extra+=(--out "$out")
    fi
  fi
  ;;
esac

echo "+ strife $sub ${extra[*]} ${rest[*]}"
"$bin" "$sub" "${extra[@]}" "${rest[@]}"
code=$?

case $code in
0) outcome=passed ;;
1) outcome=violation ;;
2) outcome=usage-error ;;
3) outcome=inconclusive ;;
*) outcome=error ;;
esac

{
  echo "exit-code=$code"
  echo "outcome=$outcome"
  echo "junit=$junit"
  echo "report-dir=$out"
} >>"${GITHUB_OUTPUT:-/dev/null}"

case $outcome in
violation)
  echo "::error title=Strife found a consistency violation::strife $sub exited with code 1. The job summary, the JUnit report and the uploaded reports contain the counterexample and a one-line reproduction command."
  ;;
inconclusive)
  echo "::warning title=Strife was inconclusive::The checker's search budget ran out before a verdict (exit code 3). Raise --max-steps or shorten the history."
  ;;
usage-error | error)
  echo "::error title=Strife failed to run::strife $sub exited with code $code."
  ;;
esac
exit 0
