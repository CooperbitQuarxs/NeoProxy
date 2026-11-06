package aws

import (
	"os"
	"path/filepath"
	"testing"
)

// ResolveRegion follows resolveAwsRegion's precedence, and the shared config
// file only participates once a profile is selected or AWS_SDK_LOAD_CONFIG
// opts the default profile in.
func TestResolveRegionPrecedence(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(config, []byte("# shared config\r\n[default]\r\nregion = ap-south-1\r\n[profile work]\nregion=eu-central-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		explicit string
		profile  string
		env      map[string]string
		want     string
	}{
		{name: "explicit wins", explicit: "sa-east-1", env: map[string]string{"AWS_REGION": "us-west-2", "AWS_PROFILE": "work"}, want: "sa-east-1"},
		{name: "AWS_REGION before AWS_DEFAULT_REGION", env: map[string]string{"AWS_REGION": "us-west-2", "AWS_DEFAULT_REGION": "eu-west-1"}, want: "us-west-2"},
		{name: "AWS_DEFAULT_REGION before profile", env: map[string]string{"AWS_DEFAULT_REGION": "eu-west-1", "AWS_PROFILE": "work"}, want: "eu-west-1"},
		{name: "default profile ignored without opt-in", want: DefaultRegion},
		{name: "AWS_SDK_LOAD_CONFIG opts the default profile in", env: map[string]string{"AWS_SDK_LOAD_CONFIG": "TRUE"}, want: "ap-south-1"},
		{name: "AWS_PROFILE selects its section", env: map[string]string{"AWS_PROFILE": "work"}, want: "eu-central-1"},
		{name: "explicit profile beats AWS_PROFILE", profile: "default", env: map[string]string{"AWS_PROFILE": "work"}, want: "ap-south-1"},
		{name: "profile without a region falls back", env: map[string]string{"AWS_PROFILE": "missing"}, want: DefaultRegion},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, name := range []string{"AWS_REGION", "AWS_DEFAULT_REGION", "AWS_PROFILE", "AWS_SDK_LOAD_CONFIG"} {
				t.Setenv(name, tc.env[name])
			}
			t.Setenv("AWS_CONFIG_FILE", config)
			if got := ResolveRegion(tc.explicit, tc.profile); got != tc.want {
				t.Fatalf("ResolveRegion(%q, %q) = %q, want %q", tc.explicit, tc.profile, got, tc.want)
			}
		})
	}
}
