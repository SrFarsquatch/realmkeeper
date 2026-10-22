package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

func (a *App) handleServerConfigGet(w http.ResponseWriter, r *http.Request) {
	a.serveServerJSONGet(w, r, "config.json")
}

func (a *App) handleServerConfigPut(w http.ResponseWriter, r *http.Request) {
	a.serveServerJSONPut(w, r, "config.json")
}

func (a *App) handleServerFileGet(w http.ResponseWriter, r *http.Request) {
	name, ok := allowedServerJSONName(r.PathValue("name"))
	if !ok {
		jsonError(w, 404, "unsupported server file")
		return
	}
	a.serveServerJSONGet(w, r, name)
}

func (a *App) handleServerFilePut(w http.ResponseWriter, r *http.Request) {
	name, ok := allowedServerJSONName(r.PathValue("name"))
	if !ok {
		jsonError(w, 404, "unsupported server file")
		return
	}
	a.serveServerJSONPut(w, r, name)
}

func (a *App) serveServerJSONGet(w http.ResponseWriter, r *http.Request, name string) {
	m, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if !requireCapability(w, s, "Editing "+name, func(c GameCapability) bool { return c.allowsConfigFile(name) }) {
		return
	}
	path := filepath.Join(pathsFor(a.serverRoot(s)).Game(), "Server", name)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		jsonOut(w, 200, map[string]any{"name": name, "exists": false, "content": "{}", "running": m.IsRunning(), "editable": !m.IsRunning()})
		return
	}
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	jsonOut(w, 200, map[string]any{"name": name, "exists": true, "content": string(b), "running": m.IsRunning(), "editable": !m.IsRunning()})
}

func (a *App) serveServerJSONPut(w http.ResponseWriter, r *http.Request, name string) {
	m, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if !requireCapability(w, s, "Editing "+name, func(c GameCapability) bool { return c.allowsConfigFile(name) }) {
		return
	}
	if m.IsRunning() {
		jsonError(w, 409, "stop the Hytale server before editing configuration files")
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := decodeJSON(r, &body); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	var tmp any
	if err := json.Unmarshal([]byte(body.Content), &tmp); err != nil {
		jsonError(w, 400, "file must contain valid JSON: "+err.Error())
		return
	}
	path := filepath.Join(pathsFor(a.serverRoot(s)).Game(), "Server", name)
	if err := atomicWrite(path, []byte(body.Content), 0644); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true, "name": name})
}

func allowedServerJSONName(name string) (string, bool) {
	switch name {
	case "config.json", "permissions.json", "whitelist.json", "bans.json":
		return name, true
	}
	return "", false
}

func (a *App) handleModsList(w http.ResponseWriter, r *http.Request) {
	_, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if !requireCapability(w, s, "Mods", func(c GameCapability) bool { return c.Mods }) {
		return
	}
	folder, _ := modFolderFor(s)
	dir := filepath.Join(a.serverRoot(s), folder.Dir)
	_ = os.MkdirAll(dir, 0755)
	entries, err := os.ReadDir(dir)
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	out := []FileInfoDTO{}
	for _, e := range entries {
		if e.IsDir() || !slices.Contains(folder.Exts, strings.ToLower(filepath.Ext(e.Name()))) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, FileInfoDTO{Name: e.Name(), Size: info.Size(), Mod: info.ModTime().Format(time.RFC3339)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	jsonOut(w, 200, out)
}

func (a *App) handleModUpload(w http.ResponseWriter, r *http.Request) {
	m, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if !requireCapability(w, s, "Mods", func(c GameCapability) bool { return c.Mods }) {
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		jsonError(w, 400, "invalid upload: "+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		jsonError(w, 400, "file is required")
		return
	}
	defer file.Close()
	folder, _ := modFolderFor(s)
	ext := strings.ToLower(filepath.Ext(header.Filename))
	if !slices.Contains(folder.Exts, ext) {
		jsonError(w, 400, "upload a "+strings.Join(folder.Exts, " or ")+" file")
		return
	}
	name := filepath.Base(header.Filename)
	dir := filepath.Join(a.serverRoot(s), folder.Dir)
	_ = os.MkdirAll(dir, 0755)
	if err := streamToFile(file, filepath.Join(dir, name), 0644); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	if folder.EnabledList {
		if err := setModEnabled(dir, name, true); err != nil {
			jsonError(w, 500, "mod saved, but it could not be enabled: "+err.Error())
			return
		}
	}
	m.appendLog("controller", "Installed "+strings.TrimSuffix(folder.Kind, "s")+": "+name)
	jsonOut(w, 201, map[string]any{"ok": true, "name": name})
}

func (a *App) handleModDelete(w http.ResponseWriter, r *http.Request) {
	_, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if !requireCapability(w, s, "Mods", func(c GameCapability) bool { return c.Mods }) {
		return
	}
	name := filepath.Base(r.PathValue("name"))
	if name == "." || name == "" {
		jsonError(w, 400, "invalid mod name")
		return
	}
	folder, _ := modFolderFor(s)
	dir := filepath.Join(a.serverRoot(s), folder.Dir)
	if name == "enabled.json" {
		jsonError(w, 400, "enabled.json is managed by Realmkeeper")
		return
	}
	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			jsonError(w, 404, "mod not found")
		} else {
			jsonError(w, 500, err.Error())
		}
		return
	}
	if folder.EnabledList {
		if err := setModEnabled(dir, name, false); err != nil {
			jsonError(w, 500, "mod removed, but enabled.json could not be updated: "+err.Error())
			return
		}
	}
	jsonOut(w, 200, map[string]any{"ok": true})
}

func (a *App) applyHytaleConfig(s ServerSettings) error {
	serverDir := filepath.Join(pathsFor(a.serverRoot(s)).Game(), "Server")
	path := filepath.Join(serverDir, "config.json")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		// Avoid writing game configuration before the authorized server files are installed.
		if !fileExists(filepath.Join(serverDir, "HytaleServer.jar")) {
			return nil
		}
		// Create a minimal current-schema config so first startup honors the wizard choices.
		cfg := map[string]any{
			"Version":                 4,
			"ServerName":              s.ServerName,
			"MOTD":                    s.MOTD,
			"Password":                s.Password,
			"Port":                    s.Port,
			"MaxPlayers":              s.MaxPlayers,
			"MaxViewRadius":           12,
			"RequireJoinPermission":   s.WhitelistEnabled,
			"Defaults":                map[string]any{"World": "default", "GameMode": s.GameMode},
			"ConnectionTimeouts":      map[string]any{"JoinTimeouts": map[string]any{}},
			"RateLimit":               map[string]any{},
			"Modules":                 map[string]any{},
			"LogLevels":               map[string]any{},
			"Mods":                    map[string]any{},
			"DisplayTmpTagsInStrings": false,
			"PlayerStorage":           map[string]any{"Type": "Hytale"},
			"AuthCredentialStore":     map[string]any{"Type": "Encrypted", "Path": "auth.enc"},
		}
		if s.HytalePatchline != "" {
			cfg["Update"] = map[string]any{"Patchline": s.HytalePatchline}
		}
		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		return atomicWrite(path, out, 0644)
	}
	if err != nil {
		return err
	}
	out, err := patchHytaleConfig(b, s)
	if err != nil {
		return fmt.Errorf("invalid config.json: %w", err)
	}
	if bytes.Equal(out, b) {
		return nil // already current; leave the game's file untouched
	}
	return atomicWrite(path, out, 0644)
}

// patchHytaleConfig sets the values Realmkeeper manages in an existing
// config.json and keeps every other byte, including the sections the game
// writes itself (such as its backup settings), exactly as they were.
func patchHytaleConfig(b []byte, s ServerSettings) ([]byte, error) {
	if !json.Valid(b) {
		return nil, errors.New("the file is not valid JSON")
	}
	out := b
	var err error
	for _, m := range []struct {
		key   string
		value any
	}{
		{"ServerName", s.ServerName}, {"Port", s.Port}, {"MOTD", s.MOTD}, {"Password", s.Password},
		{"MaxPlayers", s.MaxPlayers}, {"RequireJoinPermission", s.WhitelistEnabled},
	} {
		if out, err = setHytaleConfigValue(out, m.key, m.value); err != nil {
			return nil, err
		}
	}
	defaults, err := jsonMember(out, "Defaults")
	if err != nil {
		return nil, err
	}
	if len(defaults) == 0 || defaults[0] != '{' {
		defaults = []byte("{}")
	}
	if defaults, err = setHytaleConfigValue(defaults, "GameMode", s.GameMode); err != nil {
		return nil, err
	}
	if out, err = setJSONMemberRaw(out, "Defaults", defaults); err != nil {
		return nil, err
	}
	// The backup frequency goes in the game's own Backup section; a config
	// without one keeps the game's defaults.
	backup, err := jsonMember(out, "Backup")
	if err != nil {
		return nil, err
	}
	if len(backup) > 0 && backup[0] == '{' && s.BackupFrequency > 0 {
		if backup, err = setHytaleConfigValue(backup, "FrequencyMinutes", s.BackupFrequency); err != nil {
			return nil, err
		}
		if out, err = setJSONMemberRaw(out, "Backup", backup); err != nil {
			return nil, err
		}
	}
	return patchHytalePatchline(out, s.HytalePatchline)
}

// setHytaleConfigValue sets a member unless it already holds that value, so
// an unchanged file is not rewritten (a number spelled 20.0 stays as it is).
func setHytaleConfigValue(doc []byte, key string, value any) ([]byte, error) {
	current, err := jsonMember(doc, key)
	if err != nil {
		return nil, err
	}
	if current != nil {
		want, err := encodeJSONValue(value)
		if err != nil {
			return nil, err
		}
		var a, b any
		if json.Unmarshal(current, &a) == nil && json.Unmarshal(want, &b) == nil && fmt.Sprint(a) == fmt.Sprint(b) {
			return doc, nil
		}
	}
	return setJSONMember(doc, key, value)
}

func (a *App) writeJVMOptionsFor(s ServerSettings) error {
	if !validMemory(s.MemoryMin) || !validMemory(s.MemoryMax) {
		return errors.New("invalid JVM memory settings")
	}
	lines := []string{"# Managed by Realmkeeper - " + s.ServerName, "-Xms" + s.MemoryMin, "-Xmx" + s.MemoryMax, "-XX:+UseG1GC"}
	return atomicWrite(filepath.Join(pathsFor(a.serverRoot(s)).Game(), "jvm.options"), []byte(strings.Join(lines, "\n")+"\n"), 0644)
}
