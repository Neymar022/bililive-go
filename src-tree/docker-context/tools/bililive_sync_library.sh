#!/bin/sh
set -eu

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
PROJECT_ROOT="$(CDPATH= cd -- "${SCRIPT_DIR}/.." && pwd)"

PYTHON_BIN="${PYTHON_BIN:-}"
TOOLS_DIR="${TOOLS_DIR:-${SCRIPT_DIR}}"
REPORT_DIR="${REPORT_DIR:-${PROJECT_ROOT}/reports}"
LOCK_DIR="${LOCK_DIR:-${PROJECT_ROOT}/.sync.lock}"

if [ -d "/srv/bililive-source" ] && [ -d "/srv/bililive" ]; then
  DEFAULT_SOURCE_ROOT="/srv/bililive-source"
  DEFAULT_OUTPUT_ROOT="/srv/bililive"
else
  DEFAULT_SOURCE_ROOT="${PROJECT_ROOT}/srt_video"
  DEFAULT_OUTPUT_ROOT="${PROJECT_ROOT}/video"
fi

SOURCE_ROOT="${SOURCE_ROOT:-${DEFAULT_SOURCE_ROOT}}"
OUTPUT_ROOT="${OUTPUT_ROOT:-${DEFAULT_OUTPUT_ROOT}}"

if [ -z "${PYTHON_BIN}" ]; then
  PYTHON_BIN="$(command -v python3 || true)"
fi

if [ -z "${PYTHON_BIN}" ]; then
  echo "python3 not found" >&2
  exit 127
fi

cleanup() {
  rmdir "${LOCK_DIR}"
}

if ! mkdir "${LOCK_DIR}" 2>/dev/null; then
  echo "bililive sync already running, skip"
  exit 0
fi

trap cleanup EXIT HUP INT TERM

"${PYTHON_BIN}" "${TOOLS_DIR}/bililive_media_organizer.py" --root "${SOURCE_ROOT}" --report-dir "${REPORT_DIR}"
"${PYTHON_BIN}" "${TOOLS_DIR}/bililive_tv_library_builder.py" --source-root "${SOURCE_ROOT}" --output-root "${OUTPUT_ROOT}" --report-dir "${REPORT_DIR}"
