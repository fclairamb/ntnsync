package notion

import "strings"

// uuidLen is the length of a Notion object ID once its dashes are removed.
const uuidLen = 32

// NormalizeID returns the canonical, dash-less form of a Notion object ID.
//
// Notion's REST API and webhook events use the dashed UUID form
// (e.g. "388aa28b-3ffb-80b6-9e5b-c6a0eeaebf64"), while ntnsync keys all of its
// registry files (page-{id}.json) and queue entries by the dash-less form
// ("388aa28b3ffb80b69e5bc6a0eeaebf64"). Always normalize an ID before using it
// as a key or comparing two IDs for equality; mixing the two forms causes a
// page to be stored — and later detected as a filename conflict — twice.
func NormalizeID(id string) string {
	return strings.ReplaceAll(id, "-", "")
}

// DenormalizeID re-inserts the standard UUID dashes (8-4-4-4-12) into a
// normalized 32-character Notion ID. It exists so callers can still locate
// legacy registry files that were written under the dashed form before IDs were
// normalized on every code path. Inputs that are not exactly 32 characters long
// are returned unchanged.
func DenormalizeID(id string) string {
	if len(id) != uuidLen {
		return id
	}
	return id[0:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:32]
}
