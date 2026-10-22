package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPatchHytalePatchline(t *testing.T) {
	cases := []struct{ name, doc, patchline, want string }{
		{"release leaves a config without one alone", `{"Version": 5}`, "", `{"Version": 5}`},
		{"release leaves a null patchline alone", `{"Update": {"Enabled": true, "Patchline": null}}`, "", `{"Update": {"Enabled": true, "Patchline": null}}`},
		{"pre-release adds an Update section", `{"Version": 5}`, "pre-release", "{\"Version\": 5,\n  \"Update\": {\n  \"Patchline\": \"pre-release\"\n}}"},
		{"pre-release keeps the other update settings", `{"Update": {"Enabled": true, "CheckIntervalSeconds": 3600}}`, "pre-release", "{\"Update\": {\"Enabled\": true, \"CheckIntervalSeconds\": 3600,\n  \"Patchline\": \"pre-release\"}}"},
		{"release switches a pre-release config back", `{"Update": {"Patchline": "pre-release"}}`, "", `{"Update": {"Patchline": "release"}}`},
		{"an unchanged channel is not rewritten", `{"Update": {"Patchline":"pre-release"}}`, "pre-release", `{"Update": {"Patchline":"pre-release"}}`},
	}
	for _, c := range cases {
		got, err := patchHytalePatchline([]byte(c.doc), c.patchline)
		if err != nil || string(got) != c.want {
			t.Errorf("%s:\n got %q %v\nwant %q", c.name, got, err, c.want)
		}
	}
	for in, want := range map[string]string{"": "", "Release": "", "pre-release": "pre-release", "PreRelease": "pre-release"} {
		if got, err := normalizeHytalePatchline(in); err != nil || got != want {
			t.Errorf("normalize(%q) = %q %v", in, got, err)
		}
	}
	if _, err := normalizeHytalePatchline("beta"); err == nil {
		t.Error("an unknown channel was accepted")
	}
}

// installedHytale is a stopped Hytale server with server files, a world and
// a config.json.
func installedHytale(t *testing.T) (*App, http.Handler, string) {
	t.Helper()
	s := defaultServerSettings("hy", "Hy", 5520, filepath.Join("servers", "hy"))
	app := newCapabilityTestApp(t, s)
	game := pathsFor(app.serverRoot(s)).Game()
	writeFile(t, filepath.Join(game, "Server", "HytaleServer.jar"), "jar")
	writeFile(t, filepath.Join(game, "Assets.zip"), "assets")
	writeFile(t, filepath.Join(game, "Server", "universe", "worlds", "default", "chunk.bin"), "world")
	writeFile(t, filepath.Join(game, "Server", "config.json"), `{"Version": 5, "ServerName": "Hy", "Update": {"Enabled": true}}`)
	return app, newTestMux(app), game
}

func switchPatchline(t *testing.T, mux http.Handler, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/hytale/patchline", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-HSM-Server", "hy")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestSwitchingAStoppedServerToPreReleaseTakesASnapshotFirst(t *testing.T) {
	app, mux, game := installedHytale(t)
	code, out := switchPatchline(t, mux, `{"patchline":"pre-release"}`)
	if code != 200 || out["patchline"] != "pre-release" || !strings.HasPrefix(out["snapshot"].(string), "snapshot-") {
		t.Fatalf("switch = %d %v", code, out)
	}
	if !fileExists(filepath.Join(pathsFor(app.serverRoot(storedServer(app, "hy"))).Snapshots(), out["snapshot"].(string))) {
		t.Error("the snapshot file is missing")
	}
	if storedServer(app, "hy").HytalePatchline != "pre-release" {
		t.Error("the channel was not saved")
	}
	b, _ := os.ReadFile(filepath.Join(game, "Server", "config.json"))
	if !strings.Contains(string(b), `"Patchline": "pre-release"`) || !strings.Contains(string(b), `"Enabled": true`) {
		t.Errorf("config.json = %s", b)
	}
	if code, out := switchPatchline(t, mux, `{"patchline":"pre-release"}`); code != 200 || !strings.Contains(out["message"].(string), "Already") {
		t.Errorf("same channel again = %d %v", code, out)
	}
	if code, _ := switchPatchline(t, mux, `{"patchline":"beta"}`); code != 400 {
		t.Errorf("unknown channel = %d, want 400", code)
	}

	// Saving the settings form keeps the channel.
	s := storedServer(app, "hy")
	payload, _ := json.Marshal(settingsView(s))
	req := httptest.NewRequest("PUT", "/api/settings", bytes.NewReader(bytes.Replace(payload, []byte(`"hytalePatchline":"pre-release",`), nil, 1)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-HSM-Server", "hy")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 || storedServer(app, "hy").HytalePatchline != "pre-release" {
		t.Errorf("settings save = %d %s; channel now %q", rec.Code, rec.Body.String(), storedServer(app, "hy").HytalePatchline)
	}
}

func TestSwitchingARunningServerUsesTheUpdateCommand(t *testing.T) {
	app, mux, _ := installedHytale(t)
	stdin := nopWriteCloser{&bytes.Buffer{}}
	m := app.managers["hy"]
	m.mu.Lock()
	m.running, m.stdin = true, stdin
	m.mu.Unlock()
	t.Cleanup(func() { m.mu.Lock(); m.running, m.stdin = false, nil; m.mu.Unlock() })
	code, out := switchPatchline(t, mux, `{"patchline":"pre-release"}`)
	if code != 200 || out["snapshot"] != "" || stdin.String() != "/update patchline pre-release\n" {
		t.Errorf("switch = %d %v; console got %q", code, out, stdin.String())
	}
}

func TestPatchlineIsHytaleOnlyAndSetAtCreation(t *testing.T) {
	app := newCapabilityTestApp(t, minecraftSettings("mc"))
	mux := newTestMux(app)
	req := httptest.NewRequest("POST", "/api/hytale/patchline", strings.NewReader(`{"patchline":"pre-release"}`))
	req.Header.Set("X-HSM-Server", "mc")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 409 {
		t.Errorf("Minecraft = %d, want 409", rec.Code)
	}
	for body, want := range map[string]int{
		`{"game":"hytale","name":"Pre","hytalePatchline":"pre-release"}`: 201,
		`{"game":"hytale","name":"Bad","hytalePatchline":"beta"}`:        400,
	} {
		req := httptest.NewRequest("POST", "/api/servers", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("create %s = %d %s, want %d", body, rec.Code, rec.Body.String(), want)
		}
	}
	if s := storedServer(app, "pre"); s.HytalePatchline != "pre-release" {
		t.Errorf("created server channel = %q", s.HytalePatchline)
	}
}

func TestDownloaderGetsThePatchline(t *testing.T) {
	app, _, _ := installedHytale(t)
	m := app.managers["hy"]
	writeFile(t, filepath.Join(m.toolsDir, "hytale-downloader"), "#!/bin/sh\necho \"downloader args: $*\"\nexit 1\n")
	if err := m.RunDownloader("pre-release"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "downloader to finish", func() bool { return !m.InstallBusy() })
	found := false
	for _, l := range m.logs.Snapshot() {
		found = found || strings.HasSuffix(l.Line, "-patchline pre-release")
	}
	if !found {
		t.Error("the downloader was not given -patchline pre-release")
	}
}
