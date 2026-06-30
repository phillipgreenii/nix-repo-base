# shellcheck shell=bash
if [ -n "${NO_COLOR:-}" ]; then
  _act_color=0
elif [ -n "${CLICOLOR_FORCE:-}" ]; then
  _act_color=1
elif [ -t 1 ]; then
  _act_color=1
else
  _act_color=0
fi
case "${LC_ALL:-${LC_CTYPE:-}}" in
*UTF-8* | *utf-8* | *UTF8* | *utf8*) _act_utf8=1 ;;
*) _act_utf8=0 ;;
esac
if [ "$_act_utf8" = 1 ]; then
  _act_m_ok='✓ '
  _act_m_warn='⚠ '
  _act_m_fail='✗ '
else
  _act_m_ok='[OK]   '
  _act_m_warn='[WARN] '
  _act_m_fail='[FAIL] '
fi
if [ "$_act_color" = 1 ]; then
  _act_c_ok=$'\033[32m'
  _act_c_warn=$'\033[33m'
  _act_c_fail=$'\033[31m'
  _act_c_off=$'\033[0m'
else
  _act_c_ok=""
  _act_c_warn=""
  _act_c_fail=""
  _act_c_off=""
fi
# act_* form a small library; a given activation section may call only
# some of them, so SC2329 (function never invoked) is expected and benign.
# shellcheck disable=SC2329
act_ok() { printf '%s\n' "  ${_act_c_ok}${_act_m_ok}${_act_c_off}$*"; }
# shellcheck disable=SC2329
act_warn() { printf '%s\n' "  ${_act_c_warn}${_act_m_warn}${_act_c_off}$*"; }
# shellcheck disable=SC2329
act_fail() { printf '%s\n' "  ${_act_c_fail}${_act_m_fail}${_act_c_off}$*"; }
# shellcheck disable=SC2329
act_info() { printf '%s\n' "    $*"; }
# 2-space indent: text aligns to the glyph column (where ✓/⚠/✗ sit),
# one step left of act_info's 4-space text column. For recovery/inspect
# hints printed as siblings of an act_fail line.
# shellcheck disable=SC2329
act_detail() { printf '%s\n' "  $*"; }
