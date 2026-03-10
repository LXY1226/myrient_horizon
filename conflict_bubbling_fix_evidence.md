# Conflict Bubbling Fix Evidence

## Summary
Successfully implemented the conflict bubbling fix in `internal/server/tree.go` to properly track conflict state changes when files are updated.

## Changes Made

### 1. ApplyReport Function (lines 131-158)

**Added:**
- `oldConflict := file.Ext.HasConflict` after capturing `oldStatus`

**Changed:**
- Line 155: Split the conflict assignment into two lines:
  ```go
  newConflict := st.hasConflictLocked(fileID)
  file.Ext.HasConflict = newConflict
  ```

- Line 158: Updated bubbleDelta call to pass both old and new conflict states:
  ```go
  st.bubbleDelta(file.DirIdx, oldStatus, file.Ext.BestStatus, oldConflict, newConflict, file.Size)
  ```

### 2. bubbleDelta Function (lines 223-238)

**Changed signature from:**
```go
func (st *ServerTree) bubbleDelta(dirID int32, oldStatus, newStatus protocol.TaskStatus, hasConflict bool, fileSize int64)
```

**To:**
```go
func (st *ServerTree) bubbleDelta(dirID int32, oldStatus, newStatus protocol.TaskStatus, oldConflict, newConflict bool, fileSize int64)
```

**Changed early return condition from:**
```go
if oldStatus == newStatus { return }
```

**To:**
```go
if oldStatus == newStatus && oldConflict == newConflict { return }
```

**Added inside the for loop after incrStatus:**
```go
if oldConflict && !newConflict {
    s.Conflict--
} else if !oldConflict && newConflict {
    s.Conflict++
}
```

## Verification

Build passes successfully:
```bash
$ go build ./...
# No errors
```

## Git Diff

See the attached git diff for the complete changes:
```
diff --git a/internal/server/tree.go b/internal/server/tree.go
index d4d844d..8a3048d 100644
--- a/internal/server/tree.go
+++ b/internal/server/tree.go
@@ -129,7 +130,7 @@ func (st *ServerTree) ApplyReport(fileID int64, workerID int, status protocol.Ta
 
 	file := &st.base.Files[fileID]
 	oldStatus := file.Ext.BestStatus
-
+	oldConflict := file.Ext.HasConflict
 	// Track per-worker SHA1 for conflict detection.
 	if sha1 != nil {
 		if st.fileHashes[fileID] == nil {
@@ -151,9 +152,53 @@ func (st *ServerTree) ApplyReport(fileID int64, workerID int, status protocol.Ta
 	}
 
 	// Check for SHA1 conflicts.
-	file.Ext.HasConflict = st.hasConflictLocked(fileID)
+	newConflict := st.hasConflictLocked(fileID)
+	file.Ext.HasConflict = newConflict
 	// Bubble dirExt delta up the ancestor chain.
-	st.bubbleDelta(file.DirIdx, oldStatus, file.Ext.BestStatus, file.Ext.HasConflict, file.Size)
+	st.bubbleDelta(file.DirIdx, oldStatus, file.Ext.BestStatus, oldConflict, newConflict, file.Size)
 
 // bubbleDelta updates dirExt along the ancestor chain when a file's status changes.
-func (st *ServerTree) bubbleDelta(dirID int32, oldStatus, newStatus protocol.TaskStatus, hasConflict bool, fileSize int64) {
-	if oldStatus == newStatus {
+	func (st *ServerTree) bubbleDelta(dirID int32, oldStatus, newStatus protocol.TaskStatus, oldConflict, newConflict bool, fileSize int64) {
+		if oldStatus == newStatus && oldConflict == newConflict {
 		return
 	}
 	for id := dirID; id >= 0; id = st.base.Dirs[id].ParentIdx {
 		s := &st.base.Dirs[id].Ext
 		decrStatus(s, oldStatus, fileSize)
 		incrStatus(s, newStatus, fileSize)
+		if oldConflict && !newConflict {
+			s.Conflict--
+		} else if !oldConflict && newConflict {
+			s.Conflict++
+		}
 	}
 }
```

## Files Modified
- `internal/server/tree.go`

## Date
2026-03-08
