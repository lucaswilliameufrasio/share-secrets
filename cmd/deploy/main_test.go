package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestInactiveSlotSelection(t *testing.T) {
	tests := []struct {
		active string
		want   string
		port   int
	}{
		{active: "none", want: "blue", port: defaultBluePort},
		{active: "blue", want: "green", port: defaultGreenPort},
		{active: "green", want: "blue", port: defaultBluePort},
	}

	for _, test := range tests {
		gotSlot, gotPort := inactiveSlot(test.active)
		if gotSlot != test.want || gotPort != test.port {
			t.Errorf("inactiveSlot(%q)=(%q, %d), want (%q, %d)", test.active, gotSlot, gotPort, test.want, test.port)
		}
	}
}

func TestHostAliasValidation(t *testing.T) {
	valid := []string{"production-arm", "vps-prod", "100.64.0.1", "my.host"}
	for _, alias := range valid {
		if !hostAliasPattern.MatchString(alias) {
			t.Errorf("host alias %q should be accepted", alias)
		}
	}

	invalid := []string{"", "two words", "user@host", "host;rm -rf /"}
	for _, alias := range invalid {
		if hostAliasPattern.MatchString(alias) {
			t.Errorf("host alias %q should be rejected", alias)
		}
	}
}

func TestShellQuoteArgs(t *testing.T) {
	arguments := []string{"docker", "run", "it's-safe"}
	want := []string{"'docker'", "'run'", "'it'\\''s-safe'"}
	if got := shellQuoteArgs(arguments); !reflect.DeepEqual(got, want) {
		t.Fatalf("shellQuoteArgs()=%q, want %q", got, want)
	}
}

func TestDockerRunEnvironmentFileScriptKeepsValuesOutOfCommand(t *testing.T) {
	token := "never-put-this-token-in-the-command"
	script, err := dockerRunEnvironmentFileScript("run", "--env-file", "/tmp/input.env", "image:name")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(script, token) || !strings.Contains(script, `cat > "$env_file"`) || !strings.Contains(script, `--env-file "$env_file"`) {
		t.Fatalf("environment-file script did not use stdin-only secret input: %s", script)
	}
	if !strings.Contains(script, `trap 'rm -f "$env_file"' EXIT`) {
		t.Fatalf("environment-file script does not remove its temporary file: %s", script)
	}
}

func TestDeploymentSettingsValidateBeforeRemoteMutation(t *testing.T) {
	invalidToken := "short-sensitive-value"
	t.Setenv("METRICS_TOKEN", invalidToken)
	if _, err := deploymentSettingsFromEnvironment(); err == nil || strings.Contains(err.Error(), invalidToken) {
		t.Fatalf("invalid metrics token should fail without echoing its value: %v", err)
	}

	t.Setenv("METRICS_TOKEN", strings.Repeat("x", 32))
	t.Setenv("ACTIVE_SECRET_LIMIT", "701")
	if _, err := deploymentSettingsFromEnvironment(); err == nil {
		t.Fatal("out-of-range active capacity was accepted")
	}

	t.Setenv("ACTIVE_SECRET_LIMIT", "600")
	t.Setenv("CREATE_RATE_PER_MINUTE", "120")
	t.Setenv("CREATE_RATE_BURST", "40")
	t.Setenv("TRUSTED_PROXIES", "192.0.2.10,10.0.0.0/8")
	settings, err := deploymentSettingsFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.runtimeVariables) != 4 || settings.metricsToken != strings.Repeat("x", 32) {
		t.Fatalf("unexpected deployment runtime variable count: %d", len(settings.runtimeVariables))
	}

	t.Setenv("TRUSTED_PROXIES", "not-an-ip")
	if _, err := deploymentSettingsFromEnvironment(); err == nil {
		t.Fatal("invalid trusted proxy address was accepted")
	}
}

func TestSQLiteDeploymentRejectsUnsafeStorageChanges(t *testing.T) {
	t.Setenv("STORAGE_BACKEND", "sqlite")
	t.Setenv("SQLITE_DATA_DIR", "/srv/private/secrets")
	t.Setenv("SQLITE_NO_PAYLOAD_BACKUP_ACK", "yes")
	t.Setenv("SQLITE_VOLATILE_CUTOVER_ACK", "")
	settings, err := deploymentSettingsFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateActiveStorage("<no value>||", settings); err == nil {
		t.Fatal("volatile cutover accepted without explicit acknowledgement")
	}
	if err := validateActiveStorage("sqlite|/different|sqlite", settings); err == nil {
		t.Fatal("different active SQLite volume accepted")
	}
	if err := validateActiveStorage("sqlite|/srv/private/secrets|sqlite", settings); err != nil {
		t.Fatalf("same SQLite volume rejected: %v", err)
	}
	settings.cutoverConfirmed = true
	if err := validateActiveStorage("<no value>||", settings); err != nil {
		t.Fatalf("explicit volatile cutover rejected: %v", err)
	}
	settings.sqliteDir = ""
	if err := validateActiveStorage("sqlite|/srv/private/secrets|sqlite", settings); err == nil {
		t.Fatal("SQLite to memory rollback silently accepted")
	}
	if err := validateActiveStorage("<no value>|/srv/private/secrets|sqlite", settings); err == nil {
		t.Fatal("unlabeled active SQLite container accepted")
	}
	for _, dir := range []string{"relative", "/", "/srv/../tmp", "/srv/foo,readonly"} {
		t.Setenv("SQLITE_DATA_DIR", dir)
		if _, err := deploymentSettingsFromEnvironment(); err == nil {
			t.Fatal("unsafe SQLite directory accepted")
		}
	}
}
