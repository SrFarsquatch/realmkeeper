package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

var version = "0.6.0"

//go:embed web/*
var webFS embed.FS

type App struct {
	dataDir         string
	cfgMu           sync.RWMutex
	cfg             ControllerConfig
	sessionsMu      sync.Mutex
	sessions        map[string]Session
	loginMu         sync.Mutex
	loginAttempts   map[string]loginAttempt
	managersMu      sync.RWMutex
	managers        map[string]*ServerManager
	resourceMu      sync.Mutex
	lastCPUUsageNS  uint64
	lastCPUSampleAt time.Time
	lastCPUPercent  float64
	hasCPUPercent   bool
	sched           scheduler
	crashMu         sync.Mutex
	crashHistory    map[string][]time.Time
	// crashBackoff overrides the auto-restart delays in tests.
	crashBackoff []time.Duration
	ports        publishedPorts
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--update-helper" {
		runUpdateHelper()
		return
	}
	dataDir := appEnv("DATA_DIR", "/data")
	if err := ensureRootDirs(dataDir); err != nil {
		log.Fatal(err)
	}
	app := &App{dataDir: dataDir, sessions: map[string]Session{}, managers: map[string]*ServerManager{}}
	if err := app.loadConfig(); err != nil {
		log.Printf("config: %v", err)
	}
	app.ensureManagers()
	go app.backupMaintenanceLoop()
	go app.schedulerLoop()
	go app.watchPublishedPorts()
	go cleanupUpdateHelpersLoop()

	mux := http.NewServeMux()
	app.routes(mux)
	addr := appEnv("LISTEN", ":8008")
	srv := &http.Server{Addr: addr, Handler: securityHeaders(mux), ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}

	app.cfgMu.RLock()
	servers := append([]ServerSettings(nil), app.cfg.Servers...)
	app.cfgMu.RUnlock()
	for _, s := range servers {
		if s.AutoStart {
			if m, _, err := app.serverByID(s.ID); err == nil && m.Installed() {
				sid := s.ID
				go func() {
					time.Sleep(1200 * time.Millisecond)
					if err := app.startServerByID(sid); err != nil {
						m.appendLog("controller", "Auto-start failed: "+err.Error())
					}
				}()
			}
		}
	}

	log.Printf("Realmkeeper %s listening on %s", version, addr)
	log.Fatal(srv.ListenAndServe())
}

func (a *App) routes(mux *http.ServeMux) {
	mux.Handle("GET /api/app-update", a.auth(http.HandlerFunc(a.handleAppUpdateCheck)))
	mux.Handle("POST /api/app-update", a.auth(http.HandlerFunc(a.handleAppUpdateApply)))
	mux.Handle("GET /api/app-update/status", a.auth(http.HandlerFunc(a.handleAppUpdateStatus)))
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, 200, map[string]any{"ok": true, "version": version})
	})
	mux.HandleFunc("GET /api/bootstrap", a.handleBootstrapStatus)
	mux.HandleFunc("POST /api/bootstrap", a.handleBootstrapCreate)
	mux.HandleFunc("POST /api/login", a.handleLogin)
	mux.HandleFunc("POST /api/logout", a.handleLogout)

	mux.Handle("GET /api/games", a.auth(http.HandlerFunc(a.handleGameCatalog)))
	mux.Handle("GET /api/servers", a.auth(http.HandlerFunc(a.handleServersList)))
	mux.Handle("POST /api/servers", a.auth(http.HandlerFunc(a.handleServersCreate)))
	mux.Handle("DELETE /api/servers/{id}", a.auth(http.HandlerFunc(a.handleServersDelete)))
	mux.Handle("POST /api/servers/{id}/start", a.auth(http.HandlerFunc(a.handleServerStartByPath)))
	mux.Handle("POST /api/servers/{id}/stop", a.auth(http.HandlerFunc(a.handleServerStopByPath)))
	mux.Handle("POST /api/servers/{id}/restart", a.auth(http.HandlerFunc(a.handleServerRestartByPath)))

	mux.Handle("GET /api/status", a.auth(http.HandlerFunc(a.handleStatus)))
	mux.Handle("GET /api/players", a.auth(http.HandlerFunc(a.handlePlayers)))
	mux.Handle("GET /api/resources", a.auth(http.HandlerFunc(a.handleResources)))
	mux.Handle("GET /api/settings", a.auth(http.HandlerFunc(a.handleSettingsGet)))
	mux.Handle("PUT /api/settings", a.auth(http.HandlerFunc(a.handleSettingsPut)))
	mux.Handle("POST /api/server/start", a.auth(http.HandlerFunc(a.handleStart)))
	mux.Handle("POST /api/server/stop", a.auth(http.HandlerFunc(a.handleStop)))
	mux.Handle("POST /api/server/restart", a.auth(http.HandlerFunc(a.handleRestart)))
	mux.Handle("POST /api/console", a.auth(http.HandlerFunc(a.handleConsole)))
	mux.Handle("GET /api/logs", a.auth(http.HandlerFunc(a.handleLogs)))
	mux.Handle("GET /api/events", a.auth(http.HandlerFunc(a.handleEvents)))

	mux.Handle("GET /api/server/config", a.auth(http.HandlerFunc(a.handleServerConfigGet)))
	mux.Handle("PUT /api/server/config", a.auth(http.HandlerFunc(a.handleServerConfigPut)))
	mux.Handle("GET /api/files", a.auth(http.HandlerFunc(a.handleFilesList)))
	mux.Handle("GET /api/files/content", a.auth(http.HandlerFunc(a.handleFilesRead)))
	mux.Handle("PUT /api/files/content", a.auth(http.HandlerFunc(a.handleFilesWrite)))
	mux.Handle("POST /api/files/mkdir", a.auth(http.HandlerFunc(a.handleFilesMkdir)))
	mux.Handle("POST /api/files/upload", a.auth(http.HandlerFunc(a.handleFilesUpload)))
	mux.Handle("GET /api/files/download", a.auth(http.HandlerFunc(a.handleFilesDownload)))
	mux.Handle("DELETE /api/files", a.auth(http.HandlerFunc(a.handleFilesDelete)))
	mux.Handle("POST /api/files/rename", a.auth(http.HandlerFunc(a.handleFilesRename)))

	mux.Handle("GET /api/server/file/{name}", a.auth(http.HandlerFunc(a.handleServerFileGet)))
	mux.Handle("PUT /api/server/file/{name}", a.auth(http.HandlerFunc(a.handleServerFilePut)))
	mux.Handle("GET /api/mods", a.auth(http.HandlerFunc(a.handleModsList)))
	mux.Handle("POST /api/mods/upload", a.auth(http.HandlerFunc(a.handleModUpload)))
	mux.Handle("DELETE /api/mods/{name}", a.auth(http.HandlerFunc(a.handleModDelete)))
	mux.Handle("GET /api/modrinth/search", a.auth(http.HandlerFunc(a.handleModrinthSearch)))
	mux.Handle("GET /api/modrinth/projects/{project}/versions", a.auth(http.HandlerFunc(a.handleModrinthVersions)))
	mux.Handle("POST /api/modrinth/install", a.auth(http.HandlerFunc(a.handleModrinthInstall)))
	mux.Handle("GET /api/modrinth/updates", a.auth(http.HandlerFunc(a.handleModrinthUpdates)))
	mux.Handle("GET /api/backups", a.auth(http.HandlerFunc(a.handleBackupsList)))
	mux.Handle("POST /api/backups/native", a.auth(http.HandlerFunc(a.handleNativeBackup)))
	mux.Handle("POST /api/backups/snapshot", a.auth(http.HandlerFunc(a.handleSnapshotCreate)))
	mux.Handle("POST /api/backups/{name}/restore", a.auth(http.HandlerFunc(a.handleSnapshotRestore)))
	mux.Handle("DELETE /api/backups/{name}", a.auth(http.HandlerFunc(a.handleSnapshotDelete)))
	mux.Handle("GET /api/install/status", a.auth(http.HandlerFunc(a.handleInstallStatus)))
	mux.Handle("POST /api/install/downloader", a.auth(http.HandlerFunc(a.handleDownloaderUpload)))
	mux.Handle("POST /api/install/run-downloader", a.auth(http.HandlerFunc(a.handleDownloaderRun)))
	mux.Handle("POST /api/install/dragonwilds", a.auth(http.HandlerFunc(a.handleDragonwildsInstall)))
	mux.Handle("POST /api/install/minecraft", a.auth(http.HandlerFunc(a.handleMinecraftInstall)))
	mux.Handle("POST /api/install/terraria", a.auth(http.HandlerFunc(a.handleTerrariaInstall)))
	mux.Handle("GET /api/servers/{id}/export", a.auth(http.HandlerFunc(a.handleServerExport)))
	mux.Handle("POST /api/servers/import", a.auth(http.HandlerFunc(a.handleServerImport)))
	mux.Handle("GET /api/minecraft/players", a.auth(http.HandlerFunc(a.handleMinecraftPlayers)))
	mux.Handle("POST /api/minecraft/players", a.auth(http.HandlerFunc(a.handleMinecraftPlayerAction)))
	mux.Handle("GET /api/worlds", a.auth(http.HandlerFunc(a.handleWorldsList)))
	mux.Handle("POST /api/worlds/upload", a.auth(http.HandlerFunc(a.handleWorldUpload)))
	mux.Handle("POST /api/worlds/use", a.auth(http.HandlerFunc(a.handleWorldUse)))
	mux.Handle("POST /api/worlds/archive", a.auth(http.HandlerFunc(a.handleWorldArchive)))
	mux.Handle("POST /api/worlds/restore", a.auth(http.HandlerFunc(a.handleWorldRestore)))
	mux.Handle("POST /api/worlds/new", a.auth(http.HandlerFunc(a.handleWorldNew)))
	mux.Handle("GET /api/schedules", a.auth(http.HandlerFunc(a.handleSchedulesGet)))
	mux.Handle("PUT /api/schedules", a.auth(http.HandlerFunc(a.handleSchedulesPut)))
	mux.Handle("GET /api/minecraft/properties", a.auth(http.HandlerFunc(a.handleMinecraftPropertiesGet)))
	mux.Handle("PUT /api/minecraft/properties", a.auth(http.HandlerFunc(a.handleMinecraftPropertiesPut)))
	mux.Handle("POST /api/install/import-local", a.auth(http.HandlerFunc(a.handleImportLocal)))
	mux.Handle("POST /api/hytale/patchline", a.auth(http.HandlerFunc(a.handleHytalePatchline)))

	gameUpdates := func(c GameCapability) bool { return c.GameUpdates }
	deviceAuth := func(c GameCapability) bool { return c.DeviceAuth }
	for _, x := range []struct {
		path, cmd, feature string
		supported          func(GameCapability) bool
	}{
		{"/api/update/check", "/update check", "Game updates", gameUpdates}, {"/api/update/download", "/update download", "Game updates", gameUpdates},
		{"/api/update/apply", "/update apply --confirm", "Game updates", gameUpdates}, {"/api/update/status", "/update status", "Game updates", gameUpdates},
		{"/api/update/cancel", "/update cancel", "Game updates", gameUpdates}, {"/api/update/setup", "/update setup", "Game updates", gameUpdates},
		{"/api/update/force-download", "/update download --force", "Game updates", gameUpdates},
		{"/api/auth/device", "/auth login device", "Device authentication", deviceAuth},
	} {
		x := x
		mux.Handle("POST "+x.path, a.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			a.commandShortcut(w, r, x.cmd, x.feature, x.supported)
		})))
	}

	sub, _ := fs.Sub(webFS, "web")
	fileServer := http.FileServer(http.FS(sub))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(sub, p); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}
		rr := r.Clone(r.Context())
		rr.URL.Path = "/"
		fileServer.ServeHTTP(w, rr)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}
