package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Modrinth mod packs (.mrpack). A pack is a zip with modrinth.index.json,
// which names the Minecraft version, the loader build and every file with its
// download and SHA-512, plus overrides/ and server-overrides/ folders that are
// copied into the server (configs, and sometimes mods).
//
// A server is created from a pack, or a pack is installed or updated on an
// existing server of the same Minecraft version and loader. Everything is
// downloaded and checked into a staging folder first; only then is the loader
// installed (when the pack names another build) and the files moved into the
// game folder, so a failed download changes nothing. Files a previous version
// of the pack installed and the new one no longer has are removed; files the
// owner added are left alone.

// mrpackLoaderKeys maps a server variant to the dependency name a pack uses
// for its loader.
var mrpackLoaderKeys = map[string]string{"fabric": "fabric-loader", "quilt": "quilt-loader", "forge": "forge", "neoforge": "neoforge"}

// mrpackDownloadHosts are the hosts the .mrpack format allows file downloads
// from. Every file is also checked against its SHA-512.
var mrpackDownloadHosts = []string{"cdn.modrinth.com", "github.com", "raw.githubusercontent.com", "gitlab.com"}

// trustedMrpackDownload accepts a pack file's download URL; tests replace it.
var trustedMrpackDownload = func(raw string) bool {
	for _, host := range mrpackDownloadHosts {
		if isHTTPSHost(raw, host) {
			return true
		}
	}
	return false
}

const (
	modpackTrackFile = ".realmkeeper-modpack.json"
	mrpackLimit      = 1 << 30 // the .mrpack itself
	mrpackMaxFiles   = 2000
	// mrpackOverrideLimit bounds what overrides may unpack to.
	mrpackOverrideLimit   = 2 << 30
	mrpackMaxOverrides    = 50000
	mrpackDownloadWorkers = 4
)

// modpackRef names a pack version on Modrinth.
type modpackRef struct {
	ProjectID string `json:"projectId"`
	VersionID string `json:"versionId"`
}

func (r modpackRef) valid() bool {
	return modrinthIDPattern.MatchString(r.ProjectID) && modrinthIDPattern.MatchString(r.VersionID)
}

// modpackRecord is the installed pack, kept in the game folder.
type modpackRecord struct {
	ProjectID        string `json:"projectId"`
	VersionID        string `json:"versionId"`
	VersionNumber    string `json:"versionNumber"`
	Title            string `json:"title"`
	MinecraftVersion string `json:"minecraftVersion"`
	Loader           string `json:"loader"`
	LoaderVersion    string `json:"loaderVersion"`
	// Files are the pack's mods and other listed files, relative to the
	// game folder, removed when a later version drops them.
	Files       []string `json:"files"`
	InstalledAt string   `json:"installedAt"`
}

func readModpackRecord(gameDir string) (modpackRecord, bool) {
	var rec modpackRecord
	b, err := os.ReadFile(filepath.Join(gameDir, modpackTrackFile))
	if err != nil || json.Unmarshal(b, &rec) != nil || rec.ProjectID == "" {
		return modpackRecord{}, false
	}
	return rec, true
}

func writeModpackRecord(gameDir string, rec modpackRecord) error {
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(gameDir, modpackTrackFile), append(b, '\n'), 0o644)
}

// modpackTargetFor limits pack searches to the server's loader and version.
func (a *App) modpackTargetFor(s ServerSettings) (modrinthTarget, bool) {
	if _, ok := mrpackLoaderKeys[s.MinecraftVariant]; !ok || normalizeGameType(s.GameType) != "minecraft-java" {
		return modrinthTarget{}, false
	}
	game := pathsFor(a.serverRoot(s)).Game()
	return modrinthTarget{Kind: modrinthKindModpack, Loaders: []string{s.MinecraftVariant}, Ext: ".mrpack", GameDir: game, GameVersion: minecraftGameVersion(game, s)}, true
}

// anyModpackTarget is every pack a new server can be created from.
var anyModpackTarget = modrinthTarget{Kind: modrinthKindModpack, Loaders: []string{"fabric", "quilt", "forge", "neoforge"}, Ext: ".mrpack"}

type mrpackFile struct {
	Path   string            `json:"path"`
	Hashes map[string]string `json:"hashes"`
	Env    *struct {
		Server string `json:"server"`
	} `json:"env"`
	Downloads []string `json:"downloads"`
	FileSize  int64    `json:"fileSize"`
}

type mrpackIndex struct {
	FormatVersion int               `json:"formatVersion"`
	Game          string            `json:"game"`
	VersionID     string            `json:"versionId"`
	Name          string            `json:"name"`
	Files         []mrpackFile      `json:"files"`
	Dependencies  map[string]string `json:"dependencies"`
}

// target is the pack's Minecraft version, loader and loader build.
func (idx mrpackIndex) target() (mc, variant, loaderVersion string, err error) {
	mc = idx.Dependencies["minecraft"]
	if !safeVersionPart.MatchString(mc) {
		return "", "", "", errors.New("the pack does not name a Minecraft version")
	}
	for _, v := range []string{"neoforge", "forge", "quilt", "fabric"} {
		if version, ok := idx.Dependencies[mrpackLoaderKeys[v]]; ok {
			if variant != "" {
				return "", "", "", errors.New("the pack names more than one loader")
			}
			if !safeVersionPart.MatchString(version) {
				return "", "", "", errors.New("the pack names an invalid loader version")
			}
			variant, loaderVersion = v, version
		}
	}
	if variant == "" {
		return "", "", "", errors.New("the pack needs no mod loader; Realmkeeper installs packs for Fabric, Quilt, Forge and NeoForge")
	}
	return mc, variant, loaderVersion, nil
}

// cleanPackPath checks a path from a pack: relative, inside the game folder,
// and not one of Realmkeeper's own records.
func cleanPackPath(p string) (string, bool) {
	if p == "" || strings.ContainsAny(p, "\\\x00\r\n") || strings.HasPrefix(p, "/") {
		return "", false
	}
	clean := path.Clean(p)
	if !filepath.IsLocal(filepath.FromSlash(clean)) {
		return "", false
	}
	switch clean {
	case minecraftInstallInfoFile, modrinthTrackFile, datapackTrackFile, modpackTrackFile:
		return "", false
	}
	return clean, true
}

// modpackVariantFor reads which server a pack version needs from its
// Modrinth metadata: the loader, and the Minecraft version. A pack listed
// for Fabric and Quilt is a Fabric pack that also runs on Quilt. The pack's
// own index has the final say when it is installed.
func modpackVariantFor(v modrinthVersion) (variant, mc string, err error) {
	for _, want := range []string{"neoforge", "forge", "fabric", "quilt"} {
		for _, l := range v.Loaders {
			if l == want && variant == "" {
				variant = want
			}
		}
	}
	if variant == "" {
		return "", "", errors.New("this pack is not for Fabric, Quilt, Forge or NeoForge")
	}
	if len(v.GameVersions) == 0 || !safeVersionPart.MatchString(v.GameVersions[0]) {
		return "", "", errors.New("this pack version names no Minecraft version")
	}
	return variant, v.GameVersions[0], nil
}

func fetchModpackVersion(ctx context.Context, client *http.Client, ref modpackRef) (modrinthVersion, error) {
	var v modrinthVersion
	if err := modrinthRequest(ctx, client, http.MethodGet, "/version/"+url.PathEscape(ref.VersionID), nil, &v); err != nil {
		return v, err
	}
	if v.ProjectID != ref.ProjectID {
		return v, errors.New("that version belongs to another Modrinth project")
	}
	return v, nil
}

// resolveNewModpack fills in the loader and Minecraft version of a server
// created from a pack.
func resolveNewModpack(ctx context.Context, req *newServerRequest) error {
	ref := *req.MinecraftModpack
	if !ref.valid() {
		return errors.New("invalid mod pack")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	v, err := fetchModpackVersion(ctx, modrinthClient(), ref)
	if err != nil {
		return fmt.Errorf("could not read the mod pack: %w", err)
	}
	if _, ok := primaryModrinthFile(v, ".mrpack"); !ok {
		return errors.New("this Modrinth version has no .mrpack file")
	}
	req.MinecraftVariant, req.MinecraftVersion, err = modpackVariantFor(v)
	return err
}

// mrpackClient follows redirects only between the hosts packs download from
// (GitHub downloads redirect to githubusercontent.com).
func mrpackClient() *http.Client {
	c := &http.Client{Timeout: 10 * time.Minute}
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if !trustedMrpackDownload(req.URL.String()) && !isHTTPSHost(req.URL.String(), "githubusercontent.com") && !isHTTPSHost(req.URL.String(), "modrinth.com") {
			return fmt.Errorf("refusing a redirect to %s", req.URL.Host)
		}
		return nil
	}
	return c
}

// installModpack installs a pack version on the server. A server without
// files gets the pack's Minecraft version and loader first; an installed
// server must already run that Minecraft version and loader, and its loader
// is reinstalled when the pack names another build.
func (m *ServerManager) installModpack(ctx context.Context, variant string, ref modpackRef) (modpackRecord, error) {
	client := modrinthClient()
	p := pathsFor(m.rootDir)
	gameDir := p.Game()
	work := filepath.Join(p.Tmp(), "modpack")
	_ = os.RemoveAll(work)
	if err := os.MkdirAll(filepath.Join(work, "files"), 0o755); err != nil {
		return modpackRecord{}, err
	}
	defer os.RemoveAll(work)

	v, err := fetchModpackVersion(ctx, client, ref)
	if err != nil {
		return modpackRecord{}, err
	}
	f, ok := primaryModrinthFile(v, ".mrpack")
	want := strings.ToLower(f.Hashes["sha512"])
	if !ok || !trustedModrinthDownload(f.URL) || len(want) != 128 {
		return modpackRecord{}, errors.New("this Modrinth version has no usable .mrpack file")
	}
	var project struct {
		Title string `json:"title"`
	}
	if err := modrinthRequest(ctx, client, http.MethodGet, "/project/"+url.PathEscape(ref.ProjectID), nil, &project); err != nil || project.Title == "" {
		project.Title = v.Name
	}
	m.appendLog("installer", fmt.Sprintf("Downloading the mod pack %s %s (%d MB)…", project.Title, v.VersionNumber, f.Size>>20))
	packFile := filepath.Join(work, "pack.mrpack")
	if err := downloadSHA512(ctx, client, f.URL, packFile, want, mrpackLimit); err != nil {
		return modpackRecord{}, fmt.Errorf("download the pack: %w", err)
	}
	zr, err := zip.OpenReader(packFile)
	if err != nil {
		return modpackRecord{}, fmt.Errorf("the pack is not a valid .mrpack: %w", err)
	}
	defer zr.Close()
	idx, err := readMrpackIndex(&zr.Reader)
	if err != nil {
		return modpackRecord{}, err
	}
	mc, packVariant, loaderVersion, err := idx.target()
	if err != nil {
		return modpackRecord{}, err
	}
	info, hasInfo := readMinecraftInstallInfo(gameDir)
	installed := hasInfo && (MinecraftAdapter{}).Installed(m.rootDir)
	if packVariant != variant && installed {
		return modpackRecord{}, fmt.Errorf("the pack is for %s, but this server runs %s", minecraftVariantNames[packVariant], minecraftVariantNames[variant])
	}
	variant = packVariant
	if installed && info.Version != mc {
		return modpackRecord{}, fmt.Errorf("the pack is for Minecraft %s, but this server runs %s; create a new server from the pack instead", mc, info.Version)
	}

	stage := filepath.Join(work, "files")
	files, err := m.downloadPackFiles(ctx, idx, stage)
	if err != nil {
		return modpackRecord{}, err
	}
	overrideMods, err := extractPackOverrides(&zr.Reader, stage)
	if err != nil {
		return modpackRecord{}, err
	}
	files = append(files, overrideMods...)

	if !installed || info.LoaderVersion != loaderVersion {
		m.appendLog("installer", fmt.Sprintf("Installing %s %s for Minecraft %s, as the pack requires…", minecraftVariantNames[variant], loaderVersion, mc))
		if err := m.installMinecraftPinned(ctx, mc, variant, loaderVersion); err != nil {
			return modpackRecord{}, err
		}
	}
	previous, _ := readModpackRecord(gameDir)
	if err := moveStagedFiles(stage, gameDir); err != nil {
		return modpackRecord{}, err
	}
	keep := map[string]bool{}
	for _, f := range files {
		keep[f] = true
	}
	for _, old := range previous.Files {
		if clean, ok := cleanPackPath(old); ok && !keep[clean] {
			_ = os.Remove(filepath.Join(gameDir, filepath.FromSlash(clean)))
		}
	}
	sort.Strings(files)
	rec := modpackRecord{
		ProjectID: ref.ProjectID, VersionID: v.ID, VersionNumber: v.VersionNumber, Title: project.Title,
		MinecraftVersion: mc, Loader: variant, LoaderVersion: loaderVersion, Files: files,
		InstalledAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := writeModpackRecord(gameDir, rec); err != nil {
		return rec, err
	}
	m.appendLog("installer", fmt.Sprintf("Installed the mod pack %s %s: %d files", rec.Title, rec.VersionNumber, len(files)))
	return rec, nil
}

func readMrpackIndex(zr *zip.Reader) (mrpackIndex, error) {
	var idx mrpackIndex
	for _, f := range zr.File {
		if f.Name != "modrinth.index.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return idx, err
		}
		defer rc.Close()
		if err := json.NewDecoder(io.LimitReader(rc, 16<<20)).Decode(&idx); err != nil {
			return idx, fmt.Errorf("read the pack index: %w", err)
		}
		if idx.FormatVersion != 1 || idx.Game != "minecraft" {
			return idx, errors.New("the pack is not a Minecraft .mrpack this version understands")
		}
		if len(idx.Files) > mrpackMaxFiles {
			return idx, fmt.Errorf("the pack lists more than %d files", mrpackMaxFiles)
		}
		return idx, nil
	}
	return idx, errors.New("the pack has no modrinth.index.json")
}

// downloadPackFiles downloads the pack's server files into stage and returns
// their paths. Client-only files are skipped.
func (m *ServerManager) downloadPackFiles(ctx context.Context, idx mrpackIndex, stage string) ([]string, error) {
	type job struct {
		rel, url, sha string
	}
	var jobs []job
	for _, f := range idx.Files {
		if f.Env != nil && f.Env.Server == "unsupported" {
			continue
		}
		rel, ok := cleanPackPath(f.Path)
		sha := strings.ToLower(f.Hashes["sha512"])
		if !ok || len(sha) != 128 || strings.Trim(sha, "0123456789abcdef") != "" {
			return nil, fmt.Errorf("the pack lists an invalid file %q", f.Path)
		}
		if f.FileSize > modrinthFileLimit {
			return nil, fmt.Errorf("%s is larger than the 256 MiB limit", rel)
		}
		source := ""
		for _, d := range f.Downloads {
			if trustedMrpackDownload(d) {
				source = d
				break
			}
		}
		if source == "" {
			return nil, fmt.Errorf("%s has no download from a host packs may use", rel)
		}
		jobs = append(jobs, job{rel, source, sha})
	}
	m.appendLog("installer", fmt.Sprintf("Downloading %d server files from the pack…", len(jobs)))
	client := mrpackClient()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	var firstErr error
	done := 0
	queue := make(chan job)
	var wg sync.WaitGroup
	for range mrpackDownloadWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range queue {
				dst := filepath.Join(stage, filepath.FromSlash(j.rel))
				err := os.MkdirAll(filepath.Dir(dst), 0o755)
				if err == nil {
					err = downloadSHA512(ctx, client, j.url, dst, j.sha, modrinthFileLimit)
				}
				mu.Lock()
				if err != nil && firstErr == nil {
					firstErr = fmt.Errorf("%s: %w", j.rel, err)
					cancel()
				}
				done++
				if err == nil && (done%20 == 0 || done == len(jobs)) {
					m.appendLog("installer", fmt.Sprintf("Downloaded %d of %d files", done, len(jobs)))
				}
				mu.Unlock()
			}
		}()
	}
	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		queue <- j
	}
	close(queue)
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	paths := make([]string, 0, len(jobs))
	for _, j := range jobs {
		paths = append(paths, j.rel)
	}
	return paths, nil
}

// extractPackOverrides unpacks overrides/, then server-overrides/ (which
// wins), into stage. It returns the override files in mods/, which belong to
// the pack like its listed files; other overrides (configs) are kept on
// later updates unless the new version overrides them again.
func extractPackOverrides(zr *zip.Reader, stage string) ([]string, error) {
	var total int64
	count := 0
	seen := map[string]bool{}
	var mods []string
	for _, prefix := range []string{"overrides/", "server-overrides/"} {
		for _, f := range zr.File {
			name, ok := strings.CutPrefix(f.Name, prefix)
			if !ok || name == "" || strings.HasSuffix(name, "/") || f.FileInfo().IsDir() {
				continue
			}
			if !f.Mode().IsRegular() {
				continue // links and devices
			}
			rel, ok := cleanPackPath(name)
			if !ok {
				return nil, fmt.Errorf("the pack has an unsafe override path %q", f.Name)
			}
			if count++; count > mrpackMaxOverrides {
				return nil, errors.New("the pack has too many override files")
			}
			dst := filepath.Join(stage, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return nil, err
			}
			n, err := extractZipFile(f, dst, mrpackOverrideLimit-total)
			if err != nil {
				return nil, fmt.Errorf("unpack %s: %w", rel, err)
			}
			total += n
			if strings.HasPrefix(rel, "mods/") && !seen[rel] {
				seen[rel] = true
				mods = append(mods, rel)
			}
		}
	}
	return mods, nil
}

func extractZipFile(f *zip.File, dst string, limit int64) (int64, error) {
	rc, err := f.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, io.LimitReader(rc, limit+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > limit {
		err = errors.New("the pack's overrides unpack to more than 2 GiB")
	}
	return n, err
}

// moveStagedFiles moves every staged file into gameDir, replacing what is
// there.
func moveStagedFiles(stage, gameDir string) error {
	return filepath.WalkDir(stage, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(stage, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(gameDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		_ = os.Remove(dst) // an older file; a folder with contents stays and fails the rename
		return os.Rename(p, dst)
	})
}

// startModpackJob installs a pack in the background, like the installers,
// with progress in the console.
func (m *ServerManager) startModpackJob(variant string, ref modpackRef, done func(modpackRecord)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return errors.New("stop the server before installing a mod pack")
	}
	if m.installBusy {
		return errors.New("an install job is already running")
	}
	m.installBusy = true
	go func() {
		defer func() { m.mu.Lock(); m.installBusy = false; m.mu.Unlock() }()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
		defer cancel()
		rec, err := m.installModpack(ctx, variant, ref)
		if err != nil {
			m.appendLog("installer", "Mod pack installation failed: "+err.Error())
			return
		}
		if done != nil {
			done(rec)
		}
	}()
	return nil
}

// handleModpackSearch and handleModpackVersions list packs a new server can
// be created from.
func (a *App) handleModpackSearch(w http.ResponseWriter, r *http.Request) {
	writeModrinthSearch(w, r, anyModpackTarget)
}

func (a *App) handleModpackVersions(w http.ResponseWriter, r *http.Request) {
	writeModrinthVersions(w, r, anyModpackTarget)
}

// handleServerModpack reports the server's installed pack.
func (a *App) handleServerModpack(w http.ResponseWriter, r *http.Request) {
	m, s, t, ok := a.modrinthRequestServer(w, r, modrinthKindModpack)
	if !ok {
		return
	}
	m.mu.Lock()
	busy := m.installBusy
	m.mu.Unlock()
	out := map[string]any{"pack": nil, "busy": busy, "gameVersion": t.GameVersion, "loader": s.MinecraftVariant}
	if rec, ok := readModpackRecord(t.GameDir); ok {
		out["pack"] = rec
	}
	jsonOut(w, 200, out)
}

// handleInstallModpack installs or updates a pack on a stopped server. An
// installed server is snapshotted first.
func (a *App) handleInstallModpack(w http.ResponseWriter, r *http.Request) {
	m, s, _, ok := a.modrinthRequestServer(w, r, modrinthKindModpack)
	if !ok {
		return
	}
	var ref modpackRef
	if err := decodeJSON(r, &ref); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	if !ref.valid() {
		jsonError(w, 400, "invalid project or version")
		return
	}
	if m.IsRunning() {
		jsonError(w, 409, "stop the server before installing a mod pack")
		return
	}
	snapshot := ""
	if m.Installed() {
		var err error
		if snapshot, _, err = a.createSnapshot(m, s); err != nil {
			jsonError(w, 500, "could not take a snapshot first: "+err.Error())
			return
		}
	}
	if err := m.startModpackJob(s.MinecraftVariant, ref, nil); err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	message := "Mod pack installation started; follow progress in Console"
	if snapshot != "" {
		message += ". Snapshot taken first: " + snapshot
	}
	jsonOut(w, 202, map[string]any{"ok": true, "snapshot": snapshot, "message": message})
}

// setMinecraftServerType records the loader and Minecraft version a pack
// installed on a new server.
func (a *App) setMinecraftServerType(id, variant, version string) error {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	for i := range a.cfg.Servers {
		if a.cfg.Servers[i].ID == id {
			if a.cfg.Servers[i].MinecraftVariant == variant && a.cfg.Servers[i].MinecraftVersion == version {
				return nil
			}
			a.cfg.Servers[i].MinecraftVariant, a.cfg.Servers[i].MinecraftVersion = variant, version
			return a.saveConfigLocked()
		}
	}
	return errors.New("server not found")
}
