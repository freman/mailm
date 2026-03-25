package migrate

import (
	"encoding/csv"
	"fmt"
	"net/mail"
	"net/textproto"
	"os"
	"strings"
	"time"

	"github.com/freman/mailm/internal/config"
	"github.com/freman/mailm/internal/imaputil"
	"github.com/freman/mailm/internal/state"
)

// Stats accumulates per-run counters.
type Stats struct {
	FoldersScanned  int
	MessagesScanned int
	MessagesMatched int
	MessagesCopied  int
	MessagesSkipped int
	MessagesFailed  int
}

// Migrator performs the alias migration.
type Migrator struct {
	cfg    *config.Config
	src    *imaputil.Client
	dst    *imaputil.Client
	state  *state.DB
	since  time.Time
	before time.Time

	csvWriter *csv.Writer
	csvFile   *os.File
}

// New creates a Migrator and connects both IMAP accounts.
func New(cfg *config.Config) (*Migrator, error) {
	since, err := cfg.ParseSince()
	if err != nil {
		return nil, err
	}
	before, err := cfg.ParseBefore()
	if err != nil {
		return nil, err
	}

	src, err := imaputil.Dial(cfg.Source.Host, cfg.Source.Port, cfg.Source.TLSMode(), cfg.AllowInsecure)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	if err := src.Login(cfg.Source.User, cfg.Source.Password); err != nil {
		src.Logout()
		return nil, fmt.Errorf("source: %w", err)
	}

	dst, err := imaputil.Dial(cfg.Dest.Host, cfg.Dest.Port, cfg.Dest.TLSMode(), cfg.AllowInsecure)
	if err != nil {
		src.Logout()
		return nil, fmt.Errorf("dest: %w", err)
	}
	if err := dst.Login(cfg.Dest.User, cfg.Dest.Password); err != nil {
		src.Logout()
		dst.Logout()
		return nil, fmt.Errorf("dest: %w", err)
	}

	var stateDB *state.DB
	if !cfg.DryRun {
		stateDB, err = state.Open(cfg.StateFile)
		if err != nil {
			src.Logout()
			dst.Logout()
			return nil, err
		}
	} else {
		// Open read-only state DB for skip detection even in dry-run
		stateDB, err = state.Open(cfg.StateFile)
		if err != nil {
			// Not fatal in dry-run; just don't track state
			stateDB = nil
		}
	}

	m := &Migrator{
		cfg:    cfg,
		src:    src,
		dst:    dst,
		state:  stateDB,
		since:  since,
		before: before,
	}
	return m, nil
}

// Close disconnects both IMAP clients and closes the state DB.
func (m *Migrator) Close() {
	m.src.Logout()
	m.dst.Logout()
	if m.state != nil {
		m.state.Close()
	}
	if m.csvFile != nil {
		m.csvWriter.Flush()
		m.csvFile.Close()
	}
}

// Run executes the migration and returns stats.
func (m *Migrator) Run() (*Stats, error) {
	if err := m.openCSV(); err != nil {
		return nil, err
	}

	// Collect all source folders to process.
	folders, err := m.expandFolders()
	if err != nil {
		return nil, err
	}

	stats := &Stats{}

	for _, srcFolder := range folders {
		destFolder, ok := m.cfg.Dest.MapFolder(srcFolder)
		if !ok {
			fmt.Printf("  [skip]  %s (excluded by folder_map)\n", srcFolder)
			continue
		}

		fmt.Printf("  folder  %s → %s\n", srcFolder, destFolder)
		folderStats, err := m.migrateFolder(srcFolder, destFolder)
		if err != nil {
			fmt.Printf("  ERROR   %s: %v\n", srcFolder, err)
			// Continue with next folder rather than aborting the whole run.
			continue
		}

		stats.FoldersScanned++
		stats.MessagesScanned += folderStats.MessagesScanned
		stats.MessagesMatched += folderStats.MessagesMatched
		stats.MessagesCopied += folderStats.MessagesCopied
		stats.MessagesSkipped += folderStats.MessagesSkipped
		stats.MessagesFailed += folderStats.MessagesFailed
	}

	return stats, nil
}

// migrateFolder handles one source->dest folder pair.
func (m *Migrator) migrateFolder(srcFolder, destFolder string) (*Stats, error) {
	uidValidity, err := m.src.SelectFolder(srcFolder, !m.cfg.DeleteSource)
	if err != nil {
		return nil, err
	}

	uids, err := m.src.SearchUIDs(m.since, m.before)
	if err != nil {
		return nil, err
	}

	stats := &Stats{MessagesScanned: len(uids)}

	if len(uids) == 0 {
		fmt.Printf("          (empty)\n")
		return stats, nil
	}

	// Ensure dest folder exists before we start copying.
	if !m.cfg.DryRun && m.cfg.Dest.AutoCreate {
		if err := m.dst.EnsureFolder(destFolder); err != nil {
			return nil, fmt.Errorf("ensuring dest folder %q: %w", destFolder, err)
		}
	}

	// Process in batches.
	var toDelete []uint32
	for i := 0; i < len(uids); i += m.cfg.BatchSize {
		end := i + m.cfg.BatchSize
		if end > len(uids) {
			end = len(uids)
		}
		batch := uids[i:end]

		batchStats, deletable, err := m.processBatch(srcFolder, destFolder, uidValidity, batch)
		if err != nil {
			return stats, fmt.Errorf("batch at offset %d: %w", i, err)
		}
		toDelete = append(toDelete, deletable...)

		stats.MessagesMatched += batchStats.MessagesMatched
		stats.MessagesCopied += batchStats.MessagesCopied
		stats.MessagesSkipped += batchStats.MessagesSkipped
		stats.MessagesFailed += batchStats.MessagesFailed

		fmt.Printf("          scanned %d/%d  matched %d  copied %d  skipped %d  failed %d\r",
			min(i+m.cfg.BatchSize, len(uids)), len(uids),
			stats.MessagesMatched, stats.MessagesCopied, stats.MessagesSkipped, stats.MessagesFailed)
	}
	fmt.Println() // newline after \r progress

	// Delete from source after all copies for this folder are done.
	if m.cfg.DeleteSource && !m.cfg.DryRun && len(toDelete) > 0 {
		if err := m.src.MarkDeleted(toDelete); err != nil {
			fmt.Printf("  WARNING: marking deleted on source: %v\n", err)
		} else if err := m.src.Expunge(); err != nil {
			fmt.Printf("  WARNING: expunge on source: %v\n", err)
		} else {
			for _, uid := range toDelete {
				_ = m.state.MarkDeleted(srcFolder, uid, uidValidity)
			}
		}
	}

	return stats, nil
}

// processBatch fetches headers for a batch of UIDs, filters by alias, then copies matching messages.
// Returns per-batch stats and the UIDs that were successfully copied (eligible for deletion).
func (m *Migrator) processBatch(srcFolder, destFolder string, uidValidity uint32, uids []uint32) (*Stats, []uint32, error) {
	stats := &Stats{}

	headers, err := m.src.FetchHeaders(uids)
	if err != nil {
		return stats, nil, err
	}

	// Filter to messages addressed to the alias.
	var matched []uint32
	for _, mh := range headers {
		if m.headersMatchAlias(mh.Headers) {
			matched = append(matched, mh.UID)
		}
	}
	stats.MessagesMatched = len(matched)

	if len(matched) == 0 {
		return stats, nil, nil
	}

	// Separate into: already migrated (skip) vs. to copy.
	var toCopy []uint32
	for _, uid := range matched {
		if m.state != nil && !m.cfg.Overwrite {
			already, err := m.state.AlreadyMigrated(srcFolder, uid, uidValidity)
			if err == nil && already {
				stats.MessagesSkipped++
				continue
			}
		}
		toCopy = append(toCopy, uid)
	}

	if len(toCopy) == 0 {
		return stats, nil, nil
	}

	// Dry run: just record to CSV.
	if m.cfg.DryRun {
		for _, uid := range toCopy {
			m.writeCSVRow(srcFolder, destFolder, uid, headers)
		}
		stats.MessagesCopied = len(toCopy) // "would copy"
		return stats, nil, nil
	}

	// Real run: fetch full messages and APPEND.
	fullMsgs, err := m.src.FetchFull(toCopy)
	if err != nil {
		return stats, nil, err
	}

	var copied []uint32
	for _, fm := range fullMsgs {
		if len(fm.Body) == 0 {
			stats.MessagesFailed++
			continue
		}
		if err := m.dst.Append(destFolder, fm.Flags, fm.InternalDate, fm.Body); err != nil {
			fmt.Printf("\n  FAIL    UID %d: %v\n", fm.UID, err)
			stats.MessagesFailed++
			continue
		}
		if m.state != nil {
			_ = m.state.RecordMigration(srcFolder, fm.UID, uidValidity, destFolder)
		}
		copied = append(copied, fm.UID)
		stats.MessagesCopied++
	}

	return stats, copied, nil
}

// headersMatchAlias returns true if any of the relevant header fields contain any configured alias.
func (m *Migrator) headersMatchAlias(headers map[string][]string) bool {
	for _, field := range []string{"To", "Cc", "Delivered-To", "X-Original-To", "Envelope-To", "X-Forwarded-To"} {
		for _, val := range headers[textprotoKey(field)] {
			for _, alias := range m.cfg.Aliases {
				if addressMatchesAlias(val, strings.ToLower(alias)) {
					return true
				}
			}
		}
	}
	return false
}

// addressMatchesAlias checks if a header value contains the alias, either as a
// parsed RFC 5322 address or as a plain substring (fallback).
func addressMatchesAlias(headerVal, alias string) bool {
	addrs, err := mail.ParseAddressList(headerVal)
	if err != nil {
		// Fall back to case-insensitive substring match.
		return strings.Contains(strings.ToLower(headerVal), alias)
	}
	for _, addr := range addrs {
		if strings.EqualFold(addr.Address, alias) {
			return true
		}
	}
	return false
}

// textprotoKey returns the canonical MIME header key as stored by net/textproto.
func textprotoKey(s string) string {
	return textproto.CanonicalMIMEHeaderKey(s)
}

// expandFolders resolves all folder patterns from source.folders via IMAP LIST.
func (m *Migrator) expandFolders() ([]string, error) {
	seen := make(map[string]bool)
	var result []string
	for _, pattern := range m.cfg.Source.Folders {
		folders, err := m.src.ListFolders(pattern)
		if err != nil {
			return nil, fmt.Errorf("listing folders for pattern %q: %w", pattern, err)
		}
		for _, f := range folders {
			if !seen[f] {
				seen[f] = true
				result = append(result, f)
			}
		}
	}
	return result, nil
}

// openCSV sets up the dry-run CSV report file if configured.
func (m *Migrator) openCSV() error {
	if !m.cfg.DryRun || m.cfg.DryRunReport == "" {
		return nil
	}
	f, err := os.Create(m.cfg.DryRunReport)
	if err != nil {
		return fmt.Errorf("creating dry-run report %q: %w", m.cfg.DryRunReport, err)
	}
	m.csvFile = f
	m.csvWriter = csv.NewWriter(f)
	_ = m.csvWriter.Write([]string{"source_folder", "dest_folder", "uid", "subject", "from", "date"})
	return nil
}

// writeCSVRow writes one row to the dry-run CSV report.
func (m *Migrator) writeCSVRow(srcFolder, destFolder string, uid uint32, headers []*imaputil.MessageHeader) {
	if m.csvWriter == nil {
		return
	}
	var subject, from, date string
	for _, mh := range headers {
		if mh.UID == uid {
			subject = mh.Headers.Get("Subject")
			from = mh.Headers.Get("From")
			date = mh.Headers.Get("Date")
			break
		}
	}
	_ = m.csvWriter.Write([]string{
		srcFolder, destFolder, fmt.Sprintf("%d", uid), subject, from, date,
	})
}
