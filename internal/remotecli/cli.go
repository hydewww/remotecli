package remotecli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// Run is the single process entry point. Only config and serve are local
// commands. Every other argv token is preserved and sent to OpenCLI.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "config":
			return runConfig(args[1:], stdout, stderr)
		case "serve":
			return runServe(args[1:], stdout, stderr)
		}
	}
	return runRemote(args, stdout, stderr)
}

func runConfig(args []string, stdout, stderr io.Writer) int {
	var show, clear, help bool
	var endpoint string
	var token, tokenFile string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--show":
			show = true
		case "--clear":
			clear = true
		case "-h", "--help":
			help = true
		case "--token", "-t":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "remotecli config: --token requires a value")
				return clientExitUsage
			}
			i++
			token = args[i]
		case "--token-file":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "remotecli config: --token-file requires a path")
				return clientExitUsage
			}
			i++
			tokenFile = args[i]
		default:
			if strings.HasPrefix(arg, "-") {
				fmt.Fprintf(stderr, "remotecli config: unknown option %s\n", arg)
				return clientExitUsage
			}
			if endpoint != "" {
				fmt.Fprintln(stderr, "remotecli config: only one endpoint is allowed")
				return clientExitUsage
			}
			endpoint = arg
		}
	}
	if help {
		printConfigHelp(stdout)
		return 0
	}
	if show {
		if endpoint != "" || token != "" || tokenFile != "" || clear {
			fmt.Fprintln(stderr, "remotecli config: --show cannot be combined with other options")
			return clientExitUsage
		}
		cfg, err := loadConfig()
		if err != nil {
			fmt.Fprintln(stderr, "remotecli config:", err)
			return clientExitGeneric
		}
		_, _ = io.WriteString(stdout, configJSONForDisplay(cfg))
		return 0
	}
	if clear {
		if endpoint != "" || token != "" || tokenFile != "" {
			fmt.Fprintln(stderr, "remotecli config: --clear cannot be combined with other options")
			return clientExitUsage
		}
		if err := deleteConfig(); err != nil {
			fmt.Fprintln(stderr, "remotecli config:", err)
			return clientExitGeneric
		}
		fmt.Fprintln(stdout, "remotecli configuration cleared")
		return 0
	}
	if endpoint == "" {
		fmt.Fprintln(stderr, "remotecli config: endpoint is required")
		printConfigHelp(stderr)
		return clientExitUsage
	}
	normalized, err := normalizeEndpoint(endpoint)
	if err != nil {
		fmt.Fprintln(stderr, "remotecli config:", err)
		return clientExitUsage
	}
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(stderr, "remotecli config:", err)
		return clientExitGeneric
	}
	cfg.Endpoint = normalized
	if token != "" && tokenFile != "" {
		fmt.Fprintln(stderr, "remotecli config: use only one of --token and --token-file")
		return clientExitUsage
	}
	if tokenFile != "" {
		cfg.Token, err = readTokenFile(tokenFile)
		if err != nil {
			fmt.Fprintln(stderr, "remotecli config:", err)
			return clientExitUsage
		}
	} else if token != "" {
		cfg.Token = token
	}
	if err := saveConfig(cfg); err != nil {
		fmt.Fprintln(stderr, "remotecli config:", err)
		return clientExitGeneric
	}
	fmt.Fprintln(stdout, "remotecli endpoint configured:", normalized)
	return 0
}

func printConfigHelp(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Usage:")
	_, _ = fmt.Fprintln(w, "  remotecli config <endpoint> [--token <token> | --token-file <path>]")
	_, _ = fmt.Fprintln(w, "  remotecli config --show")
	_, _ = fmt.Fprintln(w, "  remotecli config --clear")
}

func runServe(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("remotecli serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	bind := flags.String("bind", "127.0.0.1", "address to listen on")
	port := flags.Int("port", DefaultPort, "TCP port")
	token := flags.String("token", "", "Bearer token (prefer --token-file or REMOTECLI_API_TOKEN)")
	tokenFile := flags.String("token-file", "", "file containing the Bearer token")
	opencli := flags.String("opencli-bin", "", "path or PATH name of the local opencli executable")
	runRoot := flags.String("run-root", "", "directory for per-request workspaces and artifacts")
	concurrency := flags.Int("concurrency", DefaultConcurrency, "maximum concurrent OpenCLI processes")
	commandTimeout := flags.Duration("command-timeout", DefaultCommandTimeout, "maximum duration per command")
	retention := flags.Duration("retention", DefaultRetention, "artifact retention duration")
	maxOutput := flags.Int("max-output", DefaultMaxOutput, "maximum captured bytes per stdout/stderr stream")
	maxArtifactSize := flags.Int64("max-artifact-size", DefaultMaxArtifactSize, "maximum size of one artifact")
	maxArtifactTotal := flags.Int64("max-artifacts-total", DefaultMaxArtifactTotal, "maximum total size of one request's artifacts")
	flags.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: remotecli serve [options]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return clientExitUsage
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "remotecli serve: unexpected arguments:", strings.Join(flags.Args(), " "))
		return clientExitUsage
	}
	if *port < 1 || *port > 65535 || *concurrency < 1 || *commandTimeout <= 0 || *retention <= 0 {
		fmt.Fprintln(stderr, "remotecli serve: invalid port, concurrency, timeout, or retention")
		return clientExitUsage
	}
	serverToken, err := readServerToken(*token, *tokenFile)
	if err != nil {
		fmt.Fprintln(stderr, "remotecli serve:", err)
		return clientExitUsage
	}
	server, err := NewServer(ServerOptions{
		Bind:             *bind,
		Port:             *port,
		Token:            serverToken,
		OpenCLIPath:      *opencli,
		RunRoot:          *runRoot,
		Concurrency:      *concurrency,
		CommandTimeout:   *commandTimeout,
		Retention:        *retention,
		MaxOutput:        *maxOutput,
		MaxArtifactSize:  *maxArtifactSize,
		MaxArtifactTotal: *maxArtifactTotal,
	})
	if err != nil {
		fmt.Fprintln(stderr, "remotecli serve:", err)
		return clientExitServiceUnavailable
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(stdout, "remotecli listening on http://%s:%d; opencli=%s\n", *bind, *port, server.runner.OpenCLIPath())
	if err := server.ListenAndServe(ctx); err != nil {
		fmt.Fprintln(stderr, "remotecli serve:", err)
		return clientExitServiceUnavailable
	}
	return 0
}
