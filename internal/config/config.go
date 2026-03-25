package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// TLS mode values.
const (
	TLSModeSSL      = "ssl"      // Direct TLS connection (IMAPS, typically port 993)
	TLSModeSTARTTLS = "starttls" // Plain connection upgraded via STARTTLS (typically port 143)
	TLSModeNone     = "none"     // No TLS - requires allow_insecure: true
)

// SourceConfig holds IMAP connection and folder settings for the source mailbox.
type SourceConfig struct {
	Host     string   `yaml:"host"`
	Port     int      `yaml:"port"`
	TLS      string   `yaml:"tls"` // "ssl", "starttls", or "none"; default derived from port
	User     string   `yaml:"user"`
	Password string   `yaml:"password"`
	Folders  []string `yaml:"folders"` // IMAP LIST patterns; "*" = all
}

// DestConfig holds IMAP connection and folder mapping for the destination mailbox.
type DestConfig struct {
	Host          string             `yaml:"host"`
	Port          int                `yaml:"port"`
	TLS           string             `yaml:"tls"` // "ssl", "starttls", or "none"; default derived from port
	User          string             `yaml:"user"`
	Password      string             `yaml:"password"`
	DefaultFolder string             `yaml:"default_folder"`
	FolderMap     map[string]*string `yaml:"folder_map"` // nil value = skip folder
	AutoCreate    bool               `yaml:"auto_create_folders"`
}

// StringSlice unmarshals from either a YAML scalar string or a YAML sequence of strings.
// This lets `alias` be written as a bare string in existing configs and as a list in new ones.
type StringSlice []string

func (s *StringSlice) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		*s = StringSlice{value.Value}
		return nil
	case yaml.SequenceNode:
		var strs []string
		if err := value.Decode(&strs); err != nil {
			return err
		}
		*s = strs
		return nil
	default:
		return fmt.Errorf("alias must be a string or a list of strings")
	}
}

// Config is the top-level configuration.
type Config struct {
	Aliases       StringSlice  `yaml:"alias"`
	Source        SourceConfig `yaml:"source"`
	Dest          DestConfig   `yaml:"dest"`
	DryRun        bool         `yaml:"dry_run"`
	DeleteSource  bool         `yaml:"delete_source"`
	BatchSize     int          `yaml:"batch_size"`
	StateFile     string       `yaml:"state_file"`
	LogFile       string       `yaml:"log_file"`
	DryRunReport  string       `yaml:"dry_run_report"`
	Since         string       `yaml:"since"`  // YYYY-MM-DD or RFC2822
	Before        string       `yaml:"before"` // YYYY-MM-DD or RFC2822
	RetryCount    int          `yaml:"retry_count"`
	AllowInsecure bool         `yaml:"allow_insecure"`
	Overwrite     bool         `yaml:"overwrite"` // re-copy messages already recorded in the state DB
}

// Default returns a Config with sensible defaults.
func Default() *Config {
	return &Config{
		Source: SourceConfig{
			Port:    993,
			Folders: []string{"*"},
		},
		Dest: DestConfig{
			Port:          993,
			DefaultFolder: "INBOX",
			AutoCreate:    true,
		},
		DryRun:     true,
		BatchSize:  50,
		StateFile:  "migration_state.db",
		RetryCount: 3,
	}
}

// Load reads a YAML config file (path may be empty for defaults only).
func Load(path string) (*Config, error) {
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading config file: %w", err)
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parsing config file: %w", err)
		}
	}
	return cfg, nil
}

// ExpandEnv replaces $VAR references in password and other fields with environment variable values.
func (c *Config) ExpandEnv() {
	for i, a := range c.Aliases {
		c.Aliases[i] = expandEnv(a)
	}
	c.Source.Host = expandEnv(c.Source.Host)
	c.Source.User = expandEnv(c.Source.User)
	c.Source.Password = expandEnv(c.Source.Password)
	c.Dest.Host = expandEnv(c.Dest.Host)
	c.Dest.User = expandEnv(c.Dest.User)
	c.Dest.Password = expandEnv(c.Dest.Password)
}

func expandEnv(s string) string {
	if strings.HasPrefix(s, "$") {
		return os.Getenv(strings.TrimPrefix(s, "$"))
	}
	return os.ExpandEnv(s)
}

// Validate checks required fields and applies defaults for zero values.
func (c *Config) Validate() error {
	if len(c.Aliases) == 0 {
		return fmt.Errorf("alias is required")
	}
	if c.Source.Host == "" {
		return fmt.Errorf("source.host is required")
	}
	if c.Source.User == "" {
		return fmt.Errorf("source.user is required")
	}
	if c.Source.Password == "" {
		return fmt.Errorf("source.password is required")
	}
	if c.Dest.Host == "" {
		return fmt.Errorf("dest.host is required")
	}
	if c.Dest.User == "" {
		return fmt.Errorf("dest.user is required")
	}
	if c.Dest.Password == "" {
		return fmt.Errorf("dest.password is required")
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 50
	}
	if c.RetryCount <= 0 {
		c.RetryCount = 3
	}
	if c.Source.Port == 0 {
		c.Source.Port = 993
	}
	if c.Dest.Port == 0 {
		c.Dest.Port = 993
	}
	if c.Dest.DefaultFolder == "" {
		c.Dest.DefaultFolder = "INBOX"
	}
	if len(c.Source.Folders) == 0 {
		c.Source.Folders = []string{"*"}
	}
	return nil
}

// ParseSince parses the Since string into a time.Time (zero if empty).
func (c *Config) ParseSince() (time.Time, error) {
	return parseDate(c.Since)
}

// ParseBefore parses the Before string into a time.Time (zero if empty).
func (c *Config) ParseBefore() (time.Time, error) {
	return parseDate(c.Before)
}

func parseDate(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	layouts := []string{
		"2006-01-02",
		time.RFC822,
		time.RFC822Z,
		time.RFC1123,
		time.RFC1123Z,
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse date %q; use YYYY-MM-DD", s)
}

// TLSMode returns the effective TLS mode for the source, defaulting based on port.
func (c *SourceConfig) TLSMode() string {
	return effectiveTLSMode(c.TLS, c.Port)
}

// TLSMode returns the effective TLS mode for the destination, defaulting based on port.
func (c *DestConfig) TLSMode() string {
	return effectiveTLSMode(c.TLS, c.Port)
}

// effectiveTLSMode resolves the tls field, falling back to port-based defaults.
func effectiveTLSMode(tls string, port int) string {
	switch strings.ToLower(tls) {
	case TLSModeSSL, TLSModeSTARTTLS, TLSModeNone:
		return strings.ToLower(tls)
	}
	// Default: port 993 = ssl, everything else = starttls.
	if port == 993 {
		return TLSModeSSL
	}
	return TLSModeSTARTTLS
}

// MapFolder returns the destination folder for a source folder name.
// Returns ("", false) when the source folder should be skipped.
func (c *DestConfig) MapFolder(sourceFolder string) (string, bool) {
	if c.FolderMap != nil {
		if dest, ok := c.FolderMap[sourceFolder]; ok {
			if dest == nil {
				return "", false // explicitly skipped
			}
			return *dest, true
		}
	}
	return c.DefaultFolder, true
}
