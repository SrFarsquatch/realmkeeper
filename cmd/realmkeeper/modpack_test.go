package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// fakePacks stands in for Modrinth with mod pack and data pack projects.
// Every file is served from /cdn/.
type fakePacks struct {
	srv      *httptest.Server
	mu       sync.Mutex
	files    map[string][]byte
	versions map[string]map[string]any // by version ID
	projects map[string][]string       // project → version IDs, newest first
	searches []url.Values
}

func newFakePacks(t *testing.T) *fakePacks {
	t.Helper()
	f := &fakePacks{files: map[string][]byte{}, versions: map[string]map[string]any{}, projects: map[string][]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		p := r.URL.Path
		switch {
		case p == "/v2/search":
			f.searches = append(f.searches, r.URL.Query())
			_ = json.NewEncoder(w).Encode(map[string]any{"total_hits": 1, "hits": []any{map[string]any{"project_id": "pack", "slug": "pack", "title": "Pack"}}})
		case strings.HasPrefix(p, "/v2/version/"):
			v, ok := f.versions[strings.TrimPrefix(p, "/v2/version/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(v)
		case strings.HasPrefix(p, "/v2/project/") && strings.HasSuffix(p, "/version"):
			list := []any{}
			for _, id := range f.projects[strings.TrimSuffix(strings.TrimPrefix(p, "/v2/project/"), "/version")] {
				list = append(list, f.versions[id])
			}
			_ = json.NewEncoder(w).Encode(list)
		case strings.HasPrefix(p, "/v2/project/"):
			_ = json.NewEncoder(w).Encode(map[string]string{"title": "The " + strings.TrimPrefix(p, "/v2/project/") + " project"})
		case strings.HasPrefix(p, "/cdn/"):
			b, ok := f.files[strings.TrimPrefix(p, "/cdn/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	oldAPI, oldTrust, oldPack := modrinthAPI, trustedModrinthDownload, trustedMrpackDownload
	modrinthAPI = f.srv.URL + "/v2"
	trustedModrinthDownload = func(raw string) bool { return strings.HasPrefix(raw, f.srv.URL+"/cdn/") }
	trustedMrpackDownload = trustedModrinthDownload
	t.Cleanup(func() { modrinthAPI, trustedModrinthDownload, trustedMrpackDownload = oldAPI, oldTrust, oldPack })
	return f
}

// serve publishes a file and returns its URL and SHA-512.
func (f *fakePacks) serve(name string, b []byte) (string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[name] = b
	return f.srv.URL + "/cdn/" + url.PathEscape(name), sha512Hex(b)
}

// packFile is a file listed in a pack's index.
type packFile struct {
	path, content, server string
	badHash               bool
}

// addPack publishes a pack version for Minecraft mc with the given loader
// dependency, listed files and overrides ("server-overrides/..." paths go to
// that folder; others to overrides/).
func (f *fakePacks) addPack(t *testing.T, project, id, number, mc string, loader map[string]string, files []packFile, overrides map[string]string) {
	t.Helper()
	index := map[string]any{"formatVersion": 1, "game": "minecraft", "versionId": number, "name": project}
	deps := map[string]string{"minecraft": mc}
	for k, v := range loader {
		deps[k] = v
	}
	index["dependencies"] = deps
	list := []any{}
	for _, pf := range files {
		u, sum := f.serve(id+"-"+filepath.Base(pf.path), []byte(pf.content))
		if pf.badHash {
			sum = strings.Repeat("b", 128)
		}
		entry := map[string]any{"path": pf.path, "hashes": map[string]string{"sha512": sum}, "downloads": []string{u}, "fileSize": len(pf.content)}
		if pf.server != "" {
			entry["env"] = map[string]string{"client": "required", "server": pf.server}
		}
		list = append(list, entry)
	}
	index["files"] = list
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	b, _ := json.Marshal(index)
	w, _ := zw.Create("modrinth.index.json")
	_, _ = w.Write(b)
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		full := "overrides/" + name
		if strings.HasPrefix(name, "server-overrides/") || strings.HasPrefix(name, "client-overrides/") {
			full = name
		}
		w, err := zw.Create(full)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(overrides[name]))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	u, sum := f.serve(id+".mrpack", buf.Bytes())
	loaders := []string{}
	for k := range loader {
		loaders = append(loaders, map[string]string{"fabric-loader": "fabric", "quilt-loader": "quilt", "forge": "forge", "neoforge": "neoforge"}[k])
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.versions[id] = map[string]any{
		"id": id, "project_id": project, "name": project + " " + number, "version_number": number, "version_type": "release",
		"date_published": "2026-10-0" + number[:1], "game_versions": []string{mc}, "loaders": loaders,
		"files": []any{map[string]any{"url": u, "filename": project + "-" + number + ".mrpack", "primary": true, "size": buf.Len(), "hashes": map[string]string{"sha512": sum}}},
	}
	f.projects[project] = append([]string{id}, f.projects[project]...)
}

// packServer is a stopped, installed Fabric 1.21.8 server with loader 0.17.2.
func packServer(t *testing.T) (*App, http.Handler, *ServerManager, string) {
	t.Helper()
	s := minecraftSettings("mp")
	s.MinecraftVariant, s.MinecraftVersion = "fabric", "1.21.8"
	app := newCapabilityTestApp(t, s)
	m := app.managers["mp"]
	game := pathsFor(m.rootDir).Game()
	writeFile(t, filepath.Join(game, fabricLauncherJar), "jar")
	writeFile(t, filepath.Join(game, "eula.txt"), "eula=true\n")
	if err := writeMinecraftInstallInfo(game, minecraftInstallInfo{Version: "1.21.8", Variant: "fabric", LoaderVersion: "0.17.2"}); err != nil {
		t.Fatal(err)
	}
	return app, newTestMux(app), m, game
}

func installPack(t *testing.T, mux http.Handler, m *ServerManager, body string) map[string]any {
	t.Helper()
	code, out := modrinthCall(t, mux, "mp", "POST", "/api/modrinth/modpack", body)
	if code != 202 {
		t.Fatalf("install pack = %d %v", code, out)
	}
	waitFor(t, "the pack to install", func() bool { return !m.InstallBusy() })
	return out
}

func lastLog(m *ServerManager) string {
	lines := m.logs.Snapshot()
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1].Line
}

func TestModpackInstallsServerFilesAndOverridesThenUpdates(t *testing.T) {
	f := newFakePacks(t)
	fabric := map[string]string{"fabric-loader": "0.17.2"}
	f.addPack(t, "pack", "v1", "1.0", "1.21.8", fabric, []packFile{
		{path: "mods/sodium.jar", content: "sodium"},
		{path: "mods/old.jar", content: "old"},
		{path: "mods/shaders-only.jar", content: "client", server: "unsupported"},
		{path: "resourcepacks/x.zip", content: "rp", server: "optional"},
	}, map[string]string{
		"config/a.toml":                    "overrides",
		"config/b.toml":                    "overrides",
		"mods/bundled.jar":                 "bundled",
		"server-overrides/config/b.toml":   "server",
		"client-overrides/options.txt":     "client",
		"server-overrides/server-icon.png": "icon",
	})
	_, mux, m, game := packServer(t)
	writeFile(t, filepath.Join(game, "mods", "mine.jar"), "added by the owner")
	writeFile(t, filepath.Join(game, "world", "level.dat"), "world")

	installPack(t, mux, m, `{"projectId":"pack","versionId":"v1"}`)
	read := func(rel string) string { b, _ := os.ReadFile(filepath.Join(game, rel)); return string(b) }
	for rel, want := range map[string]string{
		"mods/sodium.jar": "sodium", "mods/old.jar": "old", "resourcepacks/x.zip": "rp", "config/a.toml": "overrides",
		"config/b.toml": "server", "mods/bundled.jar": "bundled", "server-icon.png": "icon", "mods/mine.jar": "added by the owner",
	} {
		if got := read(rel); got != want {
			t.Errorf("%s = %q, want %q (log: %s)", rel, got, want, lastLog(m))
		}
	}
	if fileExists(filepath.Join(game, "mods", "shaders-only.jar")) || fileExists(filepath.Join(game, "options.txt")) {
		t.Error("client-only files were installed")
	}
	rec, ok := readModpackRecord(game)
	if !ok || rec.VersionID != "v1" || rec.Title != "The pack project" || rec.LoaderVersion != "0.17.2" || strings.Join(rec.Files, ",") != "mods/bundled.jar,mods/old.jar,mods/sodium.jar,resourcepacks/x.zip" {
		t.Errorf("record = %+v", rec)
	}
	if fileExists(pathsFor(m.rootDir).Tmp() + "/modpack") {
		t.Error("the staging folder was left behind")
	}
	code, out := modrinthCall(t, mux, "mp", "GET", "/api/modrinth/modpack", "")
	if pack, _ := out["pack"].(map[string]any); code != 200 || pack["versionNumber"] != "1.0" {
		t.Errorf("GET pack = %d %v", code, out)
	}

	// 2.0 drops old.jar and changes a config the owner may have edited.
	writeFile(t, filepath.Join(game, "config", "a.toml"), "edited by the owner")
	f.addPack(t, "pack", "v2", "2.0", "1.21.8", fabric, []packFile{{path: "mods/sodium.jar", content: "sodium 2"}}, map[string]string{"config/b.toml": "b2"})
	out = installPack(t, mux, m, `{"projectId":"pack","versionId":"v2"}`)
	if read("mods/sodium.jar") != "sodium 2" || fileExists(filepath.Join(game, "mods", "old.jar")) || fileExists(filepath.Join(game, "mods", "bundled.jar")) {
		t.Errorf("update did not replace the pack's files (log: %s)", lastLog(m))
	}
	if read("mods/mine.jar") != "added by the owner" || read("config/a.toml") != "edited by the owner" || read("config/b.toml") != "b2" {
		t.Error("update touched files it does not own, or skipped its overrides")
	}
	if snap, _ := out["snapshot"].(string); snap == "" || !fileExists(filepath.Join(pathsFor(m.rootDir).Snapshots(), snap)) {
		t.Errorf("update response = %v; want a snapshot taken first", out)
	}
}

func TestModpackRefusesMismatchesAndBadFilesWithoutChangingTheServer(t *testing.T) {
	f := newFakePacks(t)
	f.addPack(t, "other-mc", "mc", "1.0", "1.20.1", map[string]string{"fabric-loader": "0.17.2"}, nil, nil)
	f.addPack(t, "forge-pack", "fg", "1.0", "1.21.8", map[string]string{"forge": "52.0.1"}, nil, nil)
	f.addPack(t, "bad-hash", "bh", "1.0", "1.21.8", map[string]string{"fabric-loader": "0.17.2"}, []packFile{{path: "mods/ok.jar", content: "ok"}, {path: "mods/bad.jar", content: "bad", badHash: true}}, nil)
	f.addPack(t, "escape", "es", "1.0", "1.21.8", map[string]string{"fabric-loader": "0.17.2"}, []packFile{{path: "../escape.jar", content: "x"}}, nil)
	f.addPack(t, "escape2", "es2", "1.0", "1.21.8", map[string]string{"fabric-loader": "0.17.2"}, nil, map[string]string{".hsm-minecraft.json": "{}"})
	_, mux, m, game := packServer(t)
	for project, want := range map[string]string{
		"other-mc|mc": "for Minecraft 1.20.1", "forge-pack|fg": "for Forge", "bad-hash|bh": "SHA-512",
		"escape|es": "invalid file", "escape2|es2": "unsafe override path",
	} {
		p, v, _ := strings.Cut(project, "|")
		installPack(t, mux, m, `{"projectId":"`+p+`","versionId":"`+v+`"}`)
		if log := lastLog(m); !strings.Contains(log, "failed") || !strings.Contains(log, want) {
			t.Errorf("%s: last log = %q, want %q", p, log, want)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(game, "mods")); len(entries) != 0 {
		t.Errorf("mods = %v; a failed install must not leave files", entries)
	}
	if _, ok := readModpackRecord(game); ok {
		t.Error("a failed install was recorded")
	}
	if info, _ := readMinecraftInstallInfo(game); info.LoaderVersion != "0.17.2" {
		t.Error("the install info was overwritten")
	}
	if code, _ := modrinthCall(t, mux, "mp", "POST", "/api/modrinth/modpack", `{"projectId":"pack","versionId":"../x"}`); code != 400 {
		t.Errorf("invalid version = %d, want 400", code)
	}
	if code, _ := modrinthCall(t, mux, "mp", "POST", "/api/modrinth/modpack", `{"projectId":"pack","versionId":"fg"}`); code != 202 {
		t.Errorf("a version of another project = %d, want it accepted then refused", code)
	}
	waitFor(t, "the job", func() bool { return !m.InstallBusy() })
	if !strings.Contains(lastLog(m), "another Modrinth project") {
		t.Errorf("last log = %q", lastLog(m))
	}
}

func TestServerIsCreatedAndInstalledFromAPack(t *testing.T) {
	fakeMinecraftServices(t)
	f := newFakePacks(t)
	f.addPack(t, "pack", "v1", "1.0", "1.21.8", map[string]string{"fabric-loader": "0.17.2"}, []packFile{{path: "mods/sodium.jar", content: "sodium"}}, map[string]string{"config/a.toml": "a"})
	// Modrinth lists the pack for Quilt, but its index names Fabric: the
	// index decides at install.
	f.versions["v1"]["loaders"] = []string{"quilt"}
	app := newCapabilityTestApp(t)
	code, out := createServer(t, app, `{"game":"minecraft-java","name":"Packed","minecraftEulaAccepted":true,"minecraftVariant":"vanilla","minecraftModpack":{"projectId":"pack","versionId":"v1"}}`)
	if code != 201 {
		t.Fatalf("create = %d %v", code, out)
	}
	s := storedServer(app, "packed")
	if s.MinecraftVariant != "quilt" || s.MinecraftVersion != "1.21.8" || s.MinecraftModpack == nil || s.MinecraftModpack.VersionID != "v1" {
		t.Fatalf("created server = %+v", s)
	}
	mux := newTestMux(app)
	req := httptest.NewRequest("POST", "/api/install/minecraft", strings.NewReader("{}"))
	req.Header.Set("X-HSM-Server", "packed")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 202 {
		t.Fatalf("install = %d %s", rec.Code, rec.Body)
	}
	m := app.managers["packed"]
	waitFor(t, "install", func() bool { return !m.InstallBusy() })
	game := pathsFor(m.rootDir).Game()
	info, _ := readMinecraftInstallInfo(game)
	if !m.Installed() || info.LoaderVersion != "0.17.2" || info.Version != "1.21.8" || !fileExists(filepath.Join(game, "mods", "sodium.jar")) || !fileExists(filepath.Join(game, "config", "a.toml")) {
		for _, l := range m.logs.Snapshot() {
			t.Log(l.Line)
		}
		t.Fatalf("installed = %v, info = %+v", m.Installed(), info)
	}
	if s := storedServer(app, "packed"); s.MinecraftVariant != "fabric" {
		t.Errorf("variant after install = %q, want the pack's fabric", s.MinecraftVariant)
	}

	for body, want := range map[string]string{
		`{"game":"minecraft-java","name":"Nope","minecraftEulaAccepted":true,"minecraftModpack":{"projectId":"pack","versionId":"missing"}}`: "not found",
		`{"game":"minecraft-java","name":"Nope","minecraftEulaAccepted":true,"minecraftModpack":{"projectId":"pack","versionId":"a/b"}}`:     "invalid mod pack",
	} {
		if code, out := createServer(t, app, body); code != 400 || !strings.Contains(out["error"].(string), want) {
			t.Errorf("create = %d %v, want 400 %q", code, out, want)
		}
	}
}

func TestModpackSearchesAreLimitedToPacks(t *testing.T) {
	f := newFakePacks(t)
	_, mux, _, _ := packServer(t)
	if code, out := modrinthCall(t, mux, "", "GET", "/api/modpacks/search?q=sky", ""); code != 200 || len(out["hits"].([]any)) != 1 {
		t.Fatalf("wizard search = %d %v", code, out)
	}
	if code, _ := modrinthCall(t, mux, "mp", "GET", "/api/modrinth/search?kind=modpack", ""); code != 200 {
		t.Fatalf("server search = %d", code)
	}
	wizard, server := f.searches[0].Get("facets"), f.searches[1].Get("facets")
	for _, want := range []string{`"project_type:modpack"`, `"categories:forge"`, `"categories:neoforge"`} {
		if !strings.Contains(wizard, want) {
			t.Errorf("wizard facets %s lack %s", wizard, want)
		}
	}
	if !strings.Contains(server, `"categories:fabric"`) || strings.Contains(server, "forge") || !strings.Contains(server, `"versions:1.21.8"`) {
		t.Errorf("server facets = %s", server)
	}

	vanilla := minecraftSettings("van")
	app := newCapabilityTestApp(t, vanilla)
	if code, _ := modrinthCall(t, newTestMux(app), "van", "GET", "/api/modrinth/search?kind=modpack", ""); code != 409 {
		t.Errorf("vanilla pack search = %d, want 409", code)
	}
	if !serverCapabilities(vanilla).DataPacks || serverCapabilities(vanilla).ModPacks {
		t.Error("vanilla servers get data packs and no mod packs")
	}
}

func TestDatapacksInstallIntoTheWorldOfAnyMinecraftServer(t *testing.T) {
	f := newFakePacks(t)
	u, sum := f.serve("terralith.zip", []byte("terralith"))
	f.versions["tl1"] = map[string]any{
		"id": "tl1", "project_id": "terralith", "name": "Terralith 2", "version_number": "2.0", "version_type": "release",
		"date_published": "2026-09-01", "game_versions": []string{"1.21.8"}, "loaders": []string{"datapack"},
		"files": []any{map[string]any{"url": u, "filename": "Terralith.zip", "primary": true, "size": 9, "hashes": map[string]string{"sha512": sum}}},
	}
	f.projects["terralith"] = []string{"tl1"}
	s := minecraftSettings("van")
	app := newCapabilityTestApp(t, s)
	mux := newTestMux(app)
	game := pathsFor(app.serverRoot(s)).Game()
	writeFile(t, filepath.Join(game, "server.properties"), "level-name=survival\n")
	writeFile(t, filepath.Join(game, "survival", "datapacks", "handmade", "pack.mcmeta"), "{}")

	if code, _ := modrinthCall(t, mux, "van", "GET", "/api/modrinth/search?kind=datapack&q=world", ""); code != 200 {
		t.Fatalf("search = %d", code)
	}
	if facets := f.searches[0].Get("facets"); !strings.Contains(facets, `"categories:datapack"`) || !strings.Contains(facets, `"project_type:datapack"`) {
		t.Errorf("facets = %s", facets)
	}
	code, out := modrinthCall(t, mux, "van", "POST", "/api/modrinth/install", `{"kind":"datapack","projectId":"terralith"}`)
	if code != 200 {
		t.Fatalf("install = %d %v", code, out)
	}
	if b, _ := os.ReadFile(filepath.Join(game, "survival", "datapacks", "Terralith.zip")); string(b) != "terralith" {
		t.Errorf("data pack = %q", b)
	}
	code, out = modrinthCall(t, mux, "van", "GET", "/api/datapacks", "")
	packs, _ := out["datapacks"].([]any)
	if code != 200 || len(packs) != 2 || out["world"] != "survival" {
		t.Fatalf("list = %d %v", code, out)
	}
	if first := packs[0].(map[string]any); first["name"] != "handmade" || first["folder"] != true {
		t.Errorf("first = %v", first)
	}
	if second := packs[1].(map[string]any); second["title"] != "The terralith project" || second["version"] != "2.0" {
		t.Errorf("second = %v", second)
	}
	for name, want := range map[string]int{"handmade": 200, "Terralith.zip": 200, "missing.zip": 404, ".realmkeeper": 400} {
		if code, _ := modrinthCall(t, mux, "van", "DELETE", "/api/datapacks/"+url.PathEscape(name), ""); code != want {
			t.Errorf("delete %s = %d, want %d", name, code, want)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(game, "survival", "datapacks")); len(entries) != 0 {
		t.Errorf("datapacks = %v", entries)
	}
	hy := newCapabilityTestApp(t, testServer("hy", "hytale"))
	if code, _ := modrinthCall(t, newTestMux(hy), "hy", "GET", "/api/datapacks", ""); code != 409 {
		t.Errorf("Hytale data packs = %d, want 409", code)
	}
}

func TestPinnedLoaders(t *testing.T) {
	fakeMinecraftServices(t)
	ctx := context.Background()
	if _, u, err := resolveFabricLoader(ctx, http.DefaultClient, "1.21.8", "0.18.0-beta"); err != nil || !strings.Contains(u, "/1.21.8/0.18.0-beta/1.1.0/") || fabricLoaderFromURL(u) != "0.18.0-beta" {
		t.Errorf("pinned Fabric = %q %v", u, err)
	}
	if _, _, err := resolveFabricLoader(ctx, http.DefaultClient, "1.21.8", "9.9.9"); err == nil {
		t.Error("an unknown Fabric loader resolved")
	}
	if li, err := pinnedForge("1.20.1", "47.2.0"); err != nil || !strings.HasSuffix(li.InstallerURL, "/1.20.1-47.2.0/forge-1.20.1-47.2.0-installer.jar") || li.ArgsFile != "libraries/net/minecraftforge/forge/1.20.1-47.2.0/unix_args.txt" {
		t.Errorf("pinned Forge = %+v %v", li, err)
	}
	if _, err := pinnedForge("1.16.5", "36.2.39"); err == nil {
		t.Error("Forge for 1.16.5 needs Java 8 and must be refused")
	}
	if li, err := pinnedNeoForge("21.1.77"); err != nil || li.MinecraftVersion != "1.21.1" || !strings.HasSuffix(li.InstallerURL, "/21.1.77/neoforge-21.1.77-installer.jar") {
		t.Errorf("pinned NeoForge = %+v %v", li, err)
	}
	for deps, want := range map[string]string{
		`{"minecraft":"1.21.1","fabric-loader":"0.16.5"}`:           "fabric 0.16.5",
		`{"minecraft":"1.20.1","forge":"47.2.0"}`:                   "forge 47.2.0",
		`{"minecraft":"1.21.1","neoforge":"21.1.77"}`:               "neoforge 21.1.77",
		`{"minecraft":"1.21.1","quilt-loader":"0.27.1"}`:            "quilt 0.27.1",
		`{"minecraft":"1.21.1"}`:                                    "error",
		`{"minecraft":"1.21.1","forge":"1","fabric-loader":"2"}`:    "error",
		`{"minecraft":"1.21.1","fabric-loader":"0.16.5; rm -rf /"}`: "error",
		`{"fabric-loader":"0.16.5"}`:                                "error",
	} {
		var idx mrpackIndex
		_ = json.Unmarshal([]byte(`{"dependencies":`+deps+`}`), &idx)
		_, variant, version, err := idx.target()
		got := variant + " " + version
		if err != nil {
			got = "error"
		}
		if got != want {
			t.Errorf("%s = %q, want %q", deps, got, want)
		}
	}
	for p, ok := range map[string]bool{"mods/a.jar": true, "config/x/y.toml": true, "../a": false, "/etc/passwd": false, `mods\a.jar`: false, "mods/../../a": false, ".hsm-minecraft.json": false, "": false} {
		if _, got := cleanPackPath(p); got != ok {
			t.Errorf("cleanPackPath(%q) = %v", p, got)
		}
	}
}
