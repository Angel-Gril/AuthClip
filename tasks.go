package main

import (
	"database/sql"
	"errors"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

const historyLifetime = 7 * 24 * time.Hour

type taskEvent struct {
	Kind string    `json:"kind"`
	At   time.Time `json:"at"`
}

func appendEvent(task *core.Record, kind string, at time.Time) error {
	var events []taskEvent
	if err := task.UnmarshalJSONField("timeline", &events); err != nil {
		return err
	}
	task.Set("timeline", append(events, taskEvent{Kind: kind, At: at.UTC()}))
	return nil
}

func pruneHistory(app core.App, now time.Time) error {
	cutoff, err := types.ParseDateTime(now.Add(-historyLifetime))
	if err != nil {
		return err
	}
	return app.RunInTransaction(func(tx core.App) error {
		tasks, err := tx.FindRecordsByFilter(tasksCollection, "state = 'confirmed' && confirmed_at != '' && confirmed_at <= {:cutoff}",
			"", 0, 0, dbx.Params{"cutoff": cutoff.String()})
		if err != nil {
			return err
		}
		for _, task := range tasks {
			if err := tx.Delete(task); err != nil {
				return err
			}
		}
		return nil
	})
}

func listMembers(e *core.RequestEvent) error {
	if e.Auth.GetString("role") != "admin" {
		return e.ForbiddenError("", nil)
	}
	records, err := e.App.FindRecordsByFilter(usersCollection, "role = 'member'", "email", 0, 0)
	if err != nil {
		return err
	}
	members := make([]map[string]string, 0, len(records))
	for _, record := range records {
		members = append(members, map[string]string{"id": record.Id, "email": record.Email()})
	}
	return e.JSON(http.StatusOK, members)
}

func createTask(e *core.RequestEvent) error {
	if e.Auth.GetString("role") != "admin" {
		return e.ForbiddenError("", nil)
	}
	var body struct {
		Assignee string `json:"assignee"`
		Flow     string `json:"flow"`
		Email    string `json:"email"`
	}
	if err := e.BindBody(&body); err != nil {
		return e.BadRequestError("", nil)
	}
	body.Email = strings.TrimSpace(body.Email)
	if (body.Flow != "team" && body.Flow != "code") || (body.Email != "" && !validEmail(body.Email)) {
		return e.BadRequestError("", nil)
	}
	var task *core.Record
	err := e.App.RunInTransaction(func(app core.App) error {
		member, err := app.FindRecordById(usersCollection, body.Assignee)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && member.GetString("role") != "member") {
			return e.BadRequestError("", nil)
		}
		if err != nil {
			return err
		}
		task, err = app.FindFirstRecordByFilter(tasksCollection,
			"assignee = {:assignee} && flow = {:flow} && state != 'confirmed'",
			dbx.Params{"assignee": body.Assignee, "flow": body.Flow})
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		collection, err := app.FindCollectionByNameOrId(tasksCollection)
		if err != nil {
			return err
		}
		task = core.NewRecord(collection)
		task.Set("assignee", body.Assignee)
		task.Set("flow", body.Flow)
		task.Set("state", "pending")
		task.Set("email", body.Email)
		task.Set("timeline", []taskEvent{{Kind: "assigned", At: time.Now().UTC()}})
		return app.Save(task)
	})
	if err != nil {
		return err
	}
	return e.JSON(http.StatusOK, task)
}

func reportTask(e *core.RequestEvent) error {
	if e.Auth.GetString("role") != "member" {
		return e.ForbiddenError("", nil)
	}
	var body struct {
		Email   string `json:"email"`
		Region  string `json:"region"`
		IP      string `json:"ip"`
		Outcome string `json:"outcome"`
		Issue   string `json:"issue"`
		Updated string `json:"updated"`
	}
	if err := e.BindBody(&body); err != nil {
		return e.BadRequestError("", nil)
	}
	body.Email = strings.TrimSpace(body.Email)
	body.Region = strings.ToUpper(strings.TrimSpace(body.Region))
	body.IP = strings.TrimSpace(body.IP)
	if !validEmail(body.Email) || (body.Region != "ZZ" && !validRegion(body.Region)) {
		return e.BadRequestError("", nil)
	}
	if body.IP != "" {
		ip, err := netip.ParseAddr(body.IP)
		if err != nil || !publicIP(ip.Unmap()) {
			return e.BadRequestError("", nil)
		}
		body.IP = ip.Unmap().String()
	}
	switch body.Outcome {
	case "submitted":
		body.Issue = ""
	case "issue":
		if !slices.Contains([]string{"invitation", "signin", "access", "other"}, body.Issue) {
			return e.BadRequestError("", nil)
		}
	default:
		return e.BadRequestError("", nil)
	}
	return updateTask(e, body.Updated, func(task *core.Record) (bool, error) {
		state := task.GetString("state")
		if state != "pending" && state != "issue" {
			return false, taskChanged()
		}
		task.Set("email", body.Email)
		task.Set("region", body.Region)
		task.Set("ip", body.IP)
		task.Set("state", body.Outcome)
		task.Set("issue", body.Issue)
		return true, appendEvent(task, body.Outcome, time.Now())
	})
}

func confirmTask(e *core.RequestEvent) error {
	if e.Auth.GetString("role") != "admin" {
		return e.ForbiddenError("", nil)
	}
	var body struct {
		Updated string `json:"updated"`
	}
	if err := e.BindBody(&body); err != nil {
		return e.BadRequestError("", nil)
	}
	return updateTask(e, body.Updated, func(task *core.Record) (bool, error) {
		if task.GetString("state") == "confirmed" {
			return false, nil
		}
		if task.GetString("state") != "submitted" {
			return false, taskChanged()
		}
		now := time.Now()
		task.Set("state", "confirmed")
		task.Set("confirmed_at", now)
		return true, appendEvent(task, "confirmed", now)
	})
}

func renewTask(e *core.RequestEvent) error {
	if e.Auth.GetString("role") != "admin" {
		return e.ForbiddenError("", nil)
	}
	var body struct {
		Updated string `json:"updated"`
		Entry   string `json:"entry"`
	}
	if err := e.BindBody(&body); err != nil {
		return e.BadRequestError("", nil)
	}
	return updateTask(e, body.Updated, func(task *core.Record) (bool, error) {
		if task.GetString("state") == "confirmed" {
			return false, taskChanged()
		}
		flow := task.GetString("flow")
		if (flow == "team" && body.Entry != "team-site" && body.Entry != "team-guide") ||
			(flow == "code" && body.Entry != "code-auth" && body.Entry != "code-setup") {
			return false, e.BadRequestError("", nil)
		}
		task.Set("entry", body.Entry)
		task.Set("state", "pending")
		task.Set("issue", "")
		return true, appendEvent(task, "entryUpdated", time.Now())
	})
}

func taskChanged() error {
	return apis.NewApiError(http.StatusConflict, "Task changed.", nil)
}

func updateTask(e *core.RequestEvent, updated string, change func(*core.Record) (bool, error)) error {
	if updated == "" {
		return e.BadRequestError("", nil)
	}
	var task *core.Record
	err := e.App.RunInTransaction(func(app core.App) error {
		var err error
		task, err = app.FindRecordById(tasksCollection, e.Request.PathValue("id"))
		if errors.Is(err, sql.ErrNoRows) {
			return e.NotFoundError("", nil)
		}
		if err != nil {
			return err
		}
		if e.Auth.GetString("role") == "member" && task.GetString("assignee") != e.Auth.Id {
			return e.NotFoundError("", nil)
		}
		if task.GetString("updated") != updated {
			return taskChanged()
		}
		save, err := change(task)
		if err != nil || !save {
			return err
		}
		return app.Save(task)
	})
	if err != nil {
		return err
	}
	return e.JSON(http.StatusOK, task)
}
