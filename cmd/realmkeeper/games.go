package main

import (
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
)

// GameCapability describes a feature the manager can safely expose for a game.
// Capabilities are explicit so the UI does not imply support that has not been
// implemented and verified for a particular server distribution. The same
// declaration is served to the browser and enforced by the API handlers, so a
// capability must only be set when its backend implementation exists.
type GameCapability struct {
	// Console means process output (logs and the live event stream) is available.
	Console bool `json:"console"`
	// ConsoleCommands means commands can be written to the server's stdin.
	ConsoleCommands bool `json:"consoleCommands"`
	FileManagement  bool `json:"fileManagement"`
	// Backups means full snapshots (create, list, restore, delete) are supported.
	Backups bool `json:"backups"`
	// NativeBackups is Hytale's own /backup command and its schedule settings.
	NativeBackups bool `json:"nativeBackups"`
	// LiveSnapshots means a snapshot can be taken while the server is running.
	LiveSnapshots bool `json:"liveSnapshots"`
	// ServerProperties means the structured server.properties form applies.
	ServerProperties bool `json:"serverProperties"`
	// Configuration means the server settings form applies to this game.
	Configuration bool `json:"configuration"`
	// ConfigFiles lists the structured files the in-app config editor may open.
	ConfigFiles []string `json:"configFiles"`
	Mods        bool     `json:"mods"`
	// Worlds means the Worlds tab manages the server's save files.
	Worlds bool `json:"worlds,omitempty"`
	// ModsKind names what the Mods tab manages: "mods" or "plugins".
	ModsKind   string `json:"modsKind,omitempty"`
	PlayerList bool   `json:"playerList"`
	// PlayerManagement means the Players tab manages the whitelist,
	// operators and bans (Minecraft).
	PlayerManagement bool `json:"playerManagement"`
	// ModBrowser means mods or plugins can be installed from Modrinth.
	ModBrowser bool `json:"modBrowser"`
	// DataPacks means world data packs can be managed and installed from
	// Modrinth (every Minecraft server).
	DataPacks bool `json:"dataPacks,omitempty"`
	// ModPacks means Modrinth mod packs can be installed (servers with a
	// mod loader).
	ModPacks    bool     `json:"modPacks,omitempty"`
	DeviceAuth  bool     `json:"deviceAuth"`
	GameUpdates bool     `json:"gameUpdates"`
	Variants    []string `json:"variants"`
}

// GameCatalogEntry is metadata for the create-server UI. Available means that
// this release can create and manage the game end-to-end, not merely that the
// game is planned.
type GameCatalogEntry struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Available    bool           `json:"available"`
	Protocol     string         `json:"protocol"`
	Capabilities GameCapability `json:"capabilities"`
	Art          GameArt        `json:"art"`
	// Rules are present for available games.
	Rules *GameRules `json:"rules,omitempty"`
}

// GameArt holds URLs for optional official artwork bundled under
// web/assets/games/art. Empty fields mean the file is absent and the browser
// falls back to a generated monogram, so a missing image is never requested.
type GameArt struct {
	Icon   string `json:"icon,omitempty"`
	Logo   string `json:"logo,omitempty"`
	Banner string `json:"banner,omitempty"`
}

var gameArtExtensions = []string{"png", "webp", "jpg", "jpeg", "svg"}

// gameArtFrom looks for <id>-icon, <id>-logo and <id>-banner images in fsys, which is
// rooted at the embedded web directory.
func gameArtFrom(fsys fs.FS, id string) GameArt {
	find := func(kind string) string {
		for _, ext := range gameArtExtensions {
			name := "assets/games/art/" + id + "-" + kind + "." + ext
			if info, err := fs.Stat(fsys, name); err == nil && !info.IsDir() {
				return "/" + name
			}
		}
		return ""
	}
	return GameArt{Icon: find("icon"), Logo: find("logo"), Banner: find("banner")}
}

func embeddedGameArt(id string) GameArt {
	web, err := fs.Sub(webFS, "web")
	if err != nil {
		return GameArt{}
	}
	return gameArtFrom(web, id)
}

var hytaleConfigFiles = []string{"config.json", "permissions.json", "whitelist.json", "bans.json"}

func supportedGameCatalog() []GameCatalogEntry {
	return []GameCatalogEntry{
		{
			ID: "hytale", Name: "Hytale", Available: true, Protocol: "udp",
			Capabilities: GameCapability{
				Console: true, ConsoleCommands: true, FileManagement: true, Backups: true, NativeBackups: true,
				Configuration: true, ConfigFiles: hytaleConfigFiles, Mods: true, PlayerList: true,
				DeviceAuth: true, GameUpdates: true,
				Variants: []string{"dedicated"},
			},
		},
		{
			ID: "minecraft-java", Name: "Minecraft Java Edition", Available: true, Protocol: "tcp",
			Capabilities: GameCapability{
				Console: true, ConsoleCommands: true, FileManagement: true, Configuration: true,
				Backups: true, LiveSnapshots: true, PlayerList: true, PlayerManagement: true, ServerProperties: true,
				ConfigFiles: []string{}, Variants: minecraftVariants,
			},
		},
		{
			// Snapshots are taken while stopped: vanilla Terraria has no way to
			// pause world saving during a copy.
			ID: "terraria", Name: "Terraria", Available: true, Protocol: "tcp",
			Capabilities: GameCapability{
				Console: true, ConsoleCommands: true, FileManagement: true, Configuration: true,
				Backups: true, PlayerList: true, ConfigFiles: []string{}, Variants: []string{"vanilla", "tmodloader"},
			},
		},
		{
			// Dragonwilds is stopped with SIGINT and does not read commands from stdin.
			ID: "runescape-dragonwilds", Name: "RuneScape: Dragonwilds", Available: true, Protocol: "udp",
			Capabilities: GameCapability{
				Console: true, FileManagement: true, Configuration: true, Backups: true, Worlds: true,
				ConfigFiles: []string{}, Variants: []string{"dedicated"},
			},
		},
	}
}

// normalizeGameType maps persisted game identifiers to catalog IDs. An empty
// value predates multi-game support and therefore means Hytale.
func normalizeGameType(gameType string) string {
	gameType = strings.TrimSpace(strings.ToLower(gameType))
	if gameType == "" {
		return "hytale"
	}
	return gameType
}

// gameCatalogEntryFor returns the catalog entry for an available game. Planned
// and unknown games fail closed with no capabilities.
func gameCatalogEntryFor(gameType string) (GameCatalogEntry, bool) {
	gameType = normalizeGameType(gameType)
	for _, entry := range catalogWithArt() {
		if entry.ID == gameType && entry.Available {
			return entry, true
		}
	}
	return GameCatalogEntry{ID: gameType, Name: gameType, Capabilities: GameCapability{ConfigFiles: []string{}, Variants: []string{}}}, false
}

func gameCapabilitiesFor(gameType string) GameCapability {
	entry, _ := gameCatalogEntryFor(gameType)
	return entry.Capabilities
}

// modFolder is where a server's add-ons live and which files it accepts.
type modFolder struct {
	Dir  string // relative to the server root
	Exts []string
	Kind string // "mods" or "plugins"
	// EnabledList means the folder's enabled.json must list a mod for it to
	// load (tModLoader).
	EnabledList bool
}

// modFolderFor depends on the variant as well as the game: vanilla Minecraft
// has no add-ons, Paper has plugins and Fabric has mods.
func modFolderFor(s ServerSettings) (modFolder, bool) {
	switch normalizeGameType(s.GameType) {
	case "hytale":
		return modFolder{Dir: filepath.Join("game", "Server", "mods"), Exts: []string{".jar", ".zip"}, Kind: "mods"}, true
	case "minecraft-java":
		switch s.MinecraftVariant {
		case "paper":
			return modFolder{Dir: filepath.Join("game", "plugins"), Exts: []string{".jar"}, Kind: "plugins"}, true
		case "fabric", "quilt", "forge", "neoforge":
			return modFolder{Dir: filepath.Join("game", "mods"), Exts: []string{".jar"}, Kind: "mods"}, true
		}
	case "terraria":
		if terrariaVariant(s) == "tmodloader" {
			return modFolder{Dir: filepath.Join("game", "Mods"), Exts: []string{".tmod"}, Kind: "mods", EnabledList: true}, true
		}
	}
	return modFolder{}, false
}

// serverCapabilities is a server's game capabilities adjusted for its variant.
func serverCapabilities(s ServerSettings) GameCapability {
	c := gameCapabilitiesFor(s.GameType)
	f, ok := modFolderFor(s)
	c.Mods, c.ModsKind = ok, f.Kind
	_, c.ModBrowser = modrinthLoaders[s.MinecraftVariant]
	c.ModBrowser = c.ModBrowser && ok && normalizeGameType(s.GameType) == "minecraft-java"
	c.DataPacks = normalizeGameType(s.GameType) == "minecraft-java"
	_, c.ModPacks = mrpackLoaderKeys[s.MinecraftVariant]
	c.ModPacks = c.ModPacks && c.DataPacks
	return c
}

func (c GameCapability) allowsConfigFile(name string) bool {
	for _, allowed := range c.ConfigFiles {
		if allowed == name {
			return true
		}
	}
	return false
}

// requireCapability writes a 409 response and returns false when the server's
// game does not support a feature. Handlers call it before touching game state
// so, for example, Hytale console commands are never written to another game.
func requireCapability(w http.ResponseWriter, s ServerSettings, feature string, supported func(GameCapability) bool) bool {
	entry, _ := gameCatalogEntryFor(s.GameType)
	if supported(serverCapabilities(s)) {
		return true
	}
	jsonError(w, http.StatusConflict, feature+" is not supported for "+entry.Name+" servers")
	return false
}

// catalogWithArt is the catalog as served to the browser, with artwork URLs.
func catalogWithArt() []GameCatalogEntry {
	games := supportedGameCatalog()
	for i := range games {
		games[i].Art = embeddedGameArt(games[i].ID)
		if adapter, err := gameAdapterFor(games[i].ID); err == nil && games[i].Available {
			rules := adapter.Rules().resolved()
			games[i].Rules = &rules
		}
	}
	return games
}

func (a *App) handleGameCatalog(w http.ResponseWriter, r *http.Request) {
	jsonOut(w, 200, map[string]any{
		"games": catalogWithArt(),
		// Values remembered from earlier servers, offered by the create wizard.
		"defaults": map[string]string{"dragonwildsOwnerId": a.rememberedDragonwildsOwner()},
	})
}
