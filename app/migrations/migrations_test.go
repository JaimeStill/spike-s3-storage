package migrations_test

import (
	"testing"

	"github.com/JaimeStill/spike-s3-storage/app/migrations"
)

func TestMigrations_LoadsTheSetInOrder(t *testing.T) {
	set, err := migrations.Migrations()
	if err != nil {
		t.Fatalf("Migrations: %v", err)
	}
	want := []string{"directory_owner", "bookmark"}
	if len(set) != len(want) {
		t.Fatalf("Migrations returned %d migrations, want %d", len(set), len(want))
	}
	for i, name := range want {
		if set[i].Version != i+1 || set[i].Name != name {
			t.Errorf("migration %d = %d %s, want %d %s", i, set[i].Version, set[i].Name, i+1, name)
		}
		if set[i].Up == "" || set[i].Down == "" {
			t.Errorf("migration %d %s lacks an up or a down text", set[i].Version, set[i].Name)
		}
	}
}
