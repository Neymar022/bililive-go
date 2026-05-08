#!/bin/sh
# Regression test for bililive_sync_library.sh — ensures wrapper picks up
# new mp4 from srt_video/ and creates Plex-format hardlink in video/.
#
# Catches the cron-organizer-source-root-mismatch bug where wrapper scanned
# stale source/ directory while bililive-go pipeline writes to srt_video/.
#
# Run on NAS host (ext4 mount) with the deployment scripts installed.
#
# Override paths via env if needed:
#   SYNC_TEST_SOURCE_ROOT=/path/to/srt_video
#   SYNC_TEST_OUTPUT_ROOT=/path/to/video
#   SYNC_TEST_TOOLS_DIR=/path/to/tools
#
set -u

TOOLS_DIR="${SYNC_TEST_TOOLS_DIR:-/volume2/docker/bililive-go/tools}"
SRC_ROOT="${SYNC_TEST_SOURCE_ROOT:-/volume2/docker/bililive-go/srt_video}"
OUT_ROOT="${SYNC_TEST_OUTPUT_ROOT:-/volume2/docker/bililive-go/video}"

WRAPPER="${TOOLS_DIR}/bililive_sync_library.sh"
TEST_TS=$(date +%s)
TEST_HOST="syncfix-test-host-${TEST_TS}"
TEST_NAME="${TEST_HOST} - 2026-05-09 03-00-00 - sync regression test.mp4"
SRT_DIR="${SRC_ROOT}/${TEST_HOST}"
SRT_FILE="${SRT_DIR}/${TEST_NAME}"
VIDEO_DIR="${OUT_ROOT}/${TEST_HOST}"

cleanup() {
    rm -rf "${SRT_DIR}"
    rm -rf "${VIDEO_DIR}"
    rm -rf "${SRC_ROOT}/../.sync.lock" 2>/dev/null
}
trap cleanup EXIT

# --- RED setup ---
mkdir -p "${SRT_DIR}"
echo "fake-mp4-payload-${TEST_TS}" > "${SRT_FILE}"
echo "[setup] created ${SRT_FILE}"

# --- Action ---
echo "[action] running ${WRAPPER}"
bash "${WRAPPER}" > /tmp/sync-test.log 2>&1
RC=$?
echo "[action] wrapper exit=${RC}"

# --- Assert hardlink in video/ ---
HARDLINK=$(find "${VIDEO_DIR}" -name "*.mp4" -type f 2>/dev/null | head -1)
if [ -z "${HARDLINK}" ]; then
    echo "FAIL: no mp4 hardlink found under ${VIDEO_DIR}"
    echo "(this means cron wrapper is not creating library entries from srt_video/)"
    echo "--- Wrapper log tail ---"
    tail -10 /tmp/sync-test.log
    exit 1
fi

# --- Assert it is a real hardlink (same inode) ---
SRC_INODE=$(stat -c '%i' "${SRT_FILE}" 2>/dev/null)
DST_INODE=$(stat -c '%i' "${HARDLINK}" 2>/dev/null)
if [ "${SRC_INODE}" != "${DST_INODE}" ]; then
    echo "FAIL: hardlink at ${HARDLINK} has different inode (${DST_INODE}) than src (${SRC_INODE})"
    exit 1
fi

# --- Assert Plex-format filename ---
case "$(basename "${HARDLINK}")" in
    *S01E*"2026-05-09"*) ;;
    *)
        echo "FAIL: hardlink filename does not match Plex format S01E…2026-05-09…"
        echo "  got: $(basename "${HARDLINK}")"
        exit 1
        ;;
esac

echo "PASS: hardlink created at ${HARDLINK} (inode ${SRC_INODE}, Plex format ✓)"
exit 0
