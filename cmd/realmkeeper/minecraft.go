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
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var minecraftVersionManifestURL = "https://piston-meta.mojang.com/mc/game/version_manifest_v2.json"

type MinecraftAdapter struct{}

func (MinecraftAdapter) GameType() string { return "minecraft-java" }

func (MinecraftAdapter) Rules() GameRules {
	min, max := minecraftPortRange()
	return GameRules{GameModes: []string{"Survival", "Adventure", "Creative", "Spectator"}, ModeLabel: "Game mode", DefaultMOTD: "A Minecraft Server", DefaultMaxPlayers: 20, PlayerLimit: 1000, JVMMemory: true, Whitelist: true, PortMin: min, PortMax: max}
}

func (MinecraftAdapter) ValidateNew(req *newServerRequest) error {
	if req.MinecraftVariant == "" {
		req.MinecraftVariant = "vanilla"
	}
	if !validMinecraftVariant(req.MinecraftVariant) {
		return errors.New("Minecraft server type must be vanilla, paper, fabric, quilt, forge or neoforge")
	}
	if !req.MinecraftEulaAccepted {
		return errors.New("you must accept the Minecraft EULA before creating this server")
	}
	if req.MinecraftVersion == "" {
		req.MinecraftVersion = "latest"
	}
	if len(req.MinecraftVersion) > 32 || strings.ContainsAny(req.MinecraftVersion, "\r\n\x00") {
		return errors.New("invalid Minecraft version")
	}
	return nil
}

// MergeSettings keeps the version, variant and EULA consent fixed: they
// describe the installed files and change only through a reinstall.
func (MinecraftAdapter) MergeSettings(in *ServerSettings, current ServerSettings) error {
	keepMinecraftFields(in, current)
	keepDragonwildsFields(in, current)
	keepTerrariaFields(in, current)
	return nil
}

// ApplySettings has nothing to write: PrepareLaunch passes memory on the java
// command line and writes server.properties before each start.
func (MinecraftAdapter) ApplySettings(*App, *ServerManager, ServerSettings) error { return nil }

func (MinecraftAdapter) InstallStatus(app *App, s ServerSettings) map[string]any {
	return map[string]any{"game": s.GameType, "version": s.MinecraftVersion, "variant": s.MinecraftVariant, "modpack": s.MinecraftModpack, "eulaAccepted": s.MinecraftEulaAccepted, "paths": map[string]string{"game": pathsFor(app.serverRoot(s)).Game()}}
}

func (MinecraftAdapter) Installed(rootDir string) bool {
	gameDir := pathsFor(rootDir).Game()
	b, err := os.ReadFile(filepath.Join(gameDir, "eula.txt"))
	if err != nil || !minecraftServerFilesPresent(gameDir) {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.EqualFold(strings.TrimSpace(line), "eula=true") {
			return true
		}
	}
	return false
}

func (MinecraftAdapter) PrepareLaunch(app *App, settings ServerSettings) ([]string, error) {
	if !settings.MinecraftEulaAccepted {
		return nil, errors.New("Minecraft EULA must be accepted before starting this server")
	}
	if !validMemory(settings.MemoryMin) || !validMemory(settings.MemoryMax) {
		return nil, errors.New("invalid Minecraft JVM memory settings")
	}
	if err := checkMinecraftJava(pathsFor(app.serverRoot(settings)).Game(), detectJavaMajor); err != nil {
		return nil, err
	}
	if err := writeMinecraftProperties(app.serverRoot(settings), settings); err != nil {
		return nil, err
	}
	memory := []string{"-Xms" + settings.MemoryMin, "-Xmx" + settings.MemoryMax}
	if info, ok := readMinecraftInstallInfo(pathsFor(app.serverRoot(settings)).Game()); ok && info.ArgsFile != "" {
		// Forge and NeoForge: the installer's argument file names the
		// classpath and main class.
		return append(memory, "@"+info.ArgsFile, "nogui"), nil
	}
	return append(memory, "-jar", minecraftLaunchJar(settings.MinecraftVariant), "nogui"), nil
}

// minecraftServerFilesPresent reports whether a server's launch files exist:
// a jar to run, or the argument file Forge and NeoForge start from.
func minecraftServerFilesPresent(gameDir string) bool {
	if info, ok := readMinecraftInstallInfo(gameDir); ok && info.ArgsFile != "" {
		return fileExists(filepath.Join(gameDir, filepath.FromSlash(info.ArgsFile)))
	}
	for _, jar := range []string{"server.jar", fabricLauncherJar, quiltLauncherJar} {
		if fileExists(filepath.Join(gameDir, jar)) {
			return true
		}
	}
	return false
}

func (MinecraftAdapter) BuildProcess(rootDir string, args []string) (ProcessLaunch, error) {
	gameDir := pathsFor(rootDir).Game()
	jar := "server.jar"
	for i, arg := range args {
		if arg == "-jar" && i+1 < len(args) {
			jar = args[i+1]
		}
		if file, ok := strings.CutPrefix(arg, "@"); ok {
			jar = filepath.FromSlash(file)
		}
	}
	if !filepath.IsLocal(jar) || !fileExists(filepath.Join(gameDir, jar)) {
		return ProcessLaunch{}, errors.New("Minecraft " + filepath.ToSlash(jar) + " is missing; install the server files first")
	}
	return ProcessLaunch{Path: "java", Args: append([]string(nil), args...), Dir: gameDir}, nil
}

func (MinecraftAdapter) RequestStop(_ int, stdin io.WriteCloser) error {
	if stdin == nil {
		return errors.New("server stdin is unavailable")
	}
	_, err := io.WriteString(stdin, "stop\n")
	return err
}

type minecraftManifest struct {
	Latest struct {
		Release string `json:"release"`
	} `json:"latest"`
	Versions []struct {
		ID   string `json:"id"`
		URL  string `json:"url"`
		Type string `json:"type"`
	} `json:"versions"`
}

type minecraftVersionMeta struct {
	Downloads struct {
		Server struct {
			URL string `json:"url"`
		} `json:"server"`
	} `json:"downloads"`
	// JavaVersion is Mojang's declared minimum Java for this release.
	JavaVersion struct {
		MajorVersion int `json:"majorVersion"`
	} `json:"javaVersion"`
}

func isOfficialMojangURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "mojang.com" || strings.HasSuffix(host, ".mojang.com")
}

func resolveMinecraftServerURL(ctx context.Context, client *http.Client, version string) (string, string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, minecraftVersionManifestURL, nil)
	if err != nil {
		return "", "", 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", 0, fmt.Errorf("fetch Minecraft version manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", 0, fmt.Errorf("Minecraft version manifest returned HTTP %d", resp.StatusCode)
	}
	var manifest minecraftManifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&manifest); err != nil {
		return "", "", 0, fmt.Errorf("parse Minecraft version manifest: %w", err)
	}
	version = strings.TrimSpace(version)
	if version == "" || strings.EqualFold(version, "latest") {
		version = manifest.Latest.Release
	}
	var metadataURL string
	for _, entry := range manifest.Versions {
		if entry.ID == version {
			if !isOfficialMojangURL(entry.URL) {
				return "", "", 0, errors.New("Minecraft version metadata URL is not an official HTTPS Mojang URL")
			}
			metadataURL = entry.URL
			break
		}
	}
	if metadataURL == "" {
		return "", "", 0, fmt.Errorf("Minecraft version %q was not found in the official version manifest", version)
	}
	metaReq, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return "", "", 0, err
	}
	metaResp, err := client.Do(metaReq)
	if err != nil {
		return "", "", 0, fmt.Errorf("fetch Minecraft %s metadata: %w", version, err)
	}
	defer metaResp.Body.Close()
	if metaResp.StatusCode != http.StatusOK {
		return "", "", 0, fmt.Errorf("Minecraft version metadata returned HTTP %d", metaResp.StatusCode)
	}
	var meta minecraftVersionMeta
	if err := json.NewDecoder(io.LimitReader(metaResp.Body, 8<<20)).Decode(&meta); err != nil {
		return "", "", 0, fmt.Errorf("parse Minecraft version metadata: %w", err)
	}
	if meta.Downloads.Server.URL == "" {
		return "", "", 0, fmt.Errorf("official Minecraft version %s does not publish a server jar", version)
	}
	if !isOfficialMojangURL(meta.Downloads.Server.URL) {
		return "", "", 0, errors.New("Minecraft server download URL is not an official HTTPS Mojang URL")
	}
	return version, meta.Downloads.Server.URL, meta.JavaVersion.MajorVersion, nil
}

func (m *ServerManager) RunMinecraftInstaller(version, variant string) error {
	if variant == "" {
		variant = "vanilla"
	}
	if !validMinecraftVariant(variant) {
		return fmt.Errorf("unknown Minecraft server variant %q", variant)
	}
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return errors.New("server is running")
	}
	if m.installBusy {
		m.mu.Unlock()
		return errors.New("an install job is already running")
	}
	if m.gameType != "minecraft-java" {
		m.mu.Unlock()
		return errors.New("select a Minecraft Java server before installing Minecraft files")
	}
	if (MinecraftAdapter{}).Installed(m.rootDir) {
		m.mu.Unlock()
		return errors.New("Minecraft server files are already installed; updating them is not supported by this installer yet")
	}
	m.installBusy = true
	m.mu.Unlock()
	go func() {
		defer func() { m.mu.Lock(); m.installBusy = false; m.mu.Unlock() }()
		if err := m.installMinecraft(version, variant); err != nil {
			m.appendLog("installer", "Minecraft installation failed: "+err.Error())
		} else {
			m.appendLog("installer", "Minecraft Java server files installed successfully")
		}
	}()
	return nil
}

var minecraftVariantNames = map[string]string{"vanilla": "vanilla", "paper": "Paper", "fabric": "Fabric", "quilt": "Quilt", "forge": "Forge", "neoforge": "NeoForge"}

func (m *ServerManager) installMinecraft(version, variant string) error {
	// Loader installers download Minecraft and many libraries, so the whole
	// install gets longer than any single download.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	return m.installMinecraftPinned(ctx, version, variant, "")
}

// installMinecraftPinned installs a server, with the given loader build when
// pin is set (Fabric, Quilt, Forge and NeoForge).
func (m *ServerManager) installMinecraftPinned(ctx context.Context, version, variant, pin string) error {
	client := &http.Client{Timeout: 8 * time.Minute}
	if _, loader := minecraftLoaderHosts[variant]; loader {
		return m.installMinecraftLoader(ctx, client, version, variant, pin)
	}
	var resolvedVersion, downloadURL, sha string
	var javaMajor int
	var err error
	switch variant {
	case "paper":
		resolvedVersion, downloadURL, sha, err = resolvePaper(ctx, client, version)
	case "fabric":
		resolvedVersion, downloadURL, err = resolveFabricLoader(ctx, client, version, pin)
	default:
		resolvedVersion, downloadURL, javaMajor, err = resolveMinecraftServerURL(ctx, client, version)
	}
	if err != nil {
		return err
	}
	if variant != "vanilla" {
		// Paper and Fabric run Mojang's server code, so Mojang's published Java
		// requirement applies. If it cannot be read, the check is skipped.
		if _, _, major, err := resolveMinecraftServerURL(ctx, client, resolvedVersion); err == nil {
			javaMajor = major
		} else {
			m.appendLog("installer", "Could not read the Java requirement for Minecraft "+resolvedVersion+": "+err.Error())
		}
	}
	gameDir := pathsFor(m.rootDir).Game()
	if err := os.MkdirAll(gameDir, 0755); err != nil {
		return err
	}
	finalPath := filepath.Join(gameDir, minecraftLaunchJar(variant))
	tmpPath := finalPath + ".download"
	_ = os.Remove(tmpPath)
	m.appendLog("installer", "Downloading the "+minecraftVariantNames[variant]+" server for Minecraft "+resolvedVersion+"…")
	if err := downloadVerified(ctx, client, downloadURL, tmpPath, 512<<20, sha); err != nil {
		return fmt.Errorf("download server jar: %w", err)
	}
	if err := validateMinecraftJar(tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.WriteFile(filepath.Join(gameDir, "eula.txt"), []byte("# Accepted by the server owner in Realmkeeper. See https://aka.ms/MinecraftEULA.\neula=true\n"), 0600); err != nil {
		return err
	}
	info := minecraftInstallInfo{Version: resolvedVersion, JavaMajor: javaMajor, Variant: variant}
	if variant == "fabric" {
		info.LoaderVersion = fabricLoaderFromURL(downloadURL)
	}
	if err := writeMinecraftInstallInfo(gameDir, info); err != nil {
		return err
	}
	if variant == "fabric" {
		m.appendLog("installer", "Installed the Fabric launcher for Minecraft "+resolvedVersion+"; it downloads Mojang's server on first start")
	} else {
		m.appendLog("installer", "Downloaded the "+minecraftVariantNames[variant]+" server for Minecraft "+resolvedVersion)
	}
	return nil
}

func (a *App) handleMinecraftInstall(w http.ResponseWriter, r *http.Request) {
	m, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if s.GameType != "minecraft-java" {
		jsonError(w, 400, "select a Minecraft Java server before installing Minecraft files")
		return
	}
	if !s.MinecraftEulaAccepted {
		jsonError(w, 400, "accept the Minecraft EULA in the creation wizard before installing server files")
		return
	}
	if m.IsRunning() {
		jsonError(w, 409, "stop the server before installing or updating files")
		return
	}
	if s.MinecraftModpack != nil {
		if m.Installed() {
			jsonError(w, 409, "Minecraft server files are already installed; update the pack from the Mods tab")
			return
		}
		// The pack's index decides the loader and Minecraft version.
		done := func(rec modpackRecord) { _ = a.setMinecraftServerType(s.ID, rec.Loader, rec.MinecraftVersion) }
		if err := m.startModpackJob(s.MinecraftVariant, *s.MinecraftModpack, done); err != nil {
			jsonError(w, 409, err.Error())
			return
		}
		jsonOut(w, 202, map[string]any{"ok": true, "message": "Mod pack installation started; follow progress in Console"})
		return
	}
	if err := m.RunMinecraftInstaller(s.MinecraftVersion, s.MinecraftVariant); err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	jsonOut(w, 202, map[string]any{"ok": true, "message": "Minecraft server download started; follow progress in Console"})
}

func validateMinecraftJar(path string) error {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("downloaded Minecraft server file is not a valid JAR: %w", err)
	}
	defer archive.Close()
	for _, file := range archive.File {
		if strings.EqualFold(file.Name, "META-INF/MANIFEST.MF") {
			return nil
		}
	}
	return errors.New("downloaded Minecraft server JAR is missing its manifest")
}

func writeMinecraftProperties(root string, s ServerSettings) error {
	gameDir := pathsFor(root).Game()
	if err := os.MkdirAll(gameDir, 0755); err != nil {
		return err
	}
	if !s.MinecraftEulaAccepted {
		return errors.New("Minecraft EULA must be accepted before installing or starting this server")
	}
	eula := filepath.Join(gameDir, "eula.txt")
	if err := os.WriteFile(eula, []byte("# Accepted by the server owner in Realmkeeper. See https://aka.ms/MinecraftEULA.\neula=true\n"), 0600); err != nil {
		return err
	}
	mode := "survival"
	if strings.EqualFold(s.GameMode, "creative") {
		mode = "creative"
	} else if strings.EqualFold(s.GameMode, "adventure") {
		mode = "adventure"
	} else if strings.EqualFold(s.GameMode, "spectator") {
		mode = "spectator"
	}
	props := []struct{ key, value string }{
		{"motd", strings.ReplaceAll(strings.ReplaceAll(s.MOTD, "\r", " "), "\n", " ")},
		{"server-port", strconv.Itoa(s.Port)},
		{"max-players", strconv.Itoa(s.MaxPlayers)},
		{"gamemode", mode},
		{"white-list", strconv.FormatBool(s.WhitelistEnabled)},
		{"online-mode", "true"},
		{"enable-command-block", "false"},
		{"server-ip", ""},
	}
	return mergeMinecraftProperties(filepath.Join(gameDir, "server.properties"), props)
}

func mergeMinecraftProperties(path string, values []struct{ key, value string }) error {
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	managed := make(map[string]string, len(values))
	for _, item := range values {
		managed[item.key] = item.value
	}
	seen := make(map[string]bool, len(values))
	lines := []string{}
	if err == nil {
		lines = strings.Split(strings.TrimRight(string(existing), "\r\n"), "\n")
	}
	output := make([]string, 0, len(lines)+len(values))
	for _, line := range lines {
		key, _, ok := strings.Cut(line, "=")
		if ok {
			if value, isManaged := managed[strings.TrimSpace(key)]; isManaged {
				output = append(output, key+"="+value)
				seen[strings.TrimSpace(key)] = true
				continue
			}
		}
		output = append(output, line)
	}
	for _, item := range values {
		if !seen[item.key] {
			output = append(output, item.key+"="+item.value)
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(output, "\n")+"\n"), 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// minecraftInstallInfo is written next to server.jar at install time so the
// Java requirement is known without contacting Mojang again before each start.
type minecraftInstallInfo struct {
	Version   string `json:"version"`
	JavaMajor int    `json:"javaMajor"`
	Variant   string `json:"variant,omitempty"`
	// LoaderVersion is the Forge, NeoForge or Quilt build.
	LoaderVersion string `json:"loaderVersion,omitempty"`
	// ArgsFile is the argument file Forge and NeoForge start from, relative
	// to the game folder.
	ArgsFile string `json:"argsFile,omitempty"`
}

const minecraftInstallInfoFile = ".hsm-minecraft.json"

func writeMinecraftInstallInfo(gameDir string, info minecraftInstallInfo) error {
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(gameDir, minecraftInstallInfoFile), append(b, '\n'), 0644)
}

func readMinecraftInstallInfo(gameDir string) (minecraftInstallInfo, bool) {
	var info minecraftInstallInfo
	b, err := os.ReadFile(filepath.Join(gameDir, minecraftInstallInfoFile))
	if err != nil || json.Unmarshal(b, &info) != nil {
		return minecraftInstallInfo{}, false
	}
	return info, true
}

// checkMinecraftJava refuses to start a server whose release needs a newer
// Java than the one installed. Servers installed before the requirement was
// recorded are not checked, because the requirement is unknown.
func checkMinecraftJava(gameDir string, detect func() (int, error)) error {
	info, ok := readMinecraftInstallInfo(gameDir)
	if !ok || info.JavaMajor <= 0 {
		return nil
	}
	have, err := detect()
	if err != nil {
		return fmt.Errorf("could not check the installed Java version: %w", err)
	}
	if have < info.JavaMajor {
		return fmt.Errorf("Minecraft %s needs Java %d or newer, but this container has Java %d; update the Realmkeeper image to one with a newer Java runtime", info.Version, info.JavaMajor, have)
	}
	return nil
}

var javaVersionPattern = regexp.MustCompile(`version "(\d+)(?:\.(\d+))?`)

// parseJavaMajor reads the major version from `java -version` output:
// "21.0.4" is 21, and the legacy "1.8.0_392" scheme is 8.
func parseJavaMajor(output string) (int, bool) {
	m := javaVersionPattern.FindStringSubmatch(output)
	if m == nil {
		return 0, false
	}
	major, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	if major == 1 && m[2] != "" {
		minor, err := strconv.Atoi(m[2])
		if err != nil {
			return 0, false
		}
		return minor, true
	}
	return major, true
}

func detectJavaMajor() (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, javaCommand, "-version").CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("java -version failed: %w", err)
	}
	major, ok := parseJavaMajor(string(out))
	if !ok {
		return 0, fmt.Errorf("unrecognised java -version output: %q", strings.TrimSpace(string(out)))
	}
	return major, nil
}
