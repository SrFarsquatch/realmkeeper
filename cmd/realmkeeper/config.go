package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

type ServerSettings struct {
	ID                       string `json:"id"`
	GameType                 string `json:"gameType,omitempty"`
	DragonwildsOwnerID       string `json:"dragonwildsOwnerId,omitempty"`
	DragonwildsWorldName     string `json:"dragonwildsWorldName,omitempty"`
	DragonwildsAdminPassword string `json:"dragonwildsAdminPassword,omitempty"`
	DragonwildsWorldPassword string `json:"dragonwildsWorldPassword,omitempty"`
	DragonwildsPublic        bool   `json:"dragonwildsPublic,omitempty"`
	MinecraftVersion         string `json:"minecraftVersion,omitempty"`
	MinecraftVariant         string `json:"minecraftVariant,omitempty"`
	MinecraftEulaAccepted    bool   `json:"minecraftEulaAccepted,omitempty"`
	// MinecraftModpack is the Modrinth pack the server was created from; the
	// installer installs it with its loader.
	MinecraftModpack  *modpackRef `json:"minecraftModpack,omitempty"`
	TerrariaVariant   string      `json:"terrariaVariant,omitempty"`
	TerrariaVersion   string      `json:"terrariaVersion,omitempty"`
	TerrariaWorldName string      `json:"terrariaWorldName,omitempty"`
	TerrariaWorldSize string      `json:"terrariaWorldSize,omitempty"`
	// HytalePatchline is the Hytale release channel: "" for release, or
	// "pre-release".
	HytalePatchline  string `json:"hytalePatchline,omitempty"`
	ServerName       string `json:"serverName"`
	Port             int    `json:"port"`
	GameMode         string `json:"gameMode"`
	MaxPlayers       int    `json:"maxPlayers"`
	MOTD             string `json:"motd"`
	Password         string `json:"password"`
	WhitelistEnabled bool   `json:"whitelistEnabled"`
	MemoryMin        string `json:"memoryMin"`
	MemoryMax        string `json:"memoryMax"`
	AutoStart        bool   `json:"autoStart"`
	// AutoRestart restarts the server after it crashes (see crash.go).
	AutoRestart       bool   `json:"autoRestart"`
	ExtraArgs         string `json:"extraArgs"`
	PublicHost        string `json:"publicHost"`
	BackupFrequency   int    `json:"backupFrequency"`
	BackupRetention   int    `json:"backupRetention"`
	SnapshotRetention int    `json:"snapshotRetention"`
	StorageDir        string `json:"storageDir,omitempty"`
	PortMin           int    `json:"portMin,omitempty"`
	PortMax           int    `json:"portMax,omitempty"`
}

type legacySettings struct {
	ServerName      string `json:"serverName"`
	Port            int    `json:"port"`
	MemoryMin       string `json:"memoryMin"`
	MemoryMax       string `json:"memoryMax"`
	AutoStart       bool   `json:"autoStart"`
	ExtraArgs       string `json:"extraArgs"`
	PublicHost      string `json:"publicHost"`
	BackupFrequency int    `json:"backupFrequency"`
}

type ControllerConfig struct {
	Admin   *AdminConfig     `json:"admin,omitempty"`
	Servers []ServerSettings `json:"servers"`
	// Schedules holds each server's automated tasks, keyed by server ID.
	Schedules map[string][]Schedule `json:"schedules,omitempty"`
	Settings  *legacySettings       `json:"settings,omitempty"`
	// DragonwildsOwnerID is the last Owner ID (in-game Player ID) used, so new
	// Dragonwilds servers do not ask for it again.
	DragonwildsOwnerID string `json:"dragonwildsOwnerId,omitempty"`
}

func defaultServerSettings(id, name string, port int, storage string) ServerSettings {
	min, max, _ := deploymentPortRange()
	return ServerSettings{ID: id, GameType: "hytale", ServerName: name, Port: port, GameMode: "Adventure", MaxPlayers: 20, MOTD: "Welcome to your Hytale server!", MemoryMin: "2G", MemoryMax: "4G", AutoStart: false, ExtraArgs: "", PublicHost: "", BackupFrequency: 30, BackupRetention: 10, SnapshotRetention: 5, StorageDir: storage, PortMin: min, PortMax: max}
}

func (a *App) loadConfig() error {
	path := filepath.Join(a.dataDir, "controller.json")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		a.cfg = ControllerConfig{Servers: []ServerSettings{}}
		return a.saveConfigUnlocked()
	}
	if err != nil {
		return err
	}
	var cfg ControllerConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return err
	}
	var rawCfg struct {
		Servers []map[string]json.RawMessage `json:"servers"`
	}
	_ = json.Unmarshal(b, &rawCfg)
	if len(cfg.Servers) == 0 && cfg.Settings != nil {
		port := 5520
		name := "My Hytale Server"
		ls := cfg.Settings
		if ls != nil {
			if ls.Port > 0 {
				port = ls.Port
			}
			if ls.ServerName != "" {
				name = ls.ServerName
			}
		}
		s := defaultServerSettings("main", name, port, ".")
		if ls != nil {
			s.MemoryMin = defaultIf(ls.MemoryMin, "2G")
			s.MemoryMax = defaultIf(ls.MemoryMax, "4G")
			s.AutoStart = ls.AutoStart
			s.ExtraArgs = ls.ExtraArgs
			s.PublicHost = ls.PublicHost
			if ls.BackupFrequency > 0 {
				s.BackupFrequency = ls.BackupFrequency
			}
		}
		cfg.Servers = []ServerSettings{s}
		cfg.Settings = nil
	}
	min, max, _ := deploymentPortRange()
	for i := range cfg.Servers {
		// Configs written before multi-game support omit gameType; keep them as Hytale.
		if strings.TrimSpace(cfg.Servers[i].GameType) == "" {
			cfg.Servers[i].GameType = "hytale"
		}
		if cfg.Servers[i].ID == "" {
			cfg.Servers[i].ID = fmt.Sprintf("server-%d", i+1)
		}
		if cfg.Servers[i].StorageDir == "" {
			if i == 0 {
				cfg.Servers[i].StorageDir = "."
			} else {
				cfg.Servers[i].StorageDir = filepath.Join("servers", cfg.Servers[i].ID)
			}
		}
		var rawFields map[string]json.RawMessage
		if i < len(rawCfg.Servers) {
			rawFields = rawCfg.Servers[i]
		}
		a.migrateMissingHytaleSettings(&cfg.Servers[i], rawFields)
		if cfg.Servers[i].Port == 0 {
			cfg.Servers[i].Port = min + i
		}
		cfg.Servers[i].PortMin = min
		cfg.Servers[i].PortMax = max
		// Repair values using the server's own game rules, so a Minecraft
		// Survival server or a Terraria difficulty is not reset to Hytale's.
		rules := (HytaleAdapter{}).Rules()
		if adapter, err := gameAdapterFor(cfg.Servers[i].GameType); err == nil {
			rules = adapter.Rules()
		}
		if !slices.Contains(rules.GameModes, cfg.Servers[i].GameMode) {
			cfg.Servers[i].GameMode = rules.GameModes[0]
		}
		if cfg.Servers[i].MaxPlayers < 1 || cfg.Servers[i].MaxPlayers > rules.PlayerLimit {
			cfg.Servers[i].MaxPlayers = rules.DefaultMaxPlayers
		}
		if cfg.Servers[i].MOTD == "" {
			cfg.Servers[i].MOTD = rules.DefaultMOTD
		}
		if cfg.DragonwildsOwnerID == "" && cfg.Servers[i].GameType == "runescape-dragonwilds" {
			cfg.DragonwildsOwnerID = strings.TrimSpace(cfg.Servers[i].DragonwildsOwnerID)
		}
		if cfg.Servers[i].MemoryMin == "" {
			cfg.Servers[i].MemoryMin = "2G"
		}
		if cfg.Servers[i].MemoryMax == "" {
			cfg.Servers[i].MemoryMax = "4G"
		}
		if cfg.Servers[i].BackupFrequency == 0 {
			cfg.Servers[i].BackupFrequency = 30
		}
		if cfg.Servers[i].BackupRetention < 1 || cfg.Servers[i].BackupRetention > 100 {
			cfg.Servers[i].BackupRetention = 10
		}
		if cfg.Servers[i].SnapshotRetention < 1 || cfg.Servers[i].SnapshotRetention > 100 {
			cfg.Servers[i].SnapshotRetention = 5
		}
	}
	migrateStorage(a.dataDir, cfg.Servers)
	a.cfg = cfg
	return a.saveConfigUnlocked()
}

func (a *App) migrateMissingHytaleSettings(s *ServerSettings, raw map[string]json.RawMessage) {
	missing := func(key string) bool {
		_, present := raw[key]
		return !present
	}
	path := filepath.Join(pathsFor(a.serverRoot(*s)).Game(), "Server", "config.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var gameCfg map[string]any
	if json.Unmarshal(b, &gameCfg) != nil {
		return
	}
	if missing("gameMode") {
		if defaults, ok := gameCfg["Defaults"].(map[string]any); ok {
			if mode, ok := defaults["GameMode"].(string); ok && (mode == "Adventure" || mode == "Creative") {
				s.GameMode = mode
			}
		}
	}
	if missing("maxPlayers") {
		if players, ok := gameCfg["MaxPlayers"].(float64); ok && players >= 1 && players <= 1000 {
			s.MaxPlayers = int(players)
		}
	}
	if missing("motd") {
		if motd, ok := gameCfg["MOTD"].(string); ok {
			s.MOTD = motd
		}
	}
	if missing("password") {
		if password, ok := gameCfg["Password"].(string); ok {
			s.Password = password
		}
	}
	if missing("whitelistEnabled") {
		if required, ok := gameCfg["RequireJoinPermission"].(bool); ok {
			s.WhitelistEnabled = required
		}
	}
}

func (a *App) saveConfigUnlocked() error {
	b, err := json.MarshalIndent(a.cfg, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(a.dataDir, "controller.json"), b, 0600)
}

func (a *App) saveConfigLocked() error { return a.saveConfigUnlocked() }

func (a *App) uniqueServerID(base string) string {
	if base == "" {
		base = "server"
	}
	id := base
	for n := 2; n < 1000; n++ {
		if !a.serverIDExists(id) {
			return id
		}
		id = fmt.Sprintf("%s-%d", base, n)
	}
	return ""
}

func (a *App) serverIDExists(id string) bool {
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()
	for _, s := range a.cfg.Servers {
		if s.ID == id {
			return true
		}
	}
	return false
}

// firstFreePort returns the lowest port in the range no server uses, or 0.
func (a *App) firstFreePort(min, max int) int {
	for p := min; p <= max; p++ {
		if !a.portInUse(p, "") {
			return p
		}
	}
	return 0
}

func (a *App) portInUse(port int, except string) bool {
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()
	for _, s := range a.cfg.Servers {
		if s.ID != except && s.Port == port {
			return true
		}
	}
	return false
}

func deploymentPortRange() (int, int, bool) {
	start := 5520
	end := 5525
	locked := false
	if v := appEnv("GAME_PORT_START", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 65535 {
			start = n
			locked = true
		}
	}
	if v := appEnv("GAME_PORT_END", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= start && n <= 65535 {
			end = n
			locked = true
		}
	}
	if v := appEnv("GAME_PORT", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 65535 {
			start = n
			if end < start {
				end = start + 19
			}
			locked = true
		}
	}
	if end < start {
		end = start
	}
	return start, end, locked
}

// minecraftPortRange is the TCP range for Minecraft servers. It starts at
// Minecraft's default port, 25565, so players can usually join without
// typing a port. REALMKEEPER_MINECRAFT_PORT_START and _END change it; the same ports
// must be published in Compose or the CasaOS app settings.
func minecraftPortRange() (int, int) {
	start, end := 25565, 25570
	if n, err := strconv.Atoi(appEnv("MINECRAFT_PORT_START", "")); err == nil && n > 0 && n <= 65535 {
		start = n
		if end < start {
			end = min(start+5, 65535)
		}
	}
	if n, err := strconv.Atoi(appEnv("MINECRAFT_PORT_END", "")); err == nil && n >= start && n <= 65535 {
		end = n
	}
	return start, end
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func defaultIf(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}

func ensureRootDirs(dataDir string) error {
	for _, d := range []string{dataDir, filepath.Join(dataDir, "tools"), filepath.Join(dataDir, "servers")} {
		if err := os.MkdirAll(d, 0755); err != nil {
			return err
		}
	}
	return nil
}

func ensureServerDirs(root string) error { return pathsFor(root).Ensure() }

// appEnv reads the setting REALMKEEPER_<name>. Installs from before the
// rename set HSM_<name>, and older ones ORBIS_<name>; both still work.
func appEnv(name, fallback string) string {
	for _, prefix := range []string{"REALMKEEPER_", "HSM_", "ORBIS_"} {
		if v := strings.TrimSpace(os.Getenv(prefix + name)); v != "" {
			return v
		}
	}
	return fallback
}

func envOr(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

func validMemory(v string) bool {
	v = strings.TrimSpace(strings.ToUpper(v))
	if len(v) < 2 {
		return false
	}
	unit := v[len(v)-1]
	if unit != 'G' && unit != 'M' && unit != 'K' {
		return false
	}
	n, err := strconv.Atoi(v[:len(v)-1])
	return err == nil && n > 0
}
