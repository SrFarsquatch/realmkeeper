package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gameWrittenConfig is a config.json as a newer server writes it: a backup
// section with a Java long and a float, keys in the game's order.
const gameWrittenConfig = `{
    "Version": 5,
    "ServerName": "Old Name",
    "MOTD": "Hi",
    "Password": "",
    "MaxPlayers": 20,
    "Port": 5520,
    "Defaults": {
        "World": "default",
        "GameMode": "Adventure"
    },
    "Backup": {
        "Enabled": true,
        "Directory": "backups",
        "FrequencyMinutes": 30,
        "MaxCount": 5,
        "RetentionMillis": 9223372036854775807,
        "CompressionLevel": 6.0
    },
    "Update": {"RunBackupBeforeUpdate": true}
}`

func hytaleConfigApp(t *testing.T, content string) (*App, ServerSettings, string) {
	t.Helper()
	s := defaultServerSettings("hy", "Old Name", 5520, filepath.Join("servers", "hy"))
	s.MOTD, s.MaxPlayers, s.GameMode = "Hi", 20, "Adventure"
	app := &App{dataDir: t.TempDir()}
	path := filepath.Join(pathsFor(app.serverRoot(s)).Game(), "Server", "config.json")
	writeFile(t, path, content)
	return app, s, path
}

func TestHytaleConfigKeepsTheGamesBackupSectionExactly(t *testing.T) {
	app, s, path := hytaleConfigApp(t, gameWrittenConfig)
	s.ServerName, s.MOTD, s.GameMode = "New <gold>Name</gold>", "Welcome & enjoy", "Creative"
	if err := app.applyHytaleConfig(s); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	got := string(b)
	backup := gameWrittenConfig[strings.Index(gameWrittenConfig, `    "Backup"`):strings.LastIndex(gameWrittenConfig, "\n}")]
	if !strings.Contains(got, backup) {
		t.Errorf("the game's Backup and Update sections changed:\n%s", got)
	}
	for _, want := range []string{
		`"ServerName": "New <gold>Name</gold>"`, `"MOTD": "Welcome & enjoy"`, `"GameMode": "Creative"`,
		`"World": "default"`, `"RequireJoinPermission": false`, `"RetentionMillis": 9223372036854775807`, `"CompressionLevel": 6.0`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config.json lacks %s:\n%s", want, got)
		}
	}
	if strings.Index(got, `"Version"`) > strings.Index(got, `"ServerName"`) {
		t.Error("key order changed")
	}
}

func TestHytaleConfigIsNotRewrittenWhenAlreadyCurrent(t *testing.T) {
	current := strings.Replace(gameWrittenConfig, `"Port": 5520,`, `"Port": 5520, "RequireJoinPermission": false, "MaxPlayersSpelledAsFloat": 20.0,`, 1)
	app, s, path := hytaleConfigApp(t, current)
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := app.applyHytaleConfig(s); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if string(b) != current || !info.ModTime().Equal(old) {
		t.Error("an up-to-date config.json was rewritten")
	}
}

func TestHytaleConfigAddsMissingSettingsAndRefusesBrokenFiles(t *testing.T) {
	app, s, path := hytaleConfigApp(t, `{"Version": 5, "Defaults": "broken", "Backup": {"MaxCount": 5}}`)
	if err := app.applyHytaleConfig(s); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	for _, want := range []string{`"ServerName": "Old Name"`, `"Port": 5520`, `"Defaults": {`, `"GameMode": "Adventure"`, `"Backup": {"MaxCount": 5,`, `"FrequencyMinutes": 30`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("config.json lacks %s:\n%s", want, b)
		}
	}
	app, s, path = hytaleConfigApp(t, `{"ServerName": "x",}`)
	if err := app.applyHytaleConfig(s); err == nil || !strings.Contains(err.Error(), "invalid config.json") {
		t.Errorf("broken JSON = %v, want an error", err)
	}
	if b, _ := os.ReadFile(path); string(b) != `{"ServerName": "x",}` {
		t.Error("a broken config.json was overwritten")
	}
}

func TestSetJSONMemberEditsOnlyThatMember(t *testing.T) {
	doc := []byte("{\n  \"a\": 1,\n  \"b\": {\"c\": [1, 2]},\n  \"d\": 1.50\n}\n")
	out, err := setJSONMember(doc, "b", "x")
	if err != nil || string(out) != "{\n  \"a\": 1,\n  \"b\": \"x\",\n  \"d\": 1.50\n}\n" {
		t.Errorf("replace = %q %v", out, err)
	}
	out, err = setJSONMember([]byte(`{}`), "a", 1)
	if err != nil || string(out) != "{\n  \"a\": 1\n}" {
		t.Errorf("add to empty = %q %v", out, err)
	}
	for _, bad := range []string{`[]`, `{"a":1} {"b":2}`, `"x"`} {
		if _, err := setJSONMember([]byte(bad), "a", 1); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}
