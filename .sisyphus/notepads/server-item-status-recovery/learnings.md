- Successfully added ScanAllItemStatus function following the existing callback pattern
- Used same column order: worker_id, file_id, status, sha1, crc32
- Key difference from ScanAllItemStatusByWorker: no WHERE clause, no workerID parameter

## Task 3: Wire HydrateFromDB into Startup Sequence

- Hydration happens after InitDB and before stats logging
- Error handling uses log.Fatalf for loud failure
- Tests pass (exit code 0)

- HydrateFromDB now clears FileExt and fileHashes under the tree mutex, streams item_status rows, then derives ReportCount/HasConflict before one recomputeAllStatsLocked pass

- Recovery hydration is easiest to regression-test by injecting a scan callback that replays `ItemStatus` rows without a live DB
- Conflict bubbling is covered by `ApplyReport` transitions where SHA1s diverge and then converge, preserving status totals while toggling directory conflict counts
