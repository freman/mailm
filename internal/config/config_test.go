package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestStringSliceUnmarshal(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    StringSlice
	}{
		{
			name:  "bare string becomes single-element slice",
			input: `alias: foo@example.com`,
			want:  StringSlice{"foo@example.com"},
		},
		{
			name:  "single-element list",
			input: `alias: ["foo@example.com"]`,
			want:  StringSlice{"foo@example.com"},
		},
		{
			name: "multi-element list",
			input: `alias:
  - foo@example.com
  - bar@example.com`,
			want: StringSlice{"foo@example.com", "bar@example.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got struct {
				Aliases StringSlice `yaml:"alias"`
			}
			if err := yaml.Unmarshal([]byte(tt.input), &got); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got.Aliases) != len(tt.want) {
				t.Fatalf("got len %d, want len %d: %v", len(got.Aliases), len(tt.want), got.Aliases)
			}
			for i, v := range tt.want {
				if got.Aliases[i] != v {
					t.Errorf("index %d: got %q, want %q", i, got.Aliases[i], v)
				}
			}
		})
	}
}
