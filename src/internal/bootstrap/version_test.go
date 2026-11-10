package bootstrap

import "testing"

func TestEmbeddedNoticesAndDesktopShareBuildIdentity(t *testing.T) {
	services := services()
	information := services.BuildInformation
	catalog, err := services.Notices()
	if err != nil {
		t.Fatal(err)
	}
	if information.Version == "" || catalog.Version != information.Version || catalog.DebianVersion != information.DebianVersion || catalog.Revision != information.Revision || catalog.Modified != information.Modified {
		t.Fatalf("desktop and notices differ: %+v versus version=%s package=%s revision=%s modified=%t", information, catalog.Version, catalog.DebianVersion, catalog.Revision, catalog.Modified)
	}
}
