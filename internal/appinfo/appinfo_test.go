package appinfo

import "testing"

func TestSafeModeEnvFailsClosed(t *testing.T) {
	cases := []struct {
		current, legacy string
		want            bool
	}{
		{"", "", false},
		{"0", "", false},
		{"false", "", false},
		{" FALSE ", "", false},
		{"no", "off", false},
		{"Off", "No", false},
		{"1", "", true},
		{"true", "", true},
		// Values EnvBool does not know must not disable safe mode.
		{"enabled", "", true},
		{"y", "", true},
		{"t", "", true},
		{"2", "", true},
		{"always", "", true},
		// Either name turns it on; the other cannot turn it off.
		{"false", "1", true},
		{"0", "enabled", true},
		{"", "1", true},
		{"1", "false", true},
	}
	for _, c := range cases {
		t.Setenv(EnvPrefix+"SAFE_MODE", c.current)
		t.Setenv(LegacyEnvPrefix+"SAFE_MODE", c.legacy)
		if got := SafeModeEnv(); got != c.want {
			t.Errorf("%sSAFE_MODE=%q %sSAFE_MODE=%q: SafeModeEnv() = %v, want %v",
				EnvPrefix, c.current, LegacyEnvPrefix, c.legacy, got, c.want)
		}
	}
}

func TestEnvBoolStaysStrict(t *testing.T) {
	t.Setenv(LegacyEnvPrefix+"NO_UPDATE_CHECK", "")
	for value, want := range map[string]bool{"1": true, "TRUE": true, "yes": true, "on": true, "": false, "0": false, "enabled": false} {
		t.Setenv(EnvPrefix+"NO_UPDATE_CHECK", value)
		if got := EnvBool("NO_UPDATE_CHECK"); got != want {
			t.Errorf("EnvBool with %q = %v, want %v", value, got, want)
		}
	}
}

func TestResolveVersion(t *testing.T) {
	cases := []struct {
		ldflags, module string
		want            string
		stamped         bool
	}{
		{"v1.2.3", "(devel)", "v1.2.3", true},
		{"v1.2.3", "v9.9.9", "v1.2.3", true},
		// "go install …@v1.2.3": shown, but not a stamped release binary.
		{"dev", "v1.2.3", "v1.2.3", false},
		{"dev", "(devel)", "dev", false},
		{"dev", "", "dev", false},
	}
	for _, c := range cases {
		got, stamped := resolveVersion(c.ldflags, c.module)
		if got != c.want || stamped != c.stamped {
			t.Errorf("resolveVersion(%q, %q) = %q, %v; want %q, %v", c.ldflags, c.module, got, stamped, c.want, c.stamped)
		}
	}
}

func TestStampedAndBareVersion(t *testing.T) {
	oldVersion, oldStamped := Version, stamped
	t.Cleanup(func() { Version, stamped = oldVersion, oldStamped })

	Version, stamped = "v1.2.3", false // as after "go install …@v1.2.3"
	if got := StampedVersion(); got != "dev" {
		t.Errorf("StampedVersion() of a go install build = %q, want dev", got)
	}
	if got := BareVersion(); got != "1.2.3" {
		t.Errorf("BareVersion() = %q", got)
	}
	if got := UserAgent(); got != Name+"/1.2.3" {
		t.Errorf("UserAgent() = %q", got)
	}
	stamped = true
	if got := StampedVersion(); got != "v1.2.3" {
		t.Errorf("StampedVersion() of a release = %q", got)
	}
}
