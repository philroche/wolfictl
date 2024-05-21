package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/chainguard-dev/clog"
	"github.com/wolfi-dev/wolfictl/pkg/cli"
)

func main() {
	ctx := context.Background()
	if err := mainE(ctx); err != nil {
		clog.FromContext(ctx).Fatal(err.Error())
	}
}

func mainE(ctx context.Context) error {
	ctx, done := signal.NotifyContext(ctx, os.Interrupt)
	defer done()

	return cli.New().ExecuteContext(ctx)
}
