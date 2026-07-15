package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// contentHash produces a stable hash of a normalized core object, used to
// detect whether a previously-synced item actually changed (and so an
// Update call to Kentik can be skipped when it didn't). encoding/json
// marshals map keys in sorted order, which combined with each core type's
// fixed field order makes this deterministic across runs.
func contentHash(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// core.Device/Site/IPGroup contain only JSON-marshalable fields; a
		// marshal error here would mean a programming error, not bad input.
		panic("sync: content hash marshal: " + err.Error())
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
