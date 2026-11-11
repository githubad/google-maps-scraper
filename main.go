package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/gosom/google-maps-scraper/runner"
	"github.com/gosom/google-maps-scraper/runner/databaserunner"
	"github.com/gosom/google-maps-scraper/runner/filerunner"
	"github.com/gosom/google-maps-scraper/runner/installplaywright"
	"github.com/gosom/google-maps-scraper/runner/lambdaaws"
	"github.com/gosom/google-maps-scraper/runner/webrunner"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())

	runner.Banner()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan

		log.Println("Received signal, shutting down...")

		cancel()
	}()

	cfg := runner.ParseConfig()

	// Invalid flag combinations and invalid task files exit with code 2
	// before any network activity (docs/contracts.md §1.3).
	if err := runner.ValidateInputConfig(cfg); err != nil {
		exitWith(err, cancel)
	}

	runnerInstance, err := runnerFactory(cfg)
	if err != nil {
		cancel()
		exitIfInputError(err)
		os.Stderr.WriteString(err.Error() + "\n")

		runner.Telemetry().Close()

		os.Exit(exitCodeOf(err))
	}

	if err := runnerInstance.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		os.Stderr.WriteString(err.Error() + "\n")

		_ = runnerInstance.Close(ctx)
		runner.Telemetry().Close()

		cancel()

		exitIfInputError(err)

		os.Exit(exitCodeOf(err))
	}

	_ = runnerInstance.Close(ctx)
	runner.Telemetry().Close()

	cancel()

	os.Exit(0)
}

// exitCodeOf is the process exit code for a runner error: the code of a
// *runner.ExitCodeError (docs/contracts.md §1.3), 1 for anything else.
func exitCodeOf(err error) int {
	var coded *runner.ExitCodeError

	if errors.As(err, &coded) {
		return coded.Code
	}

	return runner.ExitFatal
}

// exitIfInputError prints an *runner.InputError and exits with its code (2).
func exitIfInputError(err error) {
	if code, ok := runner.ReportInputError(os.Stderr, err); ok {
		runner.Telemetry().Close()
		os.Exit(code)
	}
}

// exitWith exits for a validation error: code 2 for input errors, 1 otherwise.
func exitWith(err error, cancel context.CancelFunc) {
	cancel()
	exitIfInputError(err)
	os.Stderr.WriteString(err.Error() + "\n")
	runner.Telemetry().Close()
	os.Exit(1)
}

func runnerFactory(cfg *runner.Config) (runner.Runner, error) {
	switch cfg.RunMode {
	case runner.RunModeFile:
		return filerunner.New(cfg)
	case runner.RunModeDatabase, runner.RunModeDatabaseProduce:
		return databaserunner.New(cfg)
	case runner.RunModeInstallPlaywright:
		return installplaywright.New(cfg)
	case runner.RunModeWeb:
		return webrunner.New(cfg)
	case runner.RunModeAwsLambda:
		return lambdaaws.New(cfg)
	case runner.RunModeAwsLambdaInvoker:
		return lambdaaws.NewInvoker(cfg)
	default:
		return nil, fmt.Errorf("%w: %d", runner.ErrInvalidRunMode, cfg.RunMode)
	}
}
