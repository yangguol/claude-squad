package main

import (
	"claude-squad/app"
	cmd2 "claude-squad/cmd"
	"claude-squad/config"
	"claude-squad/daemon"
	"claude-squad/log"
	"claude-squad/session"
	"claude-squad/session/git"
	"claude-squad/session/tmux"
	"claude-squad/session/workspace"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var (
	version     = "1.0.20"
	programFlag string
	autoYesFlag bool
	daemonFlag  bool
	binName     string
	rootCmd     = &cobra.Command{
		Use:   "claude-squad",
		Short: "Claude Squad - Manage multiple AI agents like Claude Code, Aider, Codex, and Amp.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			log.Initialize(daemonFlag)
			defer log.Close()

			if daemonFlag {
				cfg := config.LoadConfig()
				err := daemon.RunDaemon(cfg)
				log.ErrorLog.Printf("failed to start daemon %v", err)
				return err
			}

			currentDir, err := filepath.Abs(".")
			if err != nil {
				return fmt.Errorf("failed to get current directory: %w", err)
			}
			if err := validateWorkingDirectory(currentDir); err != nil {
				return err
			}

			cfg := config.LoadConfig()

			// Program flag overrides config
			program := cfg.GetProgram()
			if programFlag != "" {
				program = programFlag
			}
			// AutoYes flag overrides config
			autoYes := cfg.AutoYes
			if autoYesFlag {
				autoYes = true
			}
			if autoYes {
				defer func() {
					if err := daemon.LaunchDaemon(); err != nil {
						log.ErrorLog.Printf("failed to launch daemon: %v", err)
					}
				}()
			}
			// Kill any daemon that's running.
			if err := daemon.StopDaemon(); err != nil {
				log.ErrorLog.Printf("failed to stop daemon: %v", err)
			}

			return app.Run(ctx, program, autoYes)
		},
	}

	resetCmd = &cobra.Command{
		Use:   "reset",
		Short: "Reset all stored instances",
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(false)
			defer log.Close()

			state := config.LoadState()
			storage, err := session.NewStorage(state)
			if err != nil {
				return fmt.Errorf("failed to initialize storage: %w", err)
			}
			return runReset(
				storage.DeleteAllInstances,
				func() error { return tmux.CleanupSessions(cmd2.MakeExecutor()) },
				git.CleanupWorktrees,
				daemon.StopDaemon,
			)

		},
	}

	debugCmd = &cobra.Command{
		Use:   "debug",
		Short: "Print debug information like config paths",
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(false)
			defer log.Close()

			cfg := config.LoadConfig()

			configDir, err := config.GetConfigDir()
			if err != nil {
				return fmt.Errorf("failed to get config directory: %w", err)
			}
			configJson, _ := json.MarshalIndent(cfg, "", "  ")

			fmt.Printf("Config: %s\n%s\n", filepath.Join(configDir, config.ConfigFileName), configJson)

			return nil
		},
	}

	versionCmd = &cobra.Command{
		Use:   "version",
		Short: "Print the version number",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("%s version %s\n", binName, version)
			fmt.Printf("https://github.com/smtg-ai/claude-squad/releases/tag/v%s\n", version)
		},
	}
)

func runReset(deleteStorage, cleanupTmux, cleanupWorktrees, stopDaemon func() error) error {
	return workspace.WithReset(func(cleanupShadowProjects func() error) error {
		return runResetSequence(deleteStorage, cleanupTmux, cleanupWorktrees, cleanupShadowProjects, stopDaemon)
	})
}

func runResetSequence(deleteStorage, cleanupTmux, cleanupWorktrees, cleanupShadowProjects, stopDaemon func() error) error {
	return resetAll(func() error { return nil }, deleteStorage, cleanupTmux, cleanupWorktrees, cleanupShadowProjects, stopDaemon)
}

func resetAll(validateReferences, deleteStorage, cleanupTmux, cleanupWorktrees, cleanupShadowProjects, stopDaemon func() error) error {
	steps := []struct {
		run     func() error
		failure string
		success string
	}{
		{validateReferences, "failed to validate persisted shadow references", "Persisted shadow references have been validated"},
		{deleteStorage, "failed to reset storage", "Storage has been reset successfully"},
		{cleanupTmux, "failed to cleanup tmux sessions", "Tmux sessions have been cleaned up"},
		{cleanupWorktrees, "failed to cleanup worktrees", "Worktrees have been cleaned up"},
		{cleanupShadowProjects, "failed to cleanup shadow projects", "Shadow projects have been cleaned up"},
		{stopDaemon, "failed to stop daemon", "daemon has been stopped"},
	}
	for _, step := range steps {
		if err := step.run(); err != nil {
			return fmt.Errorf("%s: %w", step.failure, err)
		}
		fmt.Println(step.success)
	}
	return nil
}

func validateWorkingDirectory(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("failed to access working directory %q: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("working directory %q is not a directory", path)
	}
	return nil
}

func init() {
	rootCmd.Flags().StringVarP(&programFlag, "program", "p", "",
		"Program to run in new instances (e.g. 'aider --model ollama_chat/gemma3:1b')")
	rootCmd.Flags().BoolVarP(&autoYesFlag, "autoyes", "y", false,
		"[experimental] If enabled, all instances will automatically accept prompts")
	rootCmd.Flags().BoolVar(&daemonFlag, "daemon", false, "Run a program that loads all sessions"+
		" and runs autoyes mode on them.")

	// Hide the daemonFlag as it's only for internal use
	err := rootCmd.Flags().MarkHidden("daemon")
	if err != nil {
		panic(err)
	}

	rootCmd.AddCommand(debugCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(resetCmd)
}

func main() {
	// Extract the binary name from how this was invoked
	binName = filepath.Base(os.Args[0])
	rootCmd.Use = binName

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
	}
}
