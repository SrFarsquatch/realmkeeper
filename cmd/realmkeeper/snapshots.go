package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// snapshotSpec describes what a full snapshot of one game's server contains:
// top-level entries of Base that are archived and, on restore, replaced.
type snapshotSpec struct {
	Base    string
	Include []string
}

var hytaleSnapshotEntries = []string{"universe", "mods", "config.json", "permissions.json", "whitelist.json", "bans.json"}

// snapshotSpecFor returns the snapshot layout for a server's game. Games
// without a verified layout have none, and the API refuses snapshots for them.
func snapshotSpecFor(root string, s ServerSettings) (snapshotSpec, bool) {
	switch normalizeGameType(s.GameType) {
	case "hytale":
		return snapshotSpec{Base: filepath.Join(pathsFor(root).Game(), "Server"), Include: hytaleSnapshotEntries}, true
	case "minecraft-java":
		game := pathsFor(root).Game()
		level := minecraftLevelName(game)
		// Vanilla keeps every dimension inside the level folder; Bukkit-style
		// servers use sibling _nether/_the_end folders, captured when present.
		return snapshotSpec{Base: game, Include: []string{
			level, level + "_nether", level + "_the_end",
			"server.properties", "whitelist.json", "ops.json", "banned-players.json", "banned-ips.json",
			// Paper plugins and their data, Paper settings, Fabric mods and configs.
			"plugins", "bukkit.yml", "spigot.yml", "mods", "config",
			// Forge's default configs, and what was installed from Modrinth.
			"defaultconfigs", modrinthTrackFile, datapackTrackFile, modpackTrackFile,
		}}, true
	case "runescape-dragonwilds":
		// Saves and the game's config; guides spell the save folder both ways.
		return snapshotSpec{Base: dragonwildsSavedDir(root), Include: []string{"SaveGames", "Savegames", "Config"}}, true
	case "terraria":
		include := []string{"Worlds", "serverconfig.txt", "banlist.txt"}
		if terrariaVariant(s) == "tmodloader" {
			// Modded worlds need the same mods and their settings.
			include = append(include, "Mods", "ModConfigs")
		}
		return snapshotSpec{Base: pathsFor(root).Game(), Include: include}, true
	}
	return snapshotSpec{}, false
}

// minecraftLevelName reads level-name from server.properties. Anything that
// is not a single plain path segment falls back to the vanilla default.
func minecraftLevelName(gameDir string) string {
	const fallback = "world"
	f, err := os.Open(filepath.Join(gameDir, "server.properties"))
	if err != nil {
		return fallback
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if !ok || strings.TrimSpace(key) != "level-name" {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/\\\x00") {
			return fallback
		}
		return value
	}
	return fallback
}

// restoreSnapshot extracts src into a staging directory beside the live data,
// then replaces each included entry present in the snapshot. Replacing rather
// than extracting over the live files means files created after the snapshot
// (for example newer world region files) cannot survive into the restore.
func restoreSnapshot(src string, spec snapshotSpec) error {
	if err := os.MkdirAll(spec.Base, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(spec.Base, ".hsm-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := extractTarGz(src, staging); err != nil {
		return fmt.Errorf("snapshot could not be read; nothing was changed: %w", err)
	}
	for _, name := range spec.Include {
		staged := filepath.Join(staging, name)
		if _, err := os.Lstat(staged); errors.Is(err, os.ErrNotExist) {
			continue
		}
		target := filepath.Join(spec.Base, name)
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("remove current %s: %w", name, err)
		}
		if err := os.Rename(staged, target); err != nil {
			return fmt.Errorf("restore %s: %w", name, err)
		}
	}
	return nil
}

// Live Minecraft snapshots follow the standard procedure for backing up a
// running server: disable autosave, flush all chunks to disk, archive, and
// re-enable autosave whatever happens.
var minecraftSaveTimeout = 60 * time.Second

// minecraftSaveTimeoutForTest shortens the save wait and returns a restore func.
func minecraftSaveTimeoutForTest(ms int) func() {
	previous := minecraftSaveTimeout
	minecraftSaveTimeout = time.Duration(ms) * time.Millisecond
	return func() { minecraftSaveTimeout = previous }
}

func (m *ServerManager) withMinecraftSaveHold(timeout time.Duration, archive func() error) error {
	sentAt := time.Now()
	if err := m.SendCommand("save-off"); err != nil {
		return fmt.Errorf("could not pause autosave: %w", err)
	}
	defer func() {
		if err := m.SendCommand("save-on"); err != nil && m.IsRunning() {
			m.appendLog("controller", "Warning: could not re-enable autosave after the snapshot: "+err.Error())
		}
	}()
	if err := m.SendCommand("save-all flush"); err != nil {
		return fmt.Errorf("could not save the world: %w", err)
	}
	if !m.waitForLog(sentAt, timeout, "Saved the game") {
		return errors.New("the server did not confirm that the world was saved; no snapshot was taken")
	}
	return archive()
}

// waitForLog reports whether a log line containing text appears at or after
// since, within timeout.
func (m *ServerManager) waitForLog(since time.Time, timeout time.Duration, text string) bool {
	deadline := time.Now().Add(timeout)
	for {
		for _, line := range m.logs.Snapshot() {
			if !line.Time.Before(since) && line.Source != "console" && strings.Contains(line.Line, text) {
				return true
			}
		}
		if time.Now().After(deadline) || !m.IsRunning() {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}
