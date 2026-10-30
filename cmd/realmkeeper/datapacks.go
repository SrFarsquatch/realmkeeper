package main

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Data packs live in the world's datapacks folder, as .zip files or
// unpacked folders. They are listed and removed here; Modrinth installs them
// (modrinth.go, kind "datapack").

func (a *App) datapackRequest(w http.ResponseWriter, r *http.Request) (modrinthTarget, bool) {
	_, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return modrinthTarget{}, false
	}
	if !requireCapability(w, s, "Data packs", func(c GameCapability) bool { return c.DataPacks }) {
		return modrinthTarget{}, false
	}
	t, _ := a.datapackTargetFor(s)
	return t, true
}

func (a *App) handleDatapacks(w http.ResponseWriter, r *http.Request) {
	t, ok := a.datapackRequest(w, r)
	if !ok {
		return
	}
	type pack struct {
		Name     string    `json:"name"`
		Folder   bool      `json:"folder"`
		Size     int64     `json:"size"`
		Modified time.Time `json:"modified"`
		Title    string    `json:"title,omitempty"`
		Version  string    `json:"version,omitempty"`
	}
	byFile := map[string]modrinthInstall{}
	for _, inst := range currentModrinthInstalls(t) {
		byFile[inst.Filename] = inst
	}
	entries, _ := os.ReadDir(t.Dir)
	out := []pack{}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || strings.HasPrefix(e.Name(), ".") || (!e.IsDir() && !strings.EqualFold(filepath.Ext(e.Name()), ".zip")) {
			continue
		}
		p := pack{Name: e.Name(), Folder: e.IsDir(), Modified: info.ModTime()}
		if !e.IsDir() {
			p.Size = info.Size()
		}
		if inst, ok := byFile[e.Name()]; ok {
			p.Title, p.Version = inst.Title, inst.VersionNumber
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	jsonOut(w, 200, map[string]any{"datapacks": out, "world": filepath.Base(filepath.Dir(t.Dir))})
}

func (a *App) handleDeleteDatapack(w http.ResponseWriter, r *http.Request) {
	t, ok := a.datapackRequest(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if name == "" || name != filepath.Base(name) || name == "." || name == ".." || strings.HasPrefix(name, ".") {
		jsonError(w, 400, "invalid data pack name")
		return
	}
	target := filepath.Join(t.Dir, name)
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		jsonError(w, 404, "data pack not found")
		return
	}
	if err == nil && info.IsDir() {
		err = os.RemoveAll(target)
	} else if err == nil {
		err = os.Remove(target)
	}
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true, "message": "Removed " + name + ". Run /reload in the console or restart the server to apply it"})
}
