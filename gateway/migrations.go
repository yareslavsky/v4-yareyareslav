package main

import (
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func runMigrations(databaseURL string) error {
	sourceDriver, err := iofs.New(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("create migration source: %w", err)
	}

	m, err := migrate.NewWithSourceInstance(
		"iofs",
		sourceDriver,
		databaseURL,
	)
	if err != nil {
		_ = sourceDriver.Close()
		return fmt.Errorf("create migrator: %w", err)
	}

	migrationErr := m.Up()
	sourceErr, databaseErr := m.Close()

	if migrationErr != nil &&
		!errors.Is(migrationErr, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", migrationErr)
	}

	if sourceErr != nil {
		return fmt.Errorf("close migration source: %w", sourceErr)
	}

	if databaseErr != nil {
		return fmt.Errorf("close migration database: %w", databaseErr)
	}

	return nil
}
