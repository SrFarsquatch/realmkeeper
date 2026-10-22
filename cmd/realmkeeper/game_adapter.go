package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ProcessLaunch is the game-specific part of process creation. ServerManager
// owns the shared lifecycle, log pipes, process group, and exit-state tracking.
type ProcessLaunch struct {
	Path string
	Args []string
	Dir  string
}

// GameAdapter owns game-specific install detection, launch preparation, process
// selection, and graceful shutdown. Shared process lifecycle stays in
// ServerManager so every game gets the same logging and crash-state behavior.
//
// Each adapter also owns the game's settings policy, so the generic create and
// settings handlers never compare game IDs.
type GameAdapter interface {
	GameType() string
	Rules() GameRules
	Installed(rootDir string) bool
	PrepareLaunch(app *App, settings ServerSettings) ([]string, error)
	BuildProcess(rootDir string, args []string) (ProcessLaunch, error)
	RequestStop(pid int, stdin io.WriteCloser) error
	// ValidateNew checks and defaults the game's own fields of a server being
	// created. The error is shown to the user.
	ValidateNew(req *newServerRequest) error
	// MergeSettings restores fields a settings update may not change from
	// current, and validates the game's own editable fields.
	MergeSettings(in *ServerSettings, current ServerSettings) error
	// ApplySettings writes saved settings into the game's own files.
	ApplySettings(app *App, m *ServerManager, s ServerSettings) error
	// InstallStatus describes the install for the Install tab. The handler adds
	// the shared installed and busy fields.
	InstallStatus(app *App, s ServerSettings) map[string]any
}

// GameRules are the per-game limits the generic settings validation applies.
// They are also served to the browser, so the forms follow the same rules.
type GameRules struct {
	// GameModes lists the accepted modes; the first is the default.
	GameModes []string `json:"gameModes"`
	// ModeLabel names the mode setting in forms ("Game mode", "Difficulty").
	ModeLabel string `json:"modeLabel"`
	// ModeFixed means the mode applies only when the world is created.
	ModeFixed         bool   `json:"modeFixed"`
	DefaultMOTD       string `json:"defaultMotd"`
	DefaultMaxPlayers int    `json:"defaultMaxPlayers"`
	// PlayerLimit is the most players the game supports.
	PlayerLimit int `json:"playerLimit"`
	// JVMMemory and Whitelist say whether those settings apply.
	JVMMemory bool `json:"jvmMemory"`
	Whitelist bool `json:"whitelist"`
	// PortMin and PortMax pin a fixed port range. Zero means the deployment's
	// published game port range; served copies always hold the resolved range.
	PortMin int `json:"portMin"`
	PortMax int `json:"portMax"`
}

// resolved returns the rules with the actual port range filled in.
func (r GameRules) resolved() GameRules {
	r.PortMin, r.PortMax = r.portRange()
	return r
}

var hytaleGameModes = []string{"Adventure", "Creative"}

func (r GameRules) portRange() (int, int) {
	if r.PortMin > 0 {
		return r.PortMin, r.PortMax
	}
	minPort, maxPort, _ := deploymentPortRange()
	return minPort, maxPort
}

func (r GameRules) checkGameMode(mode string) error {
	for _, allowed := range r.GameModes {
		if mode == allowed {
			return nil
		}
	}
	modes := r.GameModes
	list := strings.Join(modes, " or ")
	if len(modes) > 2 {
		list = strings.Join(modes[:len(modes)-1], ", ") + ", or " + modes[len(modes)-1]
	}
	return errors.New("game mode must be " + list)
}

func (r GameRules) checkMaxPlayers(n int, gameName string) error {
	if n > r.PlayerLimit && r.PlayerLimit < 1000 {
		return fmt.Errorf("%s supports up to %d players", gameName, r.PlayerLimit)
	}
	if n < 1 || n > 1000 {
		return errors.New("maximum players must be between 1 and 1000")
	}
	return nil
}

// portRangeForGame returns the ports a server of this game may use.
func portRangeForGame(gameType string) (int, int) {
	adapter, err := gameAdapterFor(gameType)
	if err != nil {
		return GameRules{}.portRange()
	}
	return adapter.Rules().portRange()
}

// keepMinecraftFields, keepDragonwildsFields and keepTerrariaFields stop a settings update from
// changing another game's fields, or Minecraft's fixed install identity.
func keepMinecraftFields(in *ServerSettings, current ServerSettings) {
	in.MinecraftEulaAccepted = current.MinecraftEulaAccepted
	in.MinecraftVersion = current.MinecraftVersion
	in.MinecraftVariant = current.MinecraftVariant
}

func keepDragonwildsFields(in *ServerSettings, current ServerSettings) {
	in.DragonwildsOwnerID = current.DragonwildsOwnerID
	in.DragonwildsWorldName = current.DragonwildsWorldName
	in.DragonwildsAdminPassword = current.DragonwildsAdminPassword
	in.DragonwildsWorldPassword = current.DragonwildsWorldPassword
	in.DragonwildsPublic = current.DragonwildsPublic
}

type HytaleAdapter struct{}

func (HytaleAdapter) GameType() string { return "hytale" }

func (HytaleAdapter) Rules() GameRules {
	return GameRules{GameModes: hytaleGameModes, ModeLabel: "Game mode", DefaultMOTD: "Welcome to your Hytale server!", DefaultMaxPlayers: 20, PlayerLimit: 1000, JVMMemory: true, Whitelist: true}
}

func (HytaleAdapter) ValidateNew(req *newServerRequest) error {
	patchline, err := normalizeHytalePatchline(req.HytalePatchline)
	req.HytalePatchline = patchline
	return err
}

func (HytaleAdapter) MergeSettings(in *ServerSettings, current ServerSettings) error {
	// The release channel changes only through the switch endpoint, which
	// takes a snapshot first.
	in.HytalePatchline = current.HytalePatchline
	keepMinecraftFields(in, current)
	keepDragonwildsFields(in, current)
	keepTerrariaFields(in, current)
	return nil
}

// ApplySettings writes jvm.options, and config.json while the server is
// stopped (the running server owns config.json).
func (HytaleAdapter) ApplySettings(app *App, m *ServerManager, s ServerSettings) error {
	if err := app.writeJVMOptionsFor(s); err != nil {
		return err
	}
	if m.IsRunning() {
		return nil
	}
	if err := app.applyHytaleConfig(s); err != nil {
		return fmt.Errorf("settings saved, but Hytale config could not be updated: %w", err)
	}
	return nil
}

func (HytaleAdapter) InstallStatus(app *App, s ServerSettings) map[string]any {
	root := app.serverRoot(s)
	downloader := filepath.Join(app.dataDir, "tools", "hytale-downloader")
	bundle := filepath.Join(pathsFor(root).Imports(), "game.zip")
	return map[string]any{"downloaderPresent": fileExists(downloader), "bundlePresent": fileExists(bundle), "paths": map[string]string{"downloader": downloader, "bundle": bundle, "game": pathsFor(root).Game()}}
}

func (HytaleAdapter) Installed(rootDir string) bool {
	return fileExists(filepath.Join(pathsFor(rootDir).Game(), "Server", "HytaleServer.jar")) &&
		fileExists(filepath.Join(pathsFor(rootDir).Game(), "Assets.zip"))
}

func (HytaleAdapter) PrepareLaunch(app *App, settings ServerSettings) ([]string, error) {
	if err := app.writeJVMOptionsFor(settings); err != nil {
		return nil, err
	}
	if err := app.applyHytaleConfig(settings); err != nil {
		return nil, fmt.Errorf("could not apply Hytale settings: %w", err)
	}
	extra := splitArgs(settings.ExtraArgs)
	script, _ := os.ReadFile(filepath.Join(pathsFor(app.serverRoot(settings)).Game(), "start.sh"))
	// Backups are configured in config.json, never on the command line: the
	// server rejects an option given twice, and start.sh can pass backup
	// options of its own.
	var args []string
	if source := hytaleOptionSource("--bind", string(script), extra); source != "" {
		if m, _, err := app.serverByID(settings.ID); err == nil {
			m.appendLog("controller", "Not passing --bind: "+source+" already sets it")
		}
	} else {
		args = append(args, "--bind", fmt.Sprintf("0.0.0.0:%d", settings.Port))
	}
	return append(args, extra...), nil
}

// hytaleOptionSource says where an option the server is already given comes
// from: start.sh or the extra launch arguments, or "" when neither sets it.
func hytaleOptionSource(option, script string, extra []string) string {
	for _, arg := range extra {
		if arg == option || strings.HasPrefix(arg, option+"=") {
			return "the extra launch arguments"
		}
	}
	for _, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		for _, field := range strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == '\t' || r == '"' || r == '\'' }) {
			if field == option || strings.HasPrefix(field, option+"=") {
				return "start.sh"
			}
		}
	}
	return ""
}

func (HytaleAdapter) BuildProcess(rootDir string, args []string) (ProcessLaunch, error) {
	gameDir := pathsFor(rootDir).Game()
	startScript := filepath.Join(gameDir, "start.sh")
	if !fileExists(startScript) {
		return ProcessLaunch{}, errors.New("game/start.sh is missing; import an official server bundle or run /update setup first")
	}
	if err := os.Chmod(startScript, 0755); err != nil {
		return ProcessLaunch{}, fmt.Errorf("make Hytale start script executable: %w", err)
	}
	cmdArgs := append([]string{startScript}, args...)
	return ProcessLaunch{Path: "bash", Args: cmdArgs, Dir: gameDir}, nil
}

func (HytaleAdapter) RequestStop(_ int, stdin io.WriteCloser) error {
	if stdin == nil {
		return errors.New("server stdin is unavailable")
	}
	return writeStopCommand(stdin)
}

func writeStopCommand(stdin io.WriteCloser) error {
	_, err := io.WriteString(stdin, "/stop\n")
	return err
}

// gameAdapterFor intentionally returns only implemented adapters. Catalog entries
// for planned games must not be mistaken for working installers or launchers.
func gameAdapterFor(gameType string) (GameAdapter, error) {
	gameType = strings.TrimSpace(strings.ToLower(gameType))
	if gameType == "" {
		gameType = "hytale" // compatibility for old settings and test fixtures
	}
	switch gameType {
	case "hytale":
		return HytaleAdapter{}, nil
	case "runescape-dragonwilds":
		return DragonwildsAdapter{}, nil
	case "minecraft-java":
		return MinecraftAdapter{}, nil
	case "terraria":
		return TerrariaAdapter{}, nil
	default:
		return nil, fmt.Errorf("unknown game type %q", gameType)
	}
}
