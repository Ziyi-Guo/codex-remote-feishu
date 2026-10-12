// Package modelconfig implements explicit validation and application of model settings.
package modelconfig

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/kxn/codex-remote-feishu/internal/config"
)

const usage = "Usage: codex-remote config <check|apply> [--config PATH] [--admin-url URL (apply only)]\n"

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	if args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		_, err := io.WriteString(stdout, usage)
		return err
	}
	action := args[0]
	if action != "check" && action != "apply" {
		return fmt.Errorf("unknown config command %q; expected check or apply", action)
	}
	flags := flag.NewFlagSet("config "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("config", config.DefaultConfigPath(), "existing config JSON path")
	adminURL := flags.String("admin-url", "", "local daemon admin URL (apply only)")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments; use --config PATH")
	}
	if action == "check" && *adminURL != "" {
		return errors.New("--admin-url is only supported by config apply")
	}
	resolved, err := filepath.Abs(*path)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return fmt.Errorf("config file must exist: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("read config file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("config path must be a regular file")
	}
	loaded, err := config.LoadAppConfigAtPath(resolved)
	if err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	if action == "check" {
		_, _ = fmt.Fprintf(stdout, "Config valid: %s\n", resolved)
		printSummary(stdout, loaded.Config.Codex)
		_, err = io.WriteString(stdout, "Model availability: unverified (offline check)\n")
		return err
	}
	endpoint, err := applyURL(*adminURL, loaded.Config.Admin)
	if err != nil {
		return err
	}
	result, err := apply(ctx, endpoint, resolved)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "Model settings applied: %s\nDefault model: %q; reasoning effort: %q\nModel availability: %s\n", result.ConfigPath, result.Model, result.ReasoningEffort, result.Validation)
	if result.Message != "" {
		_, _ = fmt.Fprintf(stdout, "%s\n", result.Message)
	}
	return nil
}

func printSummary(out io.Writer, settings config.CodexSettings) {
	_, _ = fmt.Fprintf(out, "Default model: %q; reasoning effort: %q\n", settings.DefaultModel, settings.DefaultReasoningEffort)
	presets := config.EffectiveCodexModelPresets(settings.ModelPresets)
	aliases := make([]string, 0, len(presets))
	for alias := range presets {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	if len(aliases) == 0 {
		_, _ = io.WriteString(out, "Model presets: disabled\n")
	}
	for _, alias := range aliases {
		preset := presets[alias]
		_, _ = fmt.Fprintf(out, "Preset [%s]: model %q; reasoning effort %q\n", alias, preset.Model, preset.ReasoningEffort)
	}
}
