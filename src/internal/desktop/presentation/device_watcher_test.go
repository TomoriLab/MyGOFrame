package presentation

import (
	session "github.com/TomoriLab/MyGOFrame/src/internal/session/domain"
	"testing"
	"time"
)

func TestDeviceWatcherStopsWithoutEnumerating(t *testing.T) {
	application := newTestApplication()
	calls := 0
	original := application.services.VideoDevices
	application.services.VideoDevices = func() ([]session.VideoDevice, error) { calls++; return original() }
	worker := &captureWorker{application: application, stop: make(chan struct{}), deviceWatcherDone: make(chan struct{})}
	close(worker.stop)
	go worker.watchDevices()
	select {
	case <-worker.deviceWatcherDone:
	case <-time.After(time.Second):
		t.Fatal("device watcher did not stop")
	}
	if calls != 0 {
		t.Fatal("device watcher enumerated after shutdown")
	}
}
