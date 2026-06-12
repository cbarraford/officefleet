package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cbarraford/office-fleet/internal/config"
	"github.com/cbarraford/office-fleet/internal/domain"
)

func TestBuildSecretLookupPropagatesQueryErrors(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, "postgres://fleet:fleet@127.0.0.1:1/fleet")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()

	lookup := buildSecretLookup(ctx, pool, nil)
	_, err = lookup("gitlab_token")
	if err == nil {
		t.Fatal("expected closed-pool query error to propagate")
	}
	if !strings.Contains(err.Error(), "query secret") {
		t.Fatalf("err = %v, want query-secret context", err)
	}
}

func TestPluginInitErrorForOutputsFailsReferencedPlugin(t *testing.T) {
	err := pluginInitErrorForOutputs(
		[]domain.OutputBinding{{Plugin: "gitlab", Action: "post_mr_comment"}},
		map[string]error{
			"gitlab": errors.New("missing token"),
			"slack":  errors.New("missing token"),
		},
	)
	if err == nil {
		t.Fatal("expected referenced plugin init failure")
	}
	if !strings.Contains(err.Error(), "gitlab") || !strings.Contains(err.Error(), "missing token") {
		t.Fatalf("err = %v, want plugin name and cause", err)
	}
}

func TestPluginInitErrorForOutputsIgnoresUnreferencedPlugin(t *testing.T) {
	err := pluginInitErrorForOutputs(
		[]domain.OutputBinding{{Plugin: "gitlab", Action: "post_mr_comment"}},
		map[string]error{"slack": errors.New("missing token")},
	)
	if err != nil {
		t.Fatalf("err = %v, want nil for unreferenced plugin failure", err)
	}
}

func TestInitPluginsReturnsFailures(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, "postgres://fleet:fleet@127.0.0.1:1/fleet")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()

	failures := initPlugins(ctx, &config.Config{}, pool, nil)
	if len(failures) == 0 {
		t.Fatal("expected plugin initialization failures to be returned")
	}
	if failures["gitlab"] == nil {
		t.Fatalf("failures = %v, want gitlab secret lookup failure", failures)
	}
}
