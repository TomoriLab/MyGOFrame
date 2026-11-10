package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func TestDebugPreferencePersistenceAndSessionOverride(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", directory)
	recorder := New(nil)
	if recorder.Enabled() {
		t.Fatal("debug mode must default to disabled")
	}
	recorder.Record("ignored", "disabled")
	if recorder.State().EventCount != 0 {
		t.Fatal("disabled mode retained an event")
	}
	if err := recorder.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "mygoframe/app-settings.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("app settings are not private")
	}
	if !New(nil).Enabled() {
		t.Fatal("debug preference was not restored")
	}
	override := false
	if New(&override).Enabled() || !New(nil).Enabled() {
		t.Fatal("session override changed the saved preference")
	}
	if err := recorder.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if New(nil).Enabled() {
		t.Fatal("disabled preference was not restored")
	}
}

func TestMalformedPreferencesArePreserved(t *testing.T) {
	for _, content := range []string{`{"schemaVersion":9,"debugEnabled":true}`, `{"schemaVersion":1,"debugEnabled":false,"unknown":true}`, `{"schemaVersion":1}`, strings.Repeat("x", 8193), `invalid`} {
		path := filepath.Join(t.TempDir(), "app-settings.json")
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		recorder := newRecorder(path, nil)
		if recorder.Enabled() || recorder.State().Error == "" {
			t.Fatal("invalid preference was accepted")
		}
		if err := recorder.SetEnabled(true); err == nil {
			t.Fatal("invalid preference was overwritten")
		}
		actual, err := os.ReadFile(path)
		if err != nil || string(actual) != content {
			t.Fatal("invalid source file changed")
		}
	}
}

func TestHistoryBoundsUnicodeAndIndependentSnapshots(t *testing.T) {
	recorder := newRecorder(filepath.Join(t.TempDir(), "settings.json"), nil)
	recorder.enabled.Store(true)
	for index := 0; index < Capacity+10; index++ {
		recorder.Record(fmt.Sprint(index), strings.Repeat("한日", 1000))
	}
	snapshot := recorder.Snapshot()
	if len(snapshot.Events) != Capacity || snapshot.State.Dropped != 10 || snapshot.Events[0].Name != "10" {
		t.Fatalf("unbounded or unordered history: %+v", snapshot.State)
	}
	for _, event := range snapshot.Events {
		if len(event.Detail) > maximumDetailBytes || !event.Truncated || !utf8.ValidString(event.Detail) {
			t.Fatal("invalid bounded event text")
		}
	}
	snapshot.Events[0].Name = "caller mutation"
	if recorder.Snapshot().Events[0].Name != "10" {
		t.Fatal("snapshot aliases the recorder array")
	}
	recorder.Clear()
	if recorder.State().EventCount != 0 || recorder.State().Dropped != 0 || recorder.events[0].Detail != "" {
		t.Fatal("clear retained diagnostic data")
	}
	if !recorder.Enabled() {
		t.Fatal("clear changed the debug preference")
	}
}

func TestRecordDoesNotWaitForDiagnosticReaders(t *testing.T) {
	recorder := &Recorder{}
	recorder.enabled.Store(true)
	recorder.mutex.Lock()
	recorder.Record("event", "must not block")
	recorder.mutex.Unlock()
	if recorder.State().Dropped != 1 {
		t.Fatal("contention was not counted")
	}
}

func TestConcurrentDiagnosticOwnership(t *testing.T) {
	recorder := newRecorder(filepath.Join(t.TempDir(), "settings.json"), nil)
	recorder.enabled.Store(true)
	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wait.Go(func() {
			for range 1000 {
				recorder.Record("capture.command", "complete")
			}
		})
	}
	wait.Go(func() {
		for range 100 {
			recorder.Snapshot()
		}
	})
	wait.Go(func() {
		for range 50 {
			recorder.Clear()
		}
	})
	wait.Go(func() {
		for range 3 {
			if err := recorder.SetEnabled(false); err != nil {
				t.Error(err)
			}
			if err := recorder.SetEnabled(true); err != nil {
				t.Error(err)
			}
		}
	})
	wait.Wait()
	snapshot := recorder.Snapshot()
	if len(snapshot.Events) > Capacity {
		t.Fatal("concurrent history exceeded its bound")
	}
	if _, err := json.Marshal(snapshot); err != nil {
		t.Fatal(err)
	}
}

func TestDisabledRecordingAllocatesNothing(t *testing.T) {
	recorder := &Recorder{}
	if allocations := testing.AllocsPerRun(100, func() { recorder.Record("capture.command", "complete") }); allocations != 0 {
		t.Fatalf("disabled diagnostics allocated %g objects", allocations)
	}
}

func BenchmarkDisabledDiagnostics(b *testing.B) {
	recorder := &Recorder{}
	b.ReportAllocs()
	for b.Loop() {
		recorder.Record("capture.command", "complete")
	}
}
