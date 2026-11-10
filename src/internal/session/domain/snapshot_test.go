package domain

import (
	"encoding/json"
	"testing"
)

func TestSessionSnapshotPreservesCountersAndRejectsUnknownSchema(t *testing.T) {
	var snapshot SessionSnapshot
	if err := json.Unmarshal([]byte(`{"schemaVersion":1,"generation":9007199254740993,"commandID":18446744073709551614}`), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Generation != 9007199254740993 || snapshot.CommandID != 18446744073709551614 || snapshot.Validate() != nil {
		t.Fatal("snapshot counters lost integer precision")
	}
	snapshot.SchemaVersion = 2
	if snapshot.Validate() == nil {
		t.Fatal("accepted an unsupported snapshot schema")
	}
}
