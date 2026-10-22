package main

import (
	"errors"
	"net/http"
	"strings"
)

// Hytale release channels ("patchlines"). A server follows one: the
// downloader installs that channel's build (-patchline), and the server's
// own updater follows Update.Patchline in config.json (or the
// "/update patchline <name>" console command while it runs).
//
// Switching is one-way for worlds: a world opened on pre-release (for
// example with world generation v2) may not load on release again, so a
// stopped server is snapshotted before the switch.

const hytalePreRelease = "pre-release"

// normalizeHytalePatchline maps a requested channel to the stored value:
// "" for release.
func normalizeHytalePatchline(p string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "", "release":
		return "", nil
	case hytalePreRelease, "prerelease":
		return hytalePreRelease, nil
	}
	return "", errors.New("release channel must be release or pre-release")
}

// hytalePatchlineName is the name Hytale uses for a stored channel.
func hytalePatchlineName(p string) string {
	if p == "" {
		return "release"
	}
	return p
}

// patchHytalePatchline sets Update.Patchline in config.json for a server on
// pre-release. For release it only changes a patchline that is already set,
// so a config that never named one is left alone.
func patchHytalePatchline(doc []byte, patchline string) ([]byte, error) {
	update, err := jsonMember(doc, "Update")
	if err != nil {
		return nil, err
	}
	current := ""
	if len(update) > 0 && update[0] == '{' {
		raw, err := jsonMember(update, "Patchline")
		if err != nil {
			return nil, err
		}
		if raw != nil && string(raw) != "null" {
			current = strings.Trim(string(raw), `"`)
		}
	} else {
		update = nil
	}
	if patchline == "" && (current == "" || current == "release") {
		return doc, nil
	}
	if patchline != "" && current == patchline {
		return doc, nil
	}
	if update == nil {
		update = []byte("{}")
	}
	if update, err = setJSONMember(update, "Patchline", hytalePatchlineName(patchline)); err != nil {
		return nil, err
	}
	return setJSONMemberRaw(doc, "Update", update)
}

func (a *App) setHytalePatchline(id, patchline string) error {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	for i := range a.cfg.Servers {
		if a.cfg.Servers[i].ID == id {
			a.cfg.Servers[i].HytalePatchline = patchline
			return a.saveConfigLocked()
		}
	}
	return errors.New("server not found")
}

// handleHytalePatchline switches a Hytale server's release channel. A
// stopped, installed server is snapshotted first and its config.json
// updated; a running server is switched with its console command. Either
// way, the Game updates buttons then move the server to that channel's build.
func (a *App) handleHytalePatchline(w http.ResponseWriter, r *http.Request) {
	m, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if !requireCapability(w, s, "Release channels", func(c GameCapability) bool { return c.GameUpdates }) {
		return
	}
	var body struct {
		Patchline string `json:"patchline"`
	}
	if err := decodeJSON(r, &body); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	patchline, err := normalizeHytalePatchline(body.Patchline)
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	name := hytalePatchlineName(patchline)
	if patchline == s.HytalePatchline {
		jsonOut(w, 200, map[string]any{"patchline": name, "message": "Already on " + name})
		return
	}
	running, installed := m.IsRunning(), m.Installed()
	snapshot := ""
	if !running && installed {
		if snapshot, _, err = a.createSnapshot(m, s); err != nil {
			jsonError(w, 500, "could not take a snapshot before switching: "+err.Error())
			return
		}
	}
	if err := a.setHytalePatchline(s.ID, patchline); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	s.HytalePatchline = patchline
	switch {
	case running:
		if err := m.SendCommand("/update patchline " + name); err != nil {
			jsonError(w, 409, err.Error())
			return
		}
	case installed:
		if err := a.applyHytaleConfig(s); err != nil {
			jsonError(w, 500, "channel saved, but config.json could not be updated: "+err.Error())
			return
		}
	}
	m.appendLog("controller", "Release channel set to "+name)
	message := "Switched to " + name + "."
	if installed {
		message += " Use Check, Download and Apply under Game updates to move the server to the " + name + " build."
	} else {
		message += " The Install tab downloads the " + name + " build."
	}
	if snapshot != "" {
		message += " Snapshot taken first: " + snapshot + "."
	}
	jsonOut(w, 200, map[string]any{"patchline": name, "snapshot": snapshot, "message": message})
}
