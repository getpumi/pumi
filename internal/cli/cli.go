// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"getpumi.dev/pumi/api/gen/pumi/controlplane/v1/controlplanev1connect"
	"getpumi.dev/pumi/internal/shared"
)

// Env is what the wiring of cmd/pumi gives the commands: where they write,
// how they reach the control plane as the person, and how pumi run runs
// embedded mode, which wires every component (decision 0188).
type Env struct {
	// Stdout receives the output of the commands, and Stderr the usage.
	Stdout, Stderr io.Writer
	// Connect returns the person's session with the control plane of the
	// clone: through the socket of the pumi run that holds the clone's
	// log, or, when none serves it, on the log opened in this process with
	// the configuration file config, which is then required (decision
	// 0206). config may be empty.
	Connect func(ctx context.Context, config string) (*Session, error)
	// Run runs embedded mode with the configuration file config until ctx
	// is done, writing the runner's output to out.
	Run func(ctx context.Context, config string, out io.Writer) error
}

// Session is the person's session with the control plane.
type Session struct {
	// Client is the person's client of the control plane.
	Client controlplanev1connect.ControlPlaneServiceClient
	// View is the person's client of ViewService, with which the commands
	// read an invocation's log and the workflow that the kernel runs.
	View controlplanev1connect.ViewServiceClient
	// Person is the person's principal, the clone's git user.email
	// (decision 0208).
	Person string
	// Close ends the session.
	Close func() error
}

// usage lists the commands.
const usage = `Usage: pumi <command> [flags] [arguments]

Commands:
  run --config <file>                  run the clone's plans until stopped
  status [--config <file>]             show each plan's track, state and open escalation,
                                       and each model role on its fallback
  add [--config <file>] --track <track> [--depends-on <plan>]... <plan>
                                       add a plan to a track
  cancel [--config <file>] <plan>      cancel a plan
  answer [--config <file>] <plan> <answer>...
                                       answer a plan's open escalation
  log [--config <file>] <invocation>   show an invocation's log: its stored log,
                                       or the recent output of a running one
  role reset [--config <file>] <role>  switch a model role back from its fallback
  version                              show pumi's version

Every command but run and version reaches the control plane of the pumi run
that holds the clone's log. When none runs, it opens the log itself, which
needs --config.
`

// errUsage is the error of a command line that the commands do not take.
var errUsage = errors.New("pumi: see the usage above")

// Main runs the command that args, the arguments after the program's name,
// name.
func Main(ctx context.Context, args []string, env Env) error {
	if len(args) == 0 {
		_, _ = io.WriteString(env.Stderr, usage)
		return errUsage
	}
	cmd, ok := commands[args[0]]
	if !ok {
		_, _ = fmt.Fprintf(env.Stderr, "pumi: no command %q\n\n%s", args[0], usage)
		return errUsage
	}
	return cmd(ctx, args[0], args[1:], env)
}

// commands are the commands by name.
var commands = map[string]func(ctx context.Context, name string, args []string, env Env) error{
	"version": func(_ context.Context, name string, args []string, env Env) error {
		if _, err := parse(name, args, env, 0, nil); err != nil {
			return err
		}
		return shared.WriteVersion(env.Stdout, "pumi")
	},
	"run": func(ctx context.Context, name string, args []string, env Env) error {
		var config string
		if _, err := parse(name, args, env, 0, func(fs *flag.FlagSet) {
			fs.StringVar(&config, "config", "", "the configuration file (required)")
		}); err != nil {
			return err
		}
		if config == "" {
			return errors.New("pumi run: --config names no configuration file (decision 0207)")
		}
		return env.Run(ctx, config, env.Stdout)
	},
	"status": personCommand(0, nil, status),
	"add": func(ctx context.Context, name string, args []string, env Env) error {
		var a addFlags
		return personCommand(1, a.define, a.add)(ctx, name, args, env)
	},
	"cancel": personCommand(1, nil, cancelPlan),
	"answer": personCommand(-2, nil, answer),
	"role": func(ctx context.Context, name string, args []string, env Env) error {
		if len(args) == 0 || args[0] != "reset" {
			_, _ = io.WriteString(env.Stderr, usage)
			return fmt.Errorf("pumi %s takes the command reset: %w", name, errUsage)
		}
		return personCommand(1, nil, resetRole)(ctx, name+" reset", args[1:], env)
	},
	"log": func(ctx context.Context, name string, args []string, env Env) error {
		return personCommand(1, nil, func(ctx context.Context, s *Session, args []string, w io.Writer) error {
			return printLog(ctx, s, args[0], w, env.Stderr)
		})(ctx, name, args, env)
	},
}

// parse parses the flags of the command name from args, with the flags
// that define adds, and returns the arguments after them. n is how many
// arguments the command takes, and -n at least n.
func parse(name string, args []string, env Env, n int, define func(fs *flag.FlagSet)) ([]string, error) {
	fs := flag.NewFlagSet("pumi "+name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	if define != nil {
		define(fs)
	}
	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("pumi %s: %w", name, err)
	}
	rest := fs.Args()
	switch {
	case n >= 0 && len(rest) != n:
		return nil, fmt.Errorf("pumi %s takes %d arguments, not %d: %s", name, n, len(rest), strings.Join(rest, " "))
	case n < 0 && len(rest) < -n:
		return nil, fmt.Errorf("pumi %s takes at least %d arguments, not %d", name, -n, len(rest))
	}
	return rest, nil
}

// personCommand returns a command that takes n arguments, as parse counts
// them, and the flags that define adds beside --config, and runs do with
// the person's session.
func personCommand(n int, define func(fs *flag.FlagSet),
	do func(ctx context.Context, s *Session, args []string, w io.Writer) error,
) func(ctx context.Context, name string, args []string, env Env) error {
	return func(ctx context.Context, name string, args []string, env Env) error {
		var config string
		rest, err := parse(name, args, env, n, func(fs *flag.FlagSet) {
			fs.StringVar(&config, "config", "", "the configuration file, needed when no pumi run runs")
			if define != nil {
				define(fs)
			}
		})
		if err != nil {
			return err
		}
		s, err := env.Connect(ctx, config)
		if err != nil {
			return fmt.Errorf("pumi %s: %w", name, err)
		}
		err = do(ctx, s, rest, env.Stdout)
		if cerr := s.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("pumi %s: %w", name, err)
		}
		return nil
	}
}
