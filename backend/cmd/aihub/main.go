package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"aihub-cloud/backend/internal/api"
	"aihub-cloud/backend/internal/worker"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		if password := os.Getenv("DATABASE_PASSWORD"); password != "" {
			dsn = (&url.URL{
				Scheme:   "postgres",
				User:     url.UserPassword("aihub", password),
				Host:     "db:5432",
				Path:     "/aihub",
				RawQuery: "sslmode=disable",
			}).String()
		} else {
			dsn = "postgres://aihub:aihub@db:5432/aihub?sslmode=disable"
		}
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(30)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for {
		if err = db.PingContext(ctx); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			log.Fatal("database unavailable")
		case <-time.After(2 * time.Second):
		}
	}
	if env("MODE", "api") == "worker" {
		if err := worker.Run(ctx, db); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err = api.Migrate(ctx, db); err != nil {
		log.Fatal(err)
	}
	if err = api.BootstrapAdmin(ctx, db, env("ADMIN_EMAIL", "admin@aihub.local"), env("ADMIN_PASSWORD", "")); err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: env("HTTP_ADDR", ":8080"), Handler: api.New(db), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 0, IdleTimeout: 60 * time.Second}
	go func() {
		log.Printf("AIHub API listening on %s", server.Addr)
		if e := server.ListenAndServe(); e != nil && e != http.ErrServerClosed {
			log.Fatal(e)
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdown)
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
