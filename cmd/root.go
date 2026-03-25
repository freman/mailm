package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/freman/mailm/internal/config"
	"github.com/freman/mailm/internal/migrate"
)

var (
	cfgFile string
	flags   struct {
		aliases        []string
		sourceHost     string
		sourcePort     int
		sourceUser     string
		sourcePassword string
		sourceTLS      string
		sourceFolders  []string
		destHost       string
		destPort       int
		destUser       string
		destPassword   string
		destTLS        string
		destFolder     string
		dryRun         bool
		noDryRun       bool
		deleteSource   bool
		since          string
		before         string
		stateFile      string
		logFile        string
		batchSize      int
		dryRunReport   string
		allowInsecure  bool
		retryCount     int
		overwrite      bool
	}
)

var rootCmd = &cobra.Command{
	Use:   "mailm [config-file]",
	Short: "Migrate email addressed to an alias from one mailbox to another",
	Long: `mailm migrates messages from a source IMAP mailbox to a destination IMAP
mailbox, selecting only messages that were addressed to a specific alias.

Example (dry run):
  mailm --alias dmarc.reports@example.com \
        --source-host mail.example.com --source-user user@example.com --source-password $SRC_PASS \
        --dest-host mail.example.com --dest-user dmarc.reports@example.com --dest-password $DST_PASS \
        --dry-run --dry-run-report matched.csv

Example (real run with delete):
  mailm --alias dmarc.reports@example.com \
        --source-host mail.example.com --source-user user@example.com --source-password $SRC_PASS \
        --dest-host mail.example.com --dest-user dmarc.reports@example.com --dest-password $DST_PASS \
        --no-dry-run --delete-source`,
	Args: cobra.MaximumNArgs(1),
	RunE: runMigration,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	f := rootCmd.Flags()

	f.StringVar(&cfgFile, "config", "", "Path to YAML config file")
	f.StringArrayVar(&flags.aliases, "alias", nil, "Alias address to filter on (repeatable for multiple aliases)")
	f.StringVar(&flags.sourceHost, "source-host", "", "IMAP hostname for source mailbox")
	f.IntVar(&flags.sourcePort, "source-port", 0, "IMAP port for source (default 993)")
	f.StringVar(&flags.sourceUser, "source-user", "", "Login user for source mailbox")
	f.StringVar(&flags.sourcePassword, "source-password", "", "Password for source mailbox (or $ENV_VAR)")
	f.StringVar(&flags.sourceTLS, "source-tls", "", "TLS mode for source: ssl, starttls, or none (default: ssl on port 993, starttls otherwise)")
	f.StringArrayVar(&flags.sourceFolders, "source-folder", nil, "Source folders to scan (repeatable; default: all)")
	f.StringVar(&flags.destHost, "dest-host", "", "IMAP hostname for destination mailbox")
	f.IntVar(&flags.destPort, "dest-port", 0, "IMAP port for destination (default 993)")
	f.StringVar(&flags.destUser, "dest-user", "", "Login user for destination mailbox")
	f.StringVar(&flags.destPassword, "dest-password", "", "Password for destination mailbox (or $ENV_VAR)")
	f.StringVar(&flags.destTLS, "dest-tls", "", "TLS mode for dest: ssl, starttls, or none (default: ssl on port 993, starttls otherwise)")
	f.StringVar(&flags.destFolder, "dest-folder", "", "Destination folder override (overrides folder_map and default_folder)")
	f.BoolVar(&flags.dryRun, "dry-run", false, "Enable dry-run mode (default when no config sets it)")
	f.BoolVar(&flags.noDryRun, "no-dry-run", false, "Disable dry-run; actually copy messages")
	f.BoolVar(&flags.deleteSource, "delete-source", false, "Delete matched messages from source after copy")
	f.StringVar(&flags.since, "since", "", "Only migrate messages on/after this date (YYYY-MM-DD)")
	f.StringVar(&flags.before, "before", "", "Only migrate messages before this date (YYYY-MM-DD)")
	f.StringVar(&flags.stateFile, "state-file", "", "Path to SQLite state file (default: migration_state.db)")
	f.StringVar(&flags.logFile, "log-file", "", "Path to log file (default: stdout)")
	f.IntVar(&flags.batchSize, "batch-size", 0, "Messages per IMAP batch (default 50)")
	f.StringVar(&flags.dryRunReport, "dry-run-report", "", "Write CSV of matched messages (dry-run only)")
	f.BoolVar(&flags.allowInsecure, "allow-insecure", false, "Allow plaintext IMAP connections (dangerous)")
	f.IntVar(&flags.retryCount, "retry-count", 0, "Number of retries on transient errors (default 3)")
	f.BoolVar(&flags.overwrite, "overwrite", false, "Re-copy messages already recorded in the state DB (ignores idempotency check)")
}

func runMigration(cmd *cobra.Command, args []string) error {
	// Determine config file path: --config flag, first positional arg, or empty.
	path := cfgFile
	if path == "" && len(args) > 0 {
		path = args[0]
	}

	cfg, err := config.Load(path)
	if err != nil {
		return err
	}

	// Apply CLI flag overrides (non-zero / non-empty values win over config).
	applyFlagOverrides(cfg, cmd)
	cfg.ExpandEnv()

	if err := cfg.Validate(); err != nil {
		return err
	}

	// Print run header.
	mode := "DRY RUN"
	if !cfg.DryRun {
		mode = "LIVE RUN"
	}
	fmt.Printf("\nmailm -- %s\n", mode)
	fmt.Printf("  aliases: %s\n", strings.Join(cfg.Aliases, ", "))
	fmt.Printf("  source:  %s (%s)\n", cfg.Source.User, cfg.Source.Host)
	fmt.Printf("  dest:    %s (%s)\n", cfg.Dest.User, cfg.Dest.Host)
	if cfg.DryRun {
		fmt.Printf("  NOTE: No changes will be made. Use --no-dry-run to migrate for real.\n")
	}
	if cfg.DeleteSource && !cfg.DryRun {
		fmt.Printf("  NOTE: Matched messages will be DELETED from source after copy.\n")
	}
	fmt.Println()

	start := time.Now()
	m, err := migrate.New(cfg)
	if err != nil {
		return fmt.Errorf("initializing migrator: %w", err)
	}
	defer m.Close()

	stats, err := m.Run()
	if err != nil {
		return fmt.Errorf("migration failed: %w", err)
	}

	duration := time.Since(start).Round(time.Second)

	fmt.Printf("\nMigration complete\n")
	fmt.Printf("  Aliases:          %s\n", strings.Join(cfg.Aliases, ", "))
	fmt.Printf("  Source account:   %s\n", cfg.Source.User)
	fmt.Printf("  Dest account:     %s\n", cfg.Dest.User)
	fmt.Printf("  Folders scanned:  %d\n", stats.FoldersScanned)
	fmt.Printf("  Messages scanned: %d\n", stats.MessagesScanned)
	fmt.Printf("  Messages matched: %d\n", stats.MessagesMatched)
	if cfg.DryRun {
		fmt.Printf("  Messages would copy: %d\n", stats.MessagesCopied)
	} else {
		fmt.Printf("  Messages copied:  %d\n", stats.MessagesCopied)
	}
	fmt.Printf("  Messages skipped: %d (already migrated)\n", stats.MessagesSkipped)
	fmt.Printf("  Messages failed:  %d\n", stats.MessagesFailed)
	fmt.Printf("  Source deleted:   %v\n", cfg.DeleteSource && !cfg.DryRun)
	fmt.Printf("  Duration:         %s\n\n", duration)

	if cfg.DryRunReport != "" && cfg.DryRun {
		fmt.Printf("  Dry-run report written to: %s\n\n", cfg.DryRunReport)
	}

	return nil
}

// applyFlagOverrides copies non-zero CLI flag values onto the loaded config.
func applyFlagOverrides(cfg *config.Config, cmd *cobra.Command) {
	f := cmd.Flags()

	if f.Changed("alias") {
		cfg.Aliases = config.StringSlice(flags.aliases)
	}
	if f.Changed("source-host") {
		cfg.Source.Host = flags.sourceHost
	}
	if f.Changed("source-port") {
		cfg.Source.Port = flags.sourcePort
	}
	if f.Changed("source-user") {
		cfg.Source.User = flags.sourceUser
	}
	if f.Changed("source-password") {
		cfg.Source.Password = flags.sourcePassword
	}
	if f.Changed("source-tls") {
		cfg.Source.TLS = flags.sourceTLS
	}
	if f.Changed("source-folder") {
		cfg.Source.Folders = flags.sourceFolders
	}
	if f.Changed("dest-host") {
		cfg.Dest.Host = flags.destHost
	}
	if f.Changed("dest-port") {
		cfg.Dest.Port = flags.destPort
	}
	if f.Changed("dest-user") {
		cfg.Dest.User = flags.destUser
	}
	if f.Changed("dest-password") {
		cfg.Dest.Password = flags.destPassword
	}
	if f.Changed("dest-tls") {
		cfg.Dest.TLS = flags.destTLS
	}
	if f.Changed("dest-folder") {
		cfg.Dest.DefaultFolder = flags.destFolder
		cfg.Dest.FolderMap = nil // override folder_map entirely
	}
	if f.Changed("no-dry-run") && flags.noDryRun {
		cfg.DryRun = false
	}
	if f.Changed("dry-run") && flags.dryRun {
		cfg.DryRun = true
	}
	if f.Changed("delete-source") {
		cfg.DeleteSource = flags.deleteSource
	}
	if f.Changed("since") {
		cfg.Since = flags.since
	}
	if f.Changed("before") {
		cfg.Before = flags.before
	}
	if f.Changed("state-file") {
		cfg.StateFile = flags.stateFile
	}
	if f.Changed("log-file") {
		cfg.LogFile = flags.logFile
	}
	if f.Changed("batch-size") {
		cfg.BatchSize = flags.batchSize
	}
	if f.Changed("dry-run-report") {
		cfg.DryRunReport = flags.dryRunReport
	}
	if f.Changed("allow-insecure") {
		cfg.AllowInsecure = flags.allowInsecure
	}
	if f.Changed("retry-count") {
		cfg.RetryCount = flags.retryCount
	}
	if f.Changed("overwrite") {
		cfg.Overwrite = flags.overwrite
	}
}
