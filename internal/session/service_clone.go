package session

// The functions in this file defensively copy session state so callers receive
// isolated values instead of references into Service's mutable snapshot.

func cloneSessionSnapshot(value SessionSnapshot) SessionSnapshot {
	value.Messages = cloneMessages(value.Messages)
	value.Turns = append([]TurnRecord(nil), value.Turns...)
	value.Patches = clonePatchRecords(value.Patches)
	value.RecoveryWarnings = append([]RecoveryWarning(nil), value.RecoveryWarnings...)
	return value
}

func cloneMessages(values []Message) []Message {
	return append([]Message(nil), values...)
}

func clonePatchRecords(values []PatchRecord) []PatchRecord {
	cloned := make([]PatchRecord, len(values))
	for index, value := range values {
		cloned[index] = clonePatchRecord(value)
	}
	return cloned
}

func clonePatchRecord(value PatchRecord) PatchRecord {
	value.Files = append([]PatchedFile(nil), value.Files...)
	return value
}

func containsPatchRecord(values []PatchRecord, id PatchID) bool {
	for _, value := range values {
		if value.ID == id {
			return true
		}
	}
	return false
}
