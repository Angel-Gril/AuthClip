package main

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/mail"
	"os"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

//go:embed web
var web embed.FS

func main() {
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: "pb_data"})
	app.RootCmd.AddCommand(accountCommand(app))
	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		e.InstallerFunc = nil
		geo, err := openGeo(os.Getenv("PORTAL_GEO_DB"), os.Getenv("PORTAL_TRUSTED_PROXY"))
		if err != nil {
			return err
		}
		e.Router.Bind(&hook.Handler[*core.RequestEvent]{
			Priority: apis.DefaultActivityLoggerMiddlewarePriority - 1,
			Func: func(r *core.RequestEvent) error {
				geo.applyTrustedProxy(r.Request)
				return r.Next()
			},
		})
		e.Router.BindFunc(func(r *core.RequestEvent) error {
			r.Response.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
			r.Response.Header().Set("Referrer-Policy", "no-referrer")
			r.Response.Header().Set("X-Content-Type-Options", "nosniff")
			if strings.HasPrefix(r.Request.URL.Path, "/api/") {
				r.Response.Header().Set("Cache-Control", "no-store")
			}
			return r.Next()
		})
		e.Router.GET("/robots.txt", func(r *core.RequestEvent) error {
			return r.String(http.StatusOK, "User-agent: *\nDisallow: /\n")
		})
		e.Router.GET("/api/portal/context", geo.context)
		e.Router.GET("/api/portal/network", geo.network).Bind(apis.RequireAuth(usersCollection))
		e.Router.GET("/api/portal/members", listMembers).Bind(apis.RequireAuth(usersCollection))
		e.Router.POST("/api/portal/tasks", createTask).Bind(apis.RequireAuth(usersCollection))
		e.Router.POST("/api/portal/tasks/{id}/report", reportTask).Bind(apis.RequireAuth(usersCollection))
		e.Router.POST("/api/portal/tasks/{id}/confirm", confirmTask).Bind(apis.RequireAuth(usersCollection))
		e.Router.POST("/api/portal/tasks/{id}/renew", renewTask).Bind(apis.RequireAuth(usersCollection))

		cleanHistory := func() {
			if err := pruneHistory(app, time.Now()); err != nil {
				app.Logger().Error("Unable to prune task history", "error", err)
			}
		}
		cleanHistory()
		if err := app.Cron().Add("portal-history", "*/15 * * * *", cleanHistory); err != nil {
			return err
		}

		site, err := fs.Sub(web, "web")
		if err != nil {
			return err
		}
		e.Router.GET("/{path...}", func(r *core.RequestEvent) error {
			r.Response.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
			return apis.Static(site, false)(r)
		})
		return e.Next()
	})
	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}

func accountCommand(app core.App) *cobra.Command {
	return &cobra.Command{
		Use:   "account EMAIL ROLE",
		Short: "Create a portal account (admin or member)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			email, role := strings.TrimSpace(args[0]), args[1]
			if !validEmail(email) || (role != "admin" && role != "member") {
				return fmt.Errorf("provide a valid email and the role admin or member")
			}
			if err := app.RunAllMigrations(); err != nil {
				return err
			}
			fmt.Fprint(os.Stderr, "Password (at least 12 characters): ")
			password, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			if err != nil {
				return err
			}
			if len(password) < 12 {
				return fmt.Errorf("password must be at least 12 characters")
			}
			record, err := newAccount(app, email, role, string(password))
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Created", record.Email(), "("+role+")")
			return nil
		},
	}
}

func validEmail(email string) bool {
	parsed, err := mail.ParseAddress(email)
	return err == nil && parsed.Address == email && len(email) <= 254
}

func newAccount(app core.App, email, role, password string) (*core.Record, error) {
	collection, err := app.FindCollectionByNameOrId(usersCollection)
	if err != nil {
		return nil, err
	}
	record := core.NewRecord(collection)
	record.SetEmail(email)
	record.SetPassword(password)
	record.Set("role", role)
	return record, app.Save(record)
}
