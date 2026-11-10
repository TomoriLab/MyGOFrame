// Package notices exposes immutable notices collected from the build inputs.
package notices

import (
	"encoding/json"
	"fmt"
	"sync"

	about "github.com/TomoriLab/MyGOFrame/src/internal/about/domain"
)

var load = sync.OnceValues(func() (*about.Catalog, error) {
	var catalog about.Catalog
	if err := json.Unmarshal([]byte(catalogJSON), &catalog); err != nil {
		return nil, fmt.Errorf("Read embedded license catalog: %w", err)
	}
	if catalog.SchemaVersion != 1 {
		return nil, fmt.Errorf("Unsupported license catalog version: %d", catalog.SchemaVersion)
	}
	return &catalog, nil
})

// Load returns build-time notices without filesystem or network access.
func Load() (*about.Catalog, error) { return load() }
