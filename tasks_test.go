package main

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

func TestHistoryRetention(t *testing.T) {
	app := core.NewBaseApp(core.BaseAppConfig{DataDir: t.TempDir()})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.ResetBootstrapState() })
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	member, err := newAccount(app, "member@example.com", "member", "test-only-account-password")
	if err != nil {
		t.Fatal(err)
	}
	collection, err := app.FindCollectionByNameOrId(tasksCollection)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	for _, scenario := range []struct {
		name  string
		state string
		at    time.Time
		keep  bool
	}{
		{"seven days old", "confirmed", now.Add(-7 * 24 * time.Hour), false},
		{"six days old", "confirmed", now.Add(-6 * 24 * time.Hour), true},
		{"waiting on expired invitation", "expired", time.Time{}, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			task := core.NewRecord(collection)
			task.Set("assignee", member.Id)
			task.Set("flow", "team")
			task.Set("state", scenario.state)
			task.Set("confirmed_at", scenario.at)
			if err := app.Save(task); err != nil {
				t.Fatal(err)
			}
			if err := pruneHistory(app, now); err != nil {
				t.Fatal(err)
			}
			_, err := app.FindRecordById(tasksCollection, task.Id)
			if scenario.keep && err != nil {
				t.Fatalf("expected to keep the task: %v", err)
			}
			if !scenario.keep && !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("expected to remove the task: %v", err)
			}
		})
	}
}
