package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestHytaleLaunchNeverPassesBackupOptions(t *testing.T) {
	cases := []struct {
		name, script, extra string
		want                []string
	}{
		{"release start.sh", `DEFAULT_ARGS="--assets ../Assets.zip --backup --backup-dir backups"`, "",
			[]string{"--bind", "0.0.0.0:5520"}},
		{"pre-release start.sh with a frequency", `DEFAULT_ARGS="--assets ../Assets.zip --backup --backup-frequency 30 --backup-dir backups"`, "",
			[]string{"--bind", "0.0.0.0:5520"}},
		{"start.sh sets the bind address", "java -jar HytaleServer.jar --bind=0.0.0.0:5520 \"$@\"", "",
			nil},
		{"commented out in start.sh", "# --bind 0.0.0.0:1\nDEFAULT_ARGS=\"--backup\"", "",
			[]string{"--bind", "0.0.0.0:5520"}},
		{"extra arguments set the bind address", "", "--bind 0.0.0.0:6000 --foo",
			[]string{"--bind", "0.0.0.0:6000", "--foo"}},
		{"similar option names are not matches", `DEFAULT_ARGS="--bind-retries 3"`, "",
			[]string{"--bind", "0.0.0.0:5520"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			app, s, _ := hytaleConfigApp(t, gameWrittenConfig)
			s.ExtraArgs = c.extra
			writeFile(t, filepath.Join(pathsFor(app.serverRoot(s)).Game(), "start.sh"), c.script)
			args, err := (HytaleAdapter{}).PrepareLaunch(app, s)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(args, c.want) {
				t.Fatalf("args = %q, want %q", args, c.want)
			}
		})
	}
}

func TestHytaleBackupFrequencyGoesInConfig(t *testing.T) {
	app, s, path := hytaleConfigApp(t, gameWrittenConfig)
	s.BackupFrequency = 45
	if err := app.applyHytaleConfig(s); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	got, _ := jsonMember(b, "Backup")
	want, _ := jsonMember([]byte(gameWrittenConfig), "Backup")
	if w := strings.Replace(string(want), `"FrequencyMinutes": 30`, `"FrequencyMinutes": 45`, 1); string(got) != w {
		t.Fatalf("Backup =\n%s\nwant\n%s", got, w)
	}

	// A config without a Backup section keeps the game's defaults.
	noBackup := `{"ServerName": "Old Name", "MOTD": "Hi", "Password": "", "MaxPlayers": 20, "Port": 5520, "RequireJoinPermission": false, "Defaults": {"GameMode": "Adventure"}}`
	app, s, path = hytaleConfigApp(t, noBackup)
	s.BackupFrequency = 45
	if err := app.applyHytaleConfig(s); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), "Backup") {
		t.Fatalf("a Backup section was added: %s", b)
	}
}
