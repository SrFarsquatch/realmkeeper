package main

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Modrinth browser for Minecraft servers: mods or plugins for servers with a
// loader, and data packs for every Minecraft server (mod packs are in
// modpack.go). Searches are limited to the server's loader and Minecraft
// version, and to projects that run on servers. Installed files are checked
// against Modrinth's SHA-512, and required dependencies are installed with
// them. What came from Modrinth is recorded in the game folder, so it travels
// with exports.

var (
	modrinthAPI = "https://api.modrinth.com/v2"
	// trustedModrinthDownload accepts only HTTPS downloads from Modrinth.
	trustedModrinthDownload = func(raw string) bool { return isHTTPSHost(raw, "modrinth.com") }
	modrinthTrackMu         sync.Mutex
)

const (
	modrinthTrackFile    = ".realmkeeper-mods.json"
	datapackTrackFile    = ".realmkeeper-datapacks.json"
	modrinthFileLimit    = 256 << 20
	modrinthMaxInstalled = 16 // a mod and its dependencies, per install
)

// modrinthLoaders are the Modrinth loader names each server type can run.
// Paper runs Spigot and Bukkit plugins; Quilt runs most Fabric mods.
var modrinthLoaders = map[string][]string{
	"paper":    {"paper", "spigot", "bukkit"},
	"fabric":   {"fabric"},
	"quilt":    {"quilt", "fabric"},
	"forge":    {"forge"},
	"neoforge": {"neoforge"},
}

type modrinthHit struct {
	ProjectID   string `json:"project_id"`
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Author      string `json:"author"`
	Downloads   int    `json:"downloads"`
	IconURL     string `json:"icon_url"`
}

type modrinthFile struct {
	Hashes   map[string]string `json:"hashes"`
	URL      string            `json:"url"`
	Filename string            `json:"filename"`
	Primary  bool              `json:"primary"`
	Size     int64             `json:"size"`
}

type modrinthVersion struct {
	ID            string         `json:"id"`
	ProjectID     string         `json:"project_id"`
	Name          string         `json:"name"`
	VersionNumber string         `json:"version_number"`
	VersionType   string         `json:"version_type"`
	DatePublished string         `json:"date_published"`
	GameVersions  []string       `json:"game_versions"`
	Loaders       []string       `json:"loaders"`
	Files         []modrinthFile `json:"files"`
	Dependencies  []struct {
		VersionID      string `json:"version_id"`
		ProjectID      string `json:"project_id"`
		DependencyType string `json:"dependency_type"`
	} `json:"dependencies"`
}

// modrinthInstall records a file installed from Modrinth.
type modrinthInstall struct {
	ProjectID     string `json:"projectId"`
	VersionID     string `json:"versionId"`
	VersionNumber string `json:"versionNumber"`
	Title         string `json:"title"`
	Filename      string `json:"filename"`
	SHA512        string `json:"sha512"`
}

// modrinthIDPattern accepts Modrinth IDs and project slugs.
var modrinthIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Kinds of Modrinth projects the browser installs.
const (
	modrinthKindMod      = "mod" // mods, or plugins on Paper
	modrinthKindDatapack = "datapack"
	modrinthKindModpack  = "modpack"
)

// modrinthTarget is what a server can run: its loaders and Minecraft version.
type modrinthTarget struct {
	Kind        string
	Loaders     []string
	GameVersion string // "" when the version is not known yet
	Dir         string // the mods, plugins or data packs folder
	Ext         string // the file type installed: .jar, or .zip for data packs
	GameDir     string
	TrackFile   string // records what was installed from Modrinth
}

func (a *App) modrinthTargetFor(s ServerSettings) (modrinthTarget, bool) {
	loaders, ok := modrinthLoaders[s.MinecraftVariant]
	folder, hasFolder := modFolderFor(s)
	if normalizeGameType(s.GameType) != "minecraft-java" || !ok || !hasFolder {
		return modrinthTarget{}, false
	}
	root := a.serverRoot(s)
	t := modrinthTarget{Kind: modrinthKindMod, Loaders: loaders, Dir: filepath.Join(root, folder.Dir), Ext: ".jar", GameDir: pathsFor(root).Game()}
	t.TrackFile = filepath.Join(t.GameDir, modrinthTrackFile)
	t.GameVersion = minecraftGameVersion(t.GameDir, s)
	return t, true
}

// datapackTargetFor is the data packs folder of the server's world. Any
// Minecraft server runs data packs; a pack added before the first start is
// there when the world is created, which world generation packs need.
func (a *App) datapackTargetFor(s ServerSettings) (modrinthTarget, bool) {
	if normalizeGameType(s.GameType) != "minecraft-java" {
		return modrinthTarget{}, false
	}
	game := pathsFor(a.serverRoot(s)).Game()
	return modrinthTarget{
		Kind: modrinthKindDatapack, Loaders: []string{"datapack"}, Ext: ".zip", GameDir: game,
		Dir:         filepath.Join(game, minecraftLevelName(game), "datapacks"),
		TrackFile:   filepath.Join(game, datapackTrackFile),
		GameVersion: minecraftGameVersion(game, s),
	}, true
}

// minecraftGameVersion is the installed Minecraft version, else the version
// the server was created for, else "" (latest, not installed yet).
func minecraftGameVersion(gameDir string, s ServerSettings) string {
	if info, ok := readMinecraftInstallInfo(gameDir); ok && info.Version != "" {
		return info.Version
	}
	if !isLatest(s.MinecraftVersion) {
		return s.MinecraftVersion
	}
	return ""
}

func modrinthRequest(ctx context.Context, client *http.Client, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, modrinthAPI+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", appUserAgent())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach Modrinth: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errors.New("not found on Modrinth")
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return errors.New("Modrinth is limiting requests; try again in a minute")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Modrinth returned HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
}

func modrinthClient() *http.Client {
	return loaderClient(&http.Client{Timeout: 5 * time.Minute}, "modrinth.com")
}

func readModrinthInstalls(gameDir string) map[string]modrinthInstall {
	return readInstallRecords(filepath.Join(gameDir, modrinthTrackFile))
}

func writeModrinthInstalls(gameDir string, installs map[string]modrinthInstall) error {
	return writeInstallRecords(filepath.Join(gameDir, modrinthTrackFile), installs)
}

func readInstallRecords(path string) map[string]modrinthInstall {
	out := map[string]modrinthInstall{}
	b, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

func writeInstallRecords(path string, installs map[string]modrinthInstall) error {
	b, err := json.MarshalIndent(installs, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(b, '\n'), 0o644)
}

// currentModrinthInstalls drops records whose file has since been removed.
func currentModrinthInstalls(t modrinthTarget) map[string]modrinthInstall {
	installs := readInstallRecords(t.TrackFile)
	for id, inst := range installs {
		if !fileExists(filepath.Join(t.Dir, inst.Filename)) {
			delete(installs, id)
		}
	}
	return installs
}

// modrinthRequestServer resolves the request's server and what it installs:
// kind is "mod" (the default), "datapack" or "modpack".
func (a *App) modrinthRequestServer(w http.ResponseWriter, r *http.Request, kind string) (*ServerManager, ServerSettings, modrinthTarget, bool) {
	m, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return nil, s, modrinthTarget{}, false
	}
	var t modrinthTarget
	switch kind {
	case "", modrinthKindMod:
		if !requireCapability(w, s, "The mod browser", func(c GameCapability) bool { return c.ModBrowser }) {
			return nil, s, t, false
		}
		t, _ = a.modrinthTargetFor(s)
	case modrinthKindDatapack:
		if !requireCapability(w, s, "Data packs", func(c GameCapability) bool { return c.DataPacks }) {
			return nil, s, t, false
		}
		t, _ = a.datapackTargetFor(s)
	case modrinthKindModpack:
		if !requireCapability(w, s, "Mod packs", func(c GameCapability) bool { return c.ModPacks }) {
			return nil, s, t, false
		}
		t, _ = a.modpackTargetFor(s)
	default:
		jsonError(w, 400, "kind must be mod, datapack or modpack")
		return nil, s, t, false
	}
	return m, s, t, true
}

var modrinthProjectTypes = map[string][]string{
	modrinthKindMod:      {"project_type:mod", "project_type:plugin"},
	modrinthKindDatapack: {"project_type:datapack"},
	modrinthKindModpack:  {"project_type:modpack"},
}

func modrinthFacets(t modrinthTarget) string {
	loaders := []string{}
	for _, l := range t.Loaders {
		loaders = append(loaders, "categories:"+l)
	}
	facets := [][]string{
		loaders,
		modrinthProjectTypes[t.Kind],
		{"server_side:required", "server_side:optional", "server_side:unknown"},
	}
	if t.GameVersion != "" {
		facets = append(facets, []string{"versions:" + t.GameVersion})
	}
	b, _ := json.Marshal(facets)
	return string(b)
}

func (a *App) handleModrinthSearch(w http.ResponseWriter, r *http.Request) {
	_, _, t, ok := a.modrinthRequestServer(w, r, r.URL.Query().Get("kind"))
	if !ok {
		return
	}
	writeModrinthSearch(w, r, t)
}

// writeModrinthSearch searches Modrinth for projects that fit t.
func writeModrinthSearch(w http.ResponseWriter, r *http.Request, t modrinthTarget) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) > 100 {
		query = query[:100]
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 || offset > 10000 {
		offset = 0
	}
	params := url.Values{"query": {query}, "limit": {"20"}, "offset": {strconv.Itoa(offset)}, "facets": {modrinthFacets(t)}}
	if query == "" {
		params.Set("index", "downloads")
	}
	var result struct {
		Hits      []modrinthHit `json:"hits"`
		TotalHits int           `json:"total_hits"`
	}
	if err := modrinthRequest(r.Context(), modrinthClient(), http.MethodGet, "/search?"+params.Encode(), nil, &result); err != nil {
		jsonError(w, 502, err.Error())
		return
	}
	installs := map[string]modrinthInstall{}
	if t.TrackFile != "" {
		installs = currentModrinthInstalls(t)
	}
	if t.Kind == modrinthKindModpack {
		if p, ok := readModpackRecord(t.GameDir); ok {
			installs[p.ProjectID] = modrinthInstall{}
		}
	}
	type hit struct {
		modrinthHit
		Installed bool `json:"installed"`
	}
	hits := make([]hit, 0, len(result.Hits))
	for _, h := range result.Hits {
		if !isHTTPSHost(h.IconURL, "modrinth.com") {
			h.IconURL = ""
		}
		_, installed := installs[h.ProjectID]
		hits = append(hits, hit{h, installed})
	}
	jsonOut(w, 200, map[string]any{"hits": hits, "total": result.TotalHits, "offset": offset, "gameVersion": t.GameVersion, "loaders": t.Loaders})
}

// compatibleVersions lists a project's versions for the server, newest first.
func compatibleVersions(ctx context.Context, client *http.Client, projectID string, t modrinthTarget) ([]modrinthVersion, error) {
	loaders, _ := json.Marshal(t.Loaders)
	params := url.Values{"loaders": {string(loaders)}}
	if t.GameVersion != "" {
		versions, _ := json.Marshal([]string{t.GameVersion})
		params.Set("game_versions", string(versions))
	}
	var versions []modrinthVersion
	if err := modrinthRequest(ctx, client, http.MethodGet, "/project/"+url.PathEscape(projectID)+"/version?"+params.Encode(), nil, &versions); err != nil {
		return nil, err
	}
	sort.SliceStable(versions, func(i, j int) bool { return versions[i].DatePublished > versions[j].DatePublished })
	return versions, nil
}

// pickModrinthVersion prefers the newest release, then the newest beta, then
// anything.
func pickModrinthVersion(versions []modrinthVersion) (modrinthVersion, bool) {
	for _, kind := range []string{"release", "beta", ""} {
		for _, v := range versions {
			if kind == "" || v.VersionType == kind {
				return v, true
			}
		}
	}
	return modrinthVersion{}, false
}

func (a *App) handleModrinthVersions(w http.ResponseWriter, r *http.Request) {
	_, _, t, ok := a.modrinthRequestServer(w, r, r.URL.Query().Get("kind"))
	if !ok {
		return
	}
	writeModrinthVersions(w, r, t)
}

// writeModrinthVersions lists a project's versions that fit t.
func writeModrinthVersions(w http.ResponseWriter, r *http.Request, t modrinthTarget) {
	project := r.PathValue("project")
	if !modrinthIDPattern.MatchString(project) {
		jsonError(w, 400, "invalid project")
		return
	}
	versions, err := compatibleVersions(r.Context(), modrinthClient(), project, t)
	if err != nil {
		jsonError(w, 502, err.Error())
		return
	}
	type view struct {
		ID            string   `json:"id"`
		Name          string   `json:"name"`
		VersionNumber string   `json:"versionNumber"`
		VersionType   string   `json:"versionType"`
		Published     string   `json:"published"`
		Size          int64    `json:"size"`
		Dependencies  int      `json:"dependencies"`
		GameVersions  []string `json:"gameVersions"`
		Loaders       []string `json:"loaders"`
	}
	out := []view{}
	for _, v := range versions {
		if len(out) == 15 {
			break
		}
		f, ok := primaryModrinthFile(v, t.Ext)
		if !ok {
			continue
		}
		deps := 0
		for _, d := range v.Dependencies {
			if d.DependencyType == "required" {
				deps++
			}
		}
		out = append(out, view{v.ID, v.Name, v.VersionNumber, v.VersionType, v.DatePublished, f.Size, deps, v.GameVersions, v.Loaders})
	}
	jsonOut(w, 200, map[string]any{"versions": out, "gameVersion": t.GameVersion})
}

// primaryModrinthFile is the version's file of type ext (".jar", ".zip" or
// ".mrpack"), preferring the one marked primary.
func primaryModrinthFile(v modrinthVersion, ext string) (modrinthFile, bool) {
	var found *modrinthFile
	for i, f := range v.Files {
		if strings.EqualFold(filepath.Ext(f.Filename), ext) && (found == nil || f.Primary) {
			found = &v.Files[i]
		}
	}
	if found == nil {
		return modrinthFile{}, false
	}
	return *found, true
}

func validModFilename(name, ext string) bool {
	return name != "" && name == filepath.Base(name) && !strings.HasPrefix(name, ".") && len(name) <= 200 &&
		strings.EqualFold(filepath.Ext(name), ext) && !strings.ContainsAny(name, "/\\\x00\r\n")
}

// downloadModrinthFile downloads a version's file of type ext into dir,
// checking its SHA-512, and returns the file name and checksum.
func downloadModrinthFile(ctx context.Context, client *http.Client, v modrinthVersion, dir, ext string) (string, string, error) {
	f, ok := primaryModrinthFile(v, ext)
	if !ok {
		return "", "", fmt.Errorf("%s has no %s file", v.Name, ext)
	}
	want := strings.ToLower(f.Hashes["sha512"])
	if !validModFilename(f.Filename, ext) || !trustedModrinthDownload(f.URL) || len(want) != 128 {
		return "", "", fmt.Errorf("%s has an unexpected download; not installed", v.Name)
	}
	if err := downloadSHA512(ctx, client, f.URL, filepath.Join(dir, f.Filename), want, modrinthFileLimit); err != nil {
		return "", "", err
	}
	return f.Filename, want, nil
}

// downloadSHA512 downloads rawURL to dst through a temporary file beside it,
// and keeps it only when its SHA-512 is want and it fits in limit bytes.
func downloadSHA512(ctx context.Context, client *http.Client, rawURL, dst, want string, limit int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", appUserAgent())
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".download-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	hash := sha512.New()
	n, copyErr := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(resp.Body, limit+1))
	if err := tmp.Close(); copyErr == nil {
		copyErr = err
	}
	switch {
	case copyErr != nil:
		return copyErr
	case n > limit:
		return fmt.Errorf("the file is larger than the %d MiB limit", limit>>20)
	case hex.EncodeToString(hash.Sum(nil)) != want:
		return errors.New("the downloaded file does not match Modrinth's SHA-512 checksum")
	}
	return os.Rename(tmp.Name(), dst)
}

type modrinthInstallRequest struct {
	Kind      string `json:"kind"` // "mod" (the default) or "datapack"
	ProjectID string `json:"projectId"`
	VersionID string `json:"versionId"`
	// Replace is a file in the mods folder the new version replaces (an
	// update of a mod that was not installed from the browser).
	Replace string `json:"replace"`
}

// installModrinth installs a version (or the best compatible one) and its
// required dependencies that are not installed yet.
func (a *App) installModrinth(ctx context.Context, m *ServerManager, t modrinthTarget, req modrinthInstallRequest) ([]modrinthInstall, error) {
	client := modrinthClient()
	modrinthTrackMu.Lock()
	defer modrinthTrackMu.Unlock()
	if err := os.MkdirAll(t.Dir, 0o755); err != nil {
		return nil, err
	}
	installs := currentModrinthInstalls(t)
	// A dependency may name an exact version; when that one does not fit
	// the server, the newest compatible version of the project is used.
	type job struct {
		project, version string
		dependency       bool
	}
	queue := []job{{req.ProjectID, req.VersionID, false}}
	done := map[string]bool{}
	var installed []modrinthInstall
	for len(queue) > 0 {
		j := queue[0]
		queue = queue[1:]
		if done[j.project] && j.project != "" {
			continue
		}
		if len(installed) >= modrinthMaxInstalled {
			return installed, errors.New("stopped after 16 files; the dependency chain is too long")
		}
		var v modrinthVersion
		if j.version != "" {
			if err := modrinthRequest(ctx, client, http.MethodGet, "/version/"+url.PathEscape(j.version), nil, &v); err != nil {
				return installed, err
			}
			if !modrinthFits(v, t) {
				if !j.dependency || !modrinthIDPattern.MatchString(v.ProjectID) {
					return installed, fmt.Errorf("%s %s does not support this server's loader and Minecraft version", v.Name, v.VersionNumber)
				}
				j.project, j.version = v.ProjectID, ""
			}
		}
		if j.version == "" || v.ID == "" {
			versions, err := compatibleVersions(ctx, client, j.project, t)
			if err != nil {
				return installed, err
			}
			var ok bool
			if v, ok = pickModrinthVersion(versions); !ok {
				what := "this project"
				if j.dependency {
					what = "the required dependency " + j.project
				}
				return installed, fmt.Errorf("no version of %s supports the server's loader and Minecraft version", what)
			}
		}
		if done[v.ProjectID] {
			continue
		}
		done[v.ProjectID] = true
		var project struct {
			Title string `json:"title"`
		}
		if err := modrinthRequest(ctx, client, http.MethodGet, "/project/"+url.PathEscape(v.ProjectID), nil, &project); err != nil {
			project.Title = v.Name
		}
		name, sum, err := downloadModrinthFile(ctx, client, v, t.Dir, t.Ext)
		if err != nil {
			return installed, fmt.Errorf("%s: %w", project.Title, err)
		}
		// An update replaces the previous file of the same project.
		previous := installs[v.ProjectID].Filename
		if v.ProjectID == req.ProjectID && previous == "" && validModFilename(req.Replace, t.Ext) {
			previous = req.Replace
		}
		if previous != "" && previous != name {
			_ = os.Remove(filepath.Join(t.Dir, previous))
		}
		inst := modrinthInstall{ProjectID: v.ProjectID, VersionID: v.ID, VersionNumber: v.VersionNumber, Title: project.Title, Filename: name, SHA512: sum}
		installs[v.ProjectID] = inst
		installed = append(installed, inst)
		m.appendLog("controller", "Installed "+inst.Title+" "+inst.VersionNumber+" ("+name+") from Modrinth")
		for _, d := range v.Dependencies {
			if d.DependencyType != "required" {
				continue
			}
			if _, have := installs[d.ProjectID]; have && d.ProjectID != "" {
				continue
			}
			if modrinthIDPattern.MatchString(d.ProjectID) || modrinthIDPattern.MatchString(d.VersionID) {
				queue = append(queue, job{d.ProjectID, d.VersionID, true})
			}
		}
		if err := writeInstallRecords(t.TrackFile, installs); err != nil {
			return installed, err
		}
	}
	return installed, nil
}

// modrinthFits reports whether a version runs on the server.
func modrinthFits(v modrinthVersion, t modrinthTarget) bool {
	loaderOK := false
	for _, l := range v.Loaders {
		for _, want := range t.Loaders {
			loaderOK = loaderOK || l == want
		}
	}
	if !loaderOK {
		return false
	}
	if t.GameVersion == "" {
		return true
	}
	for _, g := range v.GameVersions {
		if g == t.GameVersion {
			return true
		}
	}
	return false
}

func (a *App) handleModrinthInstall(w http.ResponseWriter, r *http.Request) {
	var req modrinthInstallRequest
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	if req.Kind == modrinthKindModpack {
		jsonError(w, 400, "mod packs are installed with POST /api/modrinth/modpack")
		return
	}
	m, _, t, ok := a.modrinthRequestServer(w, r, req.Kind)
	if !ok {
		return
	}
	if !modrinthIDPattern.MatchString(req.ProjectID) || (req.VersionID != "" && !modrinthIDPattern.MatchString(req.VersionID)) || (req.Replace != "" && !validModFilename(req.Replace, t.Ext)) {
		jsonError(w, 400, "invalid project, version or file")
		return
	}
	installed, err := a.installModrinth(r.Context(), m, t, req)
	if err != nil {
		if len(installed) > 0 {
			err = fmt.Errorf("%w (installed before the error: %d file(s))", err, len(installed))
		}
		jsonError(w, 502, err.Error())
		return
	}
	message := "Installed " + installed[0].Title + " " + installed[0].VersionNumber
	if n := len(installed) - 1; n > 0 {
		message += fmt.Sprintf(" and %d required dependenc%s", n, map[bool]string{true: "y", false: "ies"}[n == 1])
	}
	if m.IsRunning() && t.Kind == modrinthKindDatapack {
		message += ". Run /reload in the console or restart the server to load it"
	} else if m.IsRunning() {
		message += ". Restart the server to load it"
	}
	jsonOut(w, 200, map[string]any{"installed": installed, "message": message})
}

// handleModrinthUpdates asks Modrinth for newer versions of every jar in the
// mods folder, including ones that were uploaded rather than installed from
// the browser, by their SHA-512.
func (a *App) handleModrinthUpdates(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	if kind == modrinthKindModpack {
		jsonError(w, 400, "mod pack updates are listed with the pack's versions")
		return
	}
	_, _, t, ok := a.modrinthRequestServer(w, r, kind)
	if !ok {
		return
	}
	entries, _ := os.ReadDir(t.Dir)
	byHash := map[string]string{}
	hashes := []string{}
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), t.Ext) || len(hashes) >= 500 {
			continue
		}
		f, err := os.Open(filepath.Join(t.Dir, e.Name()))
		if err != nil {
			continue
		}
		h := sha512.New()
		_, err = io.Copy(h, io.LimitReader(f, modrinthFileLimit))
		f.Close()
		if err != nil {
			continue
		}
		sum := hex.EncodeToString(h.Sum(nil))
		byHash[sum] = e.Name()
		hashes = append(hashes, sum)
	}
	type update struct {
		File          string `json:"file"`
		ProjectID     string `json:"projectId"`
		VersionID     string `json:"versionId"`
		VersionNumber string `json:"versionNumber"`
		Title         string `json:"title"`
	}
	updates := []update{}
	if len(hashes) > 0 {
		body := map[string]any{"hashes": hashes, "algorithm": "sha512", "loaders": t.Loaders}
		if t.GameVersion != "" {
			body["game_versions"] = []string{t.GameVersion}
		}
		var latest map[string]modrinthVersion
		if err := modrinthRequest(r.Context(), modrinthClient(), http.MethodPost, "/version_files/update", body, &latest); err != nil {
			jsonError(w, 502, err.Error())
			return
		}
		installs := currentModrinthInstalls(t)
		for hash, v := range latest {
			file, ok := byHash[hash]
			newest, hasFile := primaryModrinthFile(v, t.Ext)
			if !ok || !hasFile || strings.EqualFold(newest.Hashes["sha512"], hash) {
				continue
			}
			title := installs[v.ProjectID].Title
			if title == "" {
				title = strings.TrimSuffix(file, filepath.Ext(file))
			}
			updates = append(updates, update{file, v.ProjectID, v.ID, v.VersionNumber, title})
		}
	}
	sort.Slice(updates, func(i, j int) bool { return updates[i].File < updates[j].File })
	jsonOut(w, 200, map[string]any{"updates": updates, "checked": len(hashes)})
}
