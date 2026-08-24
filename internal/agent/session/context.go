package session

import "fmt"

// BranchEntries returns defensive entries from root to the selected lane leaf.
func BranchEntries(snapshot Snapshot, lane Lane) ([]Entry, error) {
	var leaf EntryID
	found := false
	for _, pointer := range snapshot.Lanes {
		if pointer.Lane == lane {
			leaf = pointer.LeafID
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("build session branch: lane %q not found", lane)
	}
	byID := make(map[EntryID]Entry, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		byID[entry.ID] = entry
	}
	var reverse []Entry
	seen := make(map[EntryID]struct{})
	for leaf != "" {
		if _, duplicate := seen[leaf]; duplicate {
			return nil, fmt.Errorf("build session branch: cycle at entry %q", leaf)
		}
		entry, exists := byID[leaf]
		if !exists {
			return nil, fmt.Errorf("build session branch: entry %q not found", leaf)
		}
		seen[leaf] = struct{}{}
		reverse = append(reverse, cloneEntry(entry))
		leaf = entry.ParentID
	}
	entries := make([]Entry, len(reverse))
	for index := range reverse {
		entries[len(reverse)-1-index] = reverse[index]
	}
	return entries, nil
}
