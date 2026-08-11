package wire_test

import (
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/the-protobuf-project/orm/plugin/factory/wire"
	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/golden"
)

// TestRelationalTargetsRejectMongo verifies the relational database targets fail
// loudly when a datasource declares provider mongodb instead of silently
// emitting nonsense.
func TestRelationalTargetsRejectMongo(t *testing.T) {
	req := golden.BuildRequest(t, filepath.Join("testdata", "cases", "mongo"))

	for _, target := range []string{"gorm", "sql"} {
		p, err := protogen.Options{}.New(req)
		if err != nil {
			t.Fatalf("protogen: %v", err)
		}
		err = protokit.RunPlugin(p, protokit.Options{Target: target}, ormPlugin())
		if err == nil || !strings.Contains(err.Error(), "mongodb") {
			t.Errorf("%s on mongodb provider: want provider error, got %v", target, err)
		}
	}
}

// TestUnknownTarget verifies the registry rejects unknown and missing targets.
func TestUnknownTarget(t *testing.T) {
	req := golden.BuildRequest(t, filepath.Join("testdata", "cases", "wkt"))

	for _, target := range []string{"", "typeorm"} {
		p, err := protogen.Options{}.New(req)
		if err != nil {
			t.Fatalf("protogen: %v", err)
		}
		if err := protokit.RunPlugin(p, protokit.Options{Target: target}, ormPlugin()); err == nil {
			t.Errorf("target %q: want error, got nil", target)
		}
	}
}
