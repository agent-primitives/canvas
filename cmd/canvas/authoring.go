package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/agent-primitives/canvas/internal/authoring"
	"github.com/agent-primitives/canvas/internal/canvas"
	"github.com/agent-primitives/canvas/internal/contract"
)

type commandError struct {
	code    int
	message string
}

func (e *commandError) Error() string { return e.message }

func commandExitCode(err error) int {
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return 0
	}
	var command *commandError
	if errors.As(err, &command) {
		return command.code
	}
	return 1
}

type authoringDiagnostic struct {
	Code    string `json:"code"`
	Path    string `json:"path"`
	Pointer string `json:"pointer"`
	Message string `json:"message"`
}

type authoringResult struct {
	SchemaVersion string                `json:"schemaVersion"`
	Operation     string                `json:"operation"`
	OK            bool                  `json:"ok"`
	Status        string                `json:"status"`
	Files         []string              `json:"files"`
	Diagnostics   []authoringDiagnostic `json:"diagnostics"`
	Help          string                `json:"help,omitempty"`
}

// Detect JSON before parsing so unknown flags still get a machine-readable result.
func requestsJSON(operation string, args []string) bool {
	selected := false
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		name, value, hasValue := strings.Cut(args[i], "=")
		option := strings.TrimPrefix(strings.TrimPrefix(name, "-"), "-")
		if strings.HasPrefix(name, "-") && !hasValue && (option == "root" || operation == "init" && (option == "name" || option == "template")) {
			i++ // FlagSet consumes the next argument as this string option's value.
			continue
		}
		if name != "--json" && name != "-json" {
			continue
		}
		if !hasValue {
			selected = true
			continue
		}
		if value, err := strconv.ParseBool(value); err == nil {
			selected = value
		}
	}
	return selected
}

func authoringCommand(args []string, output io.Writer) error {
	operation := args[0]
	jsonOutput := requestsJSON(operation, args[1:])
	result := authoringResult{SchemaVersion: "canvas.authoring/v1", Operation: operation, Files: []string{}, Diagnostics: []authoringDiagnostic{}}
	emit := func() error {
		if jsonOutput {
			return json.NewEncoder(output).Encode(result)
		}
		if result.Help != "" {
			_, err := io.WriteString(output, result.Help)
			return err
		}
		if !result.OK {
			return nil
		} // main prints safe refusal text on stderr.
		message := "Definition valid (offline); not activated. Validation is not authorization or runtime qualification.\n"
		if operation == "init" {
			message = "Created deterministic fixture package; not started or runtime-qualified. Validate canvas.yaml before deliberate startup.\n"
		}
		_, err := io.WriteString(output, message)
		return err
	}
	refuse := func(status, code, message string, exit int) error {
		result.Status = status
		result.Diagnostics = []authoringDiagnostic{{Code: code, Message: message}}
		if err := emit(); err != nil {
			return err
		}
		return &commandError{code: exit, message: message}
	}
	flags := flag.NewFlagSet("canvas "+operation, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", ".", "init: source checkout; validate: base for relative FILE")
	parsedJSON := flags.Bool("json", false, "emit one canvas.authoring/v1 JSON result")
	var template, name string
	var fixture bool
	if operation == "init" {
		flags.StringVar(&template, "template", "single-review", "single-review or writer-review")
		flags.StringVar(&name, "name", "", "canvas name; defaults to destination basename (lowercase letters/digits/hyphens)")
		flags.BoolVar(&fixture, "fixture", false, "explicitly select deterministic fixture assets; required")
	}
	var help strings.Builder
	flags.Usage = func() {
		fmt.Fprintf(&help, "Usage: canvas %s %s [flags]\n", operation, map[string]string{"init": "DIR --fixture", "validate": "FILE"}[operation])
		flags.SetOutput(&help)
		flags.PrintDefaults()
		flags.SetOutput(io.Discard)
	}
	// Support canonical operand-first and conventional flags-first forms.
	remaining := args[1:]
	operand := ""
	if len(remaining) > 0 && !strings.HasPrefix(remaining[0], "-") {
		operand, remaining = remaining[0], remaining[1:]
	}
	if err := flags.Parse(remaining); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			result.OK, result.Status, result.Help = true, "help", help.String()
			if err := emit(); err != nil {
				return err
			}
			return flag.ErrHelp
		}
		return refuse("usage-error", "usage", "invalid authoring arguments; use canvas "+operation+" --help", 2)
	}
	if operand == "" && flags.NArg() == 1 {
		operand = flags.Arg(0)
	} else if flags.NArg() != 0 {
		return refuse("usage-error", "usage", "supply exactly one directory or definition file", 2)
	}
	if operand == "" || *root == "" {
		return refuse("usage-error", "usage", "a directory or definition file and nonempty root are required", 2)
	}
	jsonOutput = *parsedJSON
	if operation == "init" {
		if !fixture {
			return refuse("usage-error", "fixture-required", "init currently requires explicit --fixture; these templates do not qualify a real model", 2)
		}
		if name == "" {
			name = filepath.Base(filepath.Clean(operand))
		}
		files, err := authoring.Init(*root, operand, template, name)
		if err != nil {
			if errors.Is(err, authoring.ErrOptions) {
				return refuse("usage-error", "template-options", authoring.ErrOptions.Error(), 2)
			}
			return refuse("refused", "init-refused", err.Error(), 1)
		}
		result.OK, result.Status, result.Files = true, "created", files
	} else {
		filename := operand
		if !filepath.IsAbs(filename) {
			filename = filepath.Join(*root, filename)
		}
		if _, err := canvas.LoadGenericDefinition(filename); err != nil {
			if errors.Is(err, contract.ErrReleaseProfile) {
				return refuse("refused", "unsupported-runtime-profile", contract.ErrReleaseProfile.Error(), 1)
			}
			return refuse("invalid", "invalid-definition", "invalid definition or local dependencies; check document references, schemas, seeds and complete suite.layout assets (docs/authoring-cli.md)", 1)
		}
		result.OK, result.Status = true, "valid-offline"
	}
	return emit()
}
