package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ServerSummary struct {
	ID            string `json:"id"`
	GameType      string `json:"gameType"`
	ServerName    string `json:"serverName"`
	Port          int    `json:"port"`
	PublicHost    string `json:"publicHost"`
	Running       bool   `json:"running"`
	Installed     bool   `json:"installed"`
	InstallBusy   bool   `json:"installBusy"`
	State         string `json:"state"`
	ExitCode      int    `json:"exitCode"`
	LastExitAt    string `json:"lastExitAt,omitempty"`
	UptimeSeconds int64  `json:"uptimeSeconds"`
	StorageDir    string `json:"storageDir"`
	Primary       bool   `json:"primary"`
	// Protocol and Capabilities come from the game catalog so the browser does
	// not keep its own, possibly divergent, copy of what each game supports.
	Protocol     string         `json:"protocol"`
	Capabilities GameCapability `json:"capabilities"`
	Art          GameArt        `json:"art"`
	// PortPublished is false when the manager's container does not publish
	// this server's port, so players cannot reach it. It is absent when the
	// container's ports are unknown.
	PortPublished *bool      `json:"portPublished,omitempty"`
	Rules         *GameRules `json:"rules,omitempty"`
}

func (a *App) handleServersList(w http.ResponseWriter, r *http.Request) {
	a.cfgMu.RLock()
	servers := append([]ServerSettings(nil), a.cfg.Servers...)
	a.cfgMu.RUnlock()
	out := make([]ServerSummary, 0, len(servers))
	for i, s := range servers {
		m, _, err := a.serverByID(s.ID)
		if err != nil {
			continue
		}
		st := m.Status()
		game, _ := gameCatalogEntryFor(s.GameType)
		out = append(out, ServerSummary{ID: s.ID, GameType: s.GameType, ServerName: s.ServerName, Port: s.Port, PublicHost: s.PublicHost, Running: st.Running, Installed: st.Installed, InstallBusy: st.InstallBusy, State: st.State, ExitCode: st.ExitCode, LastExitAt: st.LastExitAt, UptimeSeconds: st.UptimeSeconds, StorageDir: s.StorageDir, Primary: i == 0, Protocol: game.Protocol, Capabilities: serverCapabilities(s), Art: game.Art, Rules: game.Rules})
		if published, ok := a.ports.published(s.Port, game.Protocol); ok {
			out[len(out)-1].PortPublished = &published
		}
	}
	jsonOut(w, 200, out)
}

// newServerRequest is the body of POST /api/servers.
type newServerRequest struct {
	Game                     string `json:"game"`
	DragonwildsOwnerID       string `json:"dragonwildsOwnerId"`
	DragonwildsWorldName     string `json:"dragonwildsWorldName"`
	DragonwildsAdminPassword string `json:"dragonwildsAdminPassword"`
	DragonwildsWorldPassword string `json:"dragonwildsWorldPassword"`
	DragonwildsPublic        bool   `json:"dragonwildsPublic"`
	Name                     string `json:"name"`
	Port                     int    `json:"port"`
	GameMode                 string `json:"gameMode"`
	MaxPlayers               int    `json:"maxPlayers"`
	MOTD                     string `json:"motd"`
	Password                 string `json:"password"`
	WhitelistEnabled         bool   `json:"whitelistEnabled"`
	PublicHost               string `json:"publicHost"`
	MemoryMin                string `json:"memoryMin"`
	MemoryMax                string `json:"memoryMax"`
	BackupFrequency          int    `json:"backupFrequency"`
	AutoStart                bool   `json:"autoStart"`
	SourceServerID           string `json:"sourceServerId"`
	MinecraftVersion         string `json:"minecraftVersion"`
	MinecraftVariant         string `json:"minecraftVariant"`
	MinecraftEulaAccepted    bool   `json:"minecraftEulaAccepted"`
	TerrariaVariant          string `json:"terrariaVariant"`
	TerrariaVersion          string `json:"terrariaVersion"`
	TerrariaWorldName        string `json:"terrariaWorldName"`
	TerrariaWorldSize        string `json:"terrariaWorldSize"`
	HytalePatchline          string `json:"hytalePatchline"`
	// rememberedOwnerID is the saved Dragonwilds Owner ID, used when the
	// request leaves the Owner ID empty. Set by the server, never decoded.
	rememberedOwnerID string
}

func (a *App) handleServersCreate(w http.ResponseWriter, r *http.Request) {
	var body newServerRequest
	if err := decodeJSON(r, &body); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	game := normalizeGameType(body.Game)
	adapter, adapterErr := gameAdapterFor(game)
	entry, available := gameCatalogEntryFor(game)
	if adapterErr != nil || !available {
		jsonError(w, 400, fmt.Sprintf("%q is not supported by this release; see GET /api/games for supported games", game))
		return
	}
	body.rememberedOwnerID = a.rememberedDragonwildsOwner()
	if err := adapter.ValidateNew(&body); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	rules := adapter.Rules()
	name := strings.TrimSpace(body.Name)
	if len(name) < 2 || len(name) > 64 {
		jsonError(w, 400, "server name must be between 2 and 64 characters")
		return
	}
	if body.GameMode == "" {
		body.GameMode = rules.GameModes[0]
	}
	if err := rules.checkGameMode(body.GameMode); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	if body.MaxPlayers == 0 {
		body.MaxPlayers = rules.DefaultMaxPlayers
	}
	if err := rules.checkMaxPlayers(body.MaxPlayers, entry.Name); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	if body.MOTD == "" {
		body.MOTD = rules.DefaultMOTD
	}
	if body.MemoryMin == "" {
		body.MemoryMin = "2G"
	}
	if body.MemoryMax == "" {
		body.MemoryMax = "4G"
	}
	if !validMemory(body.MemoryMin) || !validMemory(body.MemoryMax) {
		jsonError(w, 400, "memory values must look like 2G, 4096M, or 1024K")
		return
	}
	if body.BackupFrequency == 0 {
		body.BackupFrequency = 30
	}
	if body.BackupFrequency < 5 || body.BackupFrequency > 10080 {
		jsonError(w, 400, "backup frequency must be between 5 and 10080 minutes")
		return
	}
	id := a.uniqueServerID(slugify(name))
	if id == "" {
		jsonError(w, 500, "could not generate server id")
		return
	}
	min, max := rules.portRange()
	port := body.Port
	if port == 0 {
		port = a.firstFreePort(min, max)
		if port == 0 {
			jsonError(w, 409, fmt.Sprintf("every game port in the published range %d-%d is already assigned", min, max))
			return
		}
	}
	if port < min || port > max {
		jsonError(w, 400, fmt.Sprintf("port must be within the published %s range %d-%d", strings.ToUpper(entry.Protocol), min, max))
		return
	}
	if a.portInUse(port, "") {
		jsonError(w, 409, "that game port is already assigned to another server")
		return
	}
	s := defaultServerSettings(id, name, port, filepath.Join("servers", id))
	s.GameType = game
	s.MinecraftVersion = body.MinecraftVersion
	s.MinecraftVariant = body.MinecraftVariant
	s.MinecraftEulaAccepted = body.MinecraftEulaAccepted
	s.DragonwildsOwnerID = strings.TrimSpace(body.DragonwildsOwnerID)
	s.DragonwildsWorldName = strings.TrimSpace(body.DragonwildsWorldName)
	s.DragonwildsAdminPassword = body.DragonwildsAdminPassword
	s.DragonwildsWorldPassword = body.DragonwildsWorldPassword
	s.DragonwildsPublic = body.DragonwildsPublic
	s.TerrariaVariant = body.TerrariaVariant
	s.TerrariaVersion = body.TerrariaVersion
	s.TerrariaWorldName = body.TerrariaWorldName
	s.TerrariaWorldSize = body.TerrariaWorldSize
	s.HytalePatchline = body.HytalePatchline
	s.GameMode = body.GameMode
	s.MaxPlayers = body.MaxPlayers
	s.MOTD = body.MOTD
	s.Password = body.Password
	s.WhitelistEnabled = body.WhitelistEnabled
	s.PublicHost = strings.TrimSpace(body.PublicHost)
	s.MemoryMin = body.MemoryMin
	s.MemoryMax = body.MemoryMax
	s.BackupFrequency = body.BackupFrequency
	s.AutoStart = body.AutoStart
	// Validate a clone source before creating the destination directory. Otherwise
	// a stale, running, or incompatible source can leave an orphan server folder.
	var cloneSource *ServerManager
	var cloneSettings ServerSettings
	if body.SourceServerID != "" {
		srcM, srcS, err := a.serverByID(body.SourceServerID)
		if err != nil {
			jsonError(w, 404, "source server not found")
			return
		}
		if srcS.GameType != s.GameType {
			jsonError(w, 400, "servers can only be cloned from the same game type")
			return
		}
		if srcM.IsRunning() {
			jsonError(w, 409, "stop the source server before cloning it")
			return
		}
		if !srcM.Installed() {
			jsonError(w, 409, "source server is not installed")
			return
		}
		cloneSource = srcM
		cloneSettings = srcS
	}

	destinationRoot := a.serverRoot(s)
	if err := ensureServerDirs(destinationRoot); err != nil {
		_ = removeServerData(destinationRoot, false)
		jsonError(w, 500, err.Error())
		return
	}
	if cloneSource != nil {
		if err := cloneGameInstall(a.serverRoot(cloneSettings), destinationRoot); err != nil {
			_ = removeServerData(destinationRoot, false)
			jsonError(w, 500, "clone failed: "+err.Error())
			return
		}
	}

	a.cfgMu.Lock()
	a.cfg.Servers = append(a.cfg.Servers, s)
	err := a.saveConfigLocked()
	if err != nil {
		// Roll back the in-memory entry as well as the on-disk destination.
		a.cfg.Servers = a.cfg.Servers[:len(a.cfg.Servers)-1]
	}
	a.cfgMu.Unlock()
	if err != nil {
		_ = removeServerData(destinationRoot, false)
		jsonError(w, 500, err.Error())
		return
	}
	m := a.newServerManager(s)
	a.managersMu.Lock()
	a.managers[id] = m
	a.managersMu.Unlock()
	// The server is saved; a game file that cannot be written yet is written
	// again before the first start.
	_ = adapter.ApplySettings(a, m, s)
	jsonOut(w, 201, settingsView(s))
}

func (a *App) handleServersDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m, s, err := a.serverByID(id)
	if err != nil {
		jsonError(w, 404, "server not found")
		return
	}
	if m.IsRunning() {
		jsonError(w, 409, "stop the server before deleting it")
		return
	}
	a.cfgMu.Lock()
	idx := -1
	for i, x := range a.cfg.Servers {
		if x.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		a.cfgMu.Unlock()
		jsonError(w, 404, "server not found")
		return
	}
	a.cfg.Servers = append(a.cfg.Servers[:idx], a.cfg.Servers[idx+1:]...)
	a.removeServerSchedulesLocked(id)
	err = a.saveConfigLocked()
	a.cfgMu.Unlock()
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	a.managersMu.Lock()
	delete(a.managers, id)
	a.managersMu.Unlock()
	if err := removeServerData(a.serverRoot(s), s.StorageDir == "" || s.StorageDir == "."); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true})
}

// removeServerData removes a server's files without deleting manager-wide state.
// The legacy primary server lives directly in dataDir, alongside config.json,
// shared tools, and other server directories, so only its server-owned folders
// may be removed in that case.
func removeServerData(root string, sharedRoot bool) error {
	if !sharedRoot {
		return os.RemoveAll(root)
	}
	for _, name := range append([]string{"tmp"}, legacyRootEntries...) {
		if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
			return fmt.Errorf("remove server data %s: %w", name, err)
		}
	}
	return nil
}

func (a *App) handleServerStartByPath(w http.ResponseWriter, r *http.Request) {
	a.resetCrashes(r.PathValue("id"))
	if err := a.startServerByID(r.PathValue("id")); err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	jsonOut(w, 202, map[string]any{"ok": true})
}

func (a *App) handleServerStopByPath(w http.ResponseWriter, r *http.Request) {
	m, _, err := a.serverByID(r.PathValue("id"))
	if err != nil {
		jsonError(w, 404, "server not found")
		return
	}
	if err := m.Stop(); err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	jsonOut(w, 202, map[string]any{"ok": true})
}

func (a *App) handleServerRestartByPath(w http.ResponseWriter, r *http.Request) {
	a.restartByID(w, r.PathValue("id"))
}

func (a *App) requestServer(r *http.Request) (*ServerManager, ServerSettings, error) {
	id := strings.TrimSpace(r.Header.Get("X-HSM-Server"))
	if id == "" {
		id = strings.TrimSpace(r.Header.Get("X-Orbis-Server")) // legacy client compatibility
	}
	if id == "" {
		id = strings.TrimSpace(r.URL.Query().Get("server"))
	}
	if id == "" {
		// Single-server clients from before multi-server support omit the ID.
		// With several servers, guessing could stop or edit the wrong one.
		a.cfgMu.RLock()
		count := len(a.cfg.Servers)
		if count == 1 {
			id = a.cfg.Servers[0].ID
		}
		a.cfgMu.RUnlock()
		if count > 1 {
			return nil, ServerSettings{}, errors.New("server id is required when more than one server is configured")
		}
	}
	return a.serverByID(id)
}

func (a *App) serverByID(id string) (*ServerManager, ServerSettings, error) {
	a.cfgMu.RLock()
	var s ServerSettings
	found := false
	for _, x := range a.cfg.Servers {
		if x.ID == id {
			s = x
			found = true
			break
		}
	}
	a.cfgMu.RUnlock()
	if !found {
		return nil, s, errors.New("server not found")
	}
	a.managersMu.RLock()
	m := a.managers[id]
	a.managersMu.RUnlock()
	if m == nil {
		return nil, s, errors.New("server manager missing")
	}
	return m, s, nil
}

func (a *App) serverRoot(s ServerSettings) string {
	if s.StorageDir == "" || s.StorageDir == "." {
		return a.dataDir
	}
	return filepath.Join(a.dataDir, filepath.Clean(s.StorageDir))
}

func (a *App) ensureManagers() {
	a.cfgMu.RLock()
	servers := append([]ServerSettings(nil), a.cfg.Servers...)
	a.cfgMu.RUnlock()
	a.managersMu.Lock()
	defer a.managersMu.Unlock()
	for _, s := range servers {
		_ = ensureServerDirs(a.serverRoot(s))
		if a.managers[s.ID] == nil {
			a.managers[s.ID] = a.newServerManager(s)
		}
	}
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	m, _, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	jsonOut(w, 200, m.Status())
}

func (a *App) handleResources(w http.ResponseWriter, r *http.Request) {
	jsonOut(w, 200, a.resourceMetrics())
}

// SettingsView is ServerSettings as sent to the browser. Passwords are never
// returned; the *Set fields say whether each one is configured.
type SettingsView struct {
	ServerSettings
	PasswordSet                 bool `json:"passwordSet"`
	DragonwildsAdminPasswordSet bool `json:"dragonwildsAdminPasswordSet"`
	DragonwildsWorldPasswordSet bool `json:"dragonwildsWorldPasswordSet"`
}

func settingsView(s ServerSettings) SettingsView {
	v := SettingsView{
		ServerSettings:              s,
		PasswordSet:                 s.Password != "",
		DragonwildsAdminPasswordSet: s.DragonwildsAdminPassword != "",
		DragonwildsWorldPasswordSet: s.DragonwildsWorldPassword != "",
	}
	v.Password = ""
	v.DragonwildsAdminPassword = ""
	v.DragonwildsWorldPassword = ""
	return v
}

// settingsUpdate is the PUT /api/settings body. Clear flags remove optional
// passwords explicitly, because a blank password field means "unchanged".
type settingsUpdate struct {
	ServerSettings
	ClearPassword                 bool `json:"clearPassword"`
	ClearDragonwildsWorldPassword bool `json:"clearDragonwildsWorldPassword"`
	// Read-only SettingsView fields, accepted and ignored so a GET response
	// can be sent back unchanged.
	PasswordSet                 bool `json:"passwordSet"`
	DragonwildsAdminPasswordSet bool `json:"dragonwildsAdminPasswordSet"`
	DragonwildsWorldPasswordSet bool `json:"dragonwildsWorldPasswordSet"`
}

func keepSecret(submitted, current string, clear bool) string {
	if clear {
		return ""
	}
	if submitted == "" {
		return current
	}
	return submitted
}

func (a *App) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	_, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	min, max := portRangeForGame(s.GameType)
	s.PortMin = min
	s.PortMax = max
	jsonOut(w, 200, settingsView(s))
}

func (a *App) handleSettingsPut(w http.ResponseWriter, r *http.Request) {
	m, current, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	var req settingsUpdate
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	in := req.ServerSettings
	// Secrets are never sent to the browser, so a blank value means "keep".
	in.Password = keepSecret(in.Password, current.Password, req.ClearPassword)
	in.DragonwildsAdminPassword = keepSecret(in.DragonwildsAdminPassword, current.DragonwildsAdminPassword, false)
	in.DragonwildsWorldPassword = keepSecret(in.DragonwildsWorldPassword, current.DragonwildsWorldPassword, req.ClearDragonwildsWorldPassword)
	in.ID = current.ID
	in.GameType = current.GameType // game identity is immutable through generic settings updates
	in.StorageDir = current.StorageDir
	adapter, err := gameAdapterFor(current.GameType)
	if err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	entry, _ := gameCatalogEntryFor(current.GameType)
	rules := adapter.Rules()
	if err := adapter.MergeSettings(&in, current); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	min, max := rules.portRange()
	in.PortMin = min
	in.PortMax = max
	in.ServerName = strings.TrimSpace(in.ServerName)
	if len(in.ServerName) < 2 || len(in.ServerName) > 64 || strings.ContainsAny(in.ServerName, "\r\n\x00") {
		jsonError(w, 400, "server name must be between 2 and 64 characters")
		return
	}
	// An unchanged port is kept even outside the game's range, so servers
	// created before a range changed (Minecraft moved to 25565+) still save.
	if in.Port != current.Port && (in.Port < min || in.Port > max) {
		jsonError(w, 400, fmt.Sprintf("port must be within the Docker/CasaOS %s range %d-%d", strings.ToUpper(entry.Protocol), min, max))
		return
	}
	if a.portInUse(in.Port, current.ID) {
		jsonError(w, 409, "that game port is already assigned to another server")
		return
	}
	if !validMemory(in.MemoryMin) || !validMemory(in.MemoryMax) {
		jsonError(w, 400, "memory values must look like 2G, 4096M, or 1024K")
		return
	}
	if in.GameMode == "" {
		in.GameMode = current.GameMode
	}
	if err := rules.checkGameMode(in.GameMode); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	if in.MaxPlayers == 0 {
		in.MaxPlayers = current.MaxPlayers
	}
	if err := rules.checkMaxPlayers(in.MaxPlayers, entry.Name); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	if in.MOTD == "" {
		in.MOTD = current.MOTD
	}
	if in.BackupFrequency == 0 {
		in.BackupFrequency = current.BackupFrequency
	}
	if in.BackupFrequency < 5 || in.BackupFrequency > 10080 {
		jsonError(w, 400, "backup frequency must be between 5 and 10080 minutes")
		return
	}
	if in.BackupRetention == 0 {
		in.BackupRetention = current.BackupRetention
	}
	if in.BackupRetention < 1 || in.BackupRetention > 100 {
		jsonError(w, 400, "automatic backup retention must be between 1 and 100")
		return
	}
	if in.SnapshotRetention == 0 {
		in.SnapshotRetention = current.SnapshotRetention
	}
	if in.SnapshotRetention < 1 || in.SnapshotRetention > 100 {
		jsonError(w, 400, "snapshot retention must be between 1 and 100")
		return
	}
	if m.IsRunning() && in.Port != current.Port {
		jsonError(w, 409, "stop the server before changing its port")
		return
	}
	a.cfgMu.Lock()
	for i := range a.cfg.Servers {
		if a.cfg.Servers[i].ID == current.ID {
			a.cfg.Servers[i] = in
			break
		}
	}
	err = a.saveConfigLocked()
	a.cfgMu.Unlock()
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	if err := adapter.ApplySettings(a, m, in); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	jsonOut(w, 200, settingsView(in))
}

func (a *App) handleStart(w http.ResponseWriter, r *http.Request) {
	_, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	a.resetCrashes(s.ID)
	if err := a.startServerByID(s.ID); err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	jsonOut(w, 202, map[string]any{"ok": true})
}

func (a *App) startServerByID(id string) error {
	m, s, err := a.serverByID(id)
	if err != nil {
		return err
	}
	adapter, err := gameAdapterFor(s.GameType)
	if err != nil {
		return err
	}
	args, err := adapter.PrepareLaunch(a, s)
	if err != nil {
		return err
	}
	return m.Start(args)
}

func (a *App) handleStop(w http.ResponseWriter, r *http.Request) {
	m, _, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if err := m.Stop(); err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	jsonOut(w, 202, map[string]any{"ok": true})
}

func (a *App) restartByID(w http.ResponseWriter, id string) {
	m, _, err := a.serverByID(id)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if !m.IsRunning() {
		if err := a.startServerByID(id); err != nil {
			jsonError(w, 409, err.Error())
			return
		}
		jsonOut(w, 202, map[string]any{"ok": true})
		return
	}
	go func() {
		_ = m.Stop()
		deadline := time.Now().Add(100 * time.Second)
		for m.IsRunning() && time.Now().Before(deadline) {
			time.Sleep(500 * time.Millisecond)
		}
		if !m.IsRunning() {
			if err := a.startServerByID(id); err != nil {
				m.appendLog("controller", "Restart failed: "+err.Error())
			}
		}
	}()
	jsonOut(w, 202, map[string]any{"ok": true})
}

func (a *App) handleRestart(w http.ResponseWriter, r *http.Request) {
	_, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	a.restartByID(w, s.ID)
}
