package migrations

import "testing"

func TestReadMigrationsHasUniqueVersions(t *testing.T) {
	migrations, err := readMigrations()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, migration := range migrations {
		if previous := seen[migration.version]; previous != "" {
			t.Fatalf("duplicate migration version %s: %s and %s", migration.version, previous, migration.name)
		}
		seen[migration.version] = migration.name
	}
}
