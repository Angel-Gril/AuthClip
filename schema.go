package main

import (
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/migrations"
	"github.com/pocketbase/pocketbase/tools/types"
)

const (
	usersCollection = "portal_users"
	tasksCollection = "portal_tasks"
)

func init() {
	migrations.Register(func(app core.App) error {
		users := core.NewAuthCollection(usersCollection)
		users.Fields.Add(
			&core.SelectField{Name: "role", Required: true, MaxSelect: 1, Values: []string{"admin", "member"}},
		)
		if err := app.Save(users); err != nil {
			return err
		}

		tasks := core.NewBaseCollection(tasksCollection)
		readRule := "@request.auth.collectionName = '" + usersCollection +
			"' && (@request.auth.role = 'admin' || assignee = @request.auth.id)"
		tasks.ListRule = types.Pointer(readRule)
		tasks.ViewRule = types.Pointer(readRule)
		tasks.Fields.Add(
			&core.RelationField{Name: "assignee", CollectionId: users.Id, Required: true, MaxSelect: 1},
			&core.SelectField{Name: "flow", Required: true, MaxSelect: 1, Values: []string{"team", "code"}},
			&core.SelectField{Name: "state", Required: true, MaxSelect: 1, Values: []string{"pending", "submitted", "issue", "expired", "confirmed"}},
			&core.EmailField{Name: "email"},
			&core.TextField{Name: "region", Max: 2},
			&core.SelectField{Name: "issue", MaxSelect: 1, Values: []string{"invitation", "signin", "access", "other"}},
			&core.JSONField{Name: "timeline", MaxSize: 65536},
			&core.DateField{Name: "confirmed_at"},
			&core.AutodateField{Name: "created", OnCreate: true},
			&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
		)
		tasks.AddIndex("one_open_portal_task", true, "assignee, flow", "state != 'confirmed'")
		tasks.AddIndex("portal_history_expiry", false, "confirmed_at", "state = 'confirmed'")
		if err := app.Save(tasks); err != nil {
			return err
		}

		settings := app.Settings()
		settings.Logs.LogIP = false
		settings.RateLimits.Enabled = true
		settings.RateLimits.Rules = append([]core.RateLimitRule{
			{Label: usersCollection + ":auth", Duration: 60, MaxRequests: 10},
		}, settings.RateLimits.Rules...)
		return app.Save(settings)
	}, func(app core.App) error {
		for _, name := range []string{tasksCollection, usersCollection} {
			collection, err := app.FindCollectionByNameOrId(name)
			if err != nil {
				return err
			}
			if err := app.Delete(collection); err != nil {
				return err
			}
		}
		settings := app.Settings()
		for i, rule := range settings.RateLimits.Rules {
			if strings.HasPrefix(rule.Label, usersCollection+":") {
				settings.RateLimits.Rules = append(settings.RateLimits.Rules[:i], settings.RateLimits.Rules[i+1:]...)
				break
			}
		}
		return app.Save(settings)
	}, "2026111901_portal.go")

	migrations.Register(func(app core.App) error {
		tasks, err := app.FindCollectionByNameOrId(tasksCollection)
		if err != nil {
			return err
		}
		tasks.Fields.Add(
			&core.TextField{Name: "ip", Max: 45},
			&core.SelectField{Name: "entry", MaxSelect: 1, Values: []string{"team-site", "team-guide", "code-auth", "code-setup"}},
		)
		return app.Save(tasks)
	}, func(app core.App) error {
		tasks, err := app.FindCollectionByNameOrId(tasksCollection)
		if err != nil {
			return err
		}
		tasks.Fields.RemoveByName("ip")
		tasks.Fields.RemoveByName("entry")
		return app.Save(tasks)
	}, "2026111902_network_and_official_entries.go")
}
