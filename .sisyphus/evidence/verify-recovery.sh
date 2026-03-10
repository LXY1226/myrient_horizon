#!/usr/bin/env bash
set -euo pipefail

echo "== Build server =="
go build ./cmd/server

cat <<'EOF'

Manual recovery verification
1. Start the server against a database containing `item_status` rows for at least:
   - one file with matching SHA1 values from multiple workers
   - one file with differing SHA1 values from multiple workers
2. Confirm startup completes without hydration errors.
3. Query the overview and tree APIs after startup.
4. Verify API responses show:
   - recovered `verified_files`/`archived_files`/`failed_files` counts from persisted rows
   - `conflict` counts incremented for directories that contain mismatched SHA1 reports
   - per-file report counts restored after restart
   - conflict counts clear after workers resend matching SHA1 values for the same file
5. Spot-check that files with out-of-range `file_id` rows are ignored and do not affect totals.

Suggested endpoints to inspect
- `GET /api/overview`
- `GET /api/tree?dir_id=0`
- any file/detail endpoint that exposes recovered status or conflict state

Expected recovery behavior
- highest reported status wins for each file
- conflict state depends on differing SHA1 values across workers
- directory conflict totals bubble to ancestor directories
EOF
