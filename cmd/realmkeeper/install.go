package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

func (a *App) handleInstallStatus(w http.ResponseWriter, r *http.Request) {
	m, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	adapter, err := gameAdapterFor(s.GameType)
	if err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	out := adapter.InstallStatus(a, s)
	out["installed"] = m.Installed()
	out["busy"] = m.InstallBusy()
	jsonOut(w, 200, out)
}

func (a *App) handleDownloaderUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(128 << 20); err != nil {
		jsonError(w, 400, "invalid upload: "+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		jsonError(w, 400, "file is required")
		return
	}
	defer file.Close()
	if strings.HasSuffix(strings.ToLower(header.Filename), ".exe") {
		jsonError(w, 400, "upload the Linux hytale-downloader or official hytale-downloader.zip")
		return
	}
	toolsDir := filepath.Join(a.dataDir, "tools")
	_ = os.MkdirAll(toolsDir, 0755)
	uploadPath := filepath.Join(toolsDir, ".hytale-downloader-upload")
	_ = os.Remove(uploadPath)
	if err := streamToFile(file, uploadPath, 0644); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	defer os.Remove(uploadPath)
	dst := filepath.Join(toolsDir, "hytale-downloader")
	_ = os.Remove(dst)
	if strings.HasSuffix(strings.ToLower(header.Filename), ".zip") || isZipFile(uploadPath) {
		tmp := filepath.Join(toolsDir, ".downloader-unpack")
		_ = os.RemoveAll(tmp)
		defer os.RemoveAll(tmp)
		if err := extractZip(uploadPath, tmp); err != nil {
			jsonError(w, 400, "could not unpack downloader archive: "+err.Error())
			return
		}
		candidate, err := locateDownloaderBinary(tmp)
		if err != nil {
			jsonError(w, 400, err.Error())
			return
		}
		if err := copyFile(candidate, dst, 0755); err != nil {
			jsonError(w, 500, err.Error())
			return
		}
	} else if err := copyFile(uploadPath, dst, 0755); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	_ = os.Chmod(dst, 0755)
	jsonOut(w, 201, map[string]any{"ok": true, "filename": header.Filename})
}

func (a *App) handleDownloaderRun(w http.ResponseWriter, r *http.Request) {
	m, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if normalizeGameType(s.GameType) != "hytale" {
		jsonError(w, 400, "select a Hytale server before running the Hytale downloader")
		return
	}
	if m.IsRunning() {
		jsonError(w, 409, "stop the server before installing server files")
		return
	}
	if err := m.RunDownloader(s.HytalePatchline); err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	jsonOut(w, 202, map[string]any{"ok": true})
}

func (a *App) handleImportLocal(w http.ResponseWriter, r *http.Request) {
	m, s, err := a.requestServer(r)
	if err != nil {
		jsonError(w, 404, err.Error())
		return
	}
	if normalizeGameType(s.GameType) != "hytale" {
		jsonError(w, 400, "select a Hytale server before importing a Hytale server bundle")
		return
	}
	if m.IsRunning() {
		jsonError(w, 409, "stop the server before importing server files")
		return
	}
	if err := m.ImportBundle(filepath.Join(pathsFor(a.serverRoot(s)).Imports(), "game.zip")); err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	jsonOut(w, 202, map[string]any{"ok": true})
}

// RunDownloader downloads the server files for a Hytale patchline ("" for
// release) with the official downloader, then installs them.
func (m *ServerManager) RunDownloader(patchline string) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return errors.New("server is running")
	}
	if m.installBusy {
		m.mu.Unlock()
		return errors.New("an install job is already running")
	}
	downloader := filepath.Join(m.toolsDir, "hytale-downloader")
	if !fileExists(downloader) {
		m.mu.Unlock()
		return errors.New("official Hytale downloader is not uploaded yet")
	}
	_ = os.Chmod(downloader, 0755)
	m.installBusy = true
	m.mu.Unlock()
	go func() {
		defer func() { m.mu.Lock(); m.installBusy = false; m.mu.Unlock() }()
		bundle := filepath.Join(pathsFor(m.rootDir).Imports(), "game.zip")
		_ = os.Remove(bundle)
		args := []string{"-download-path", bundle}
		if patchline != "" {
			args = append(args, "-patchline", patchline)
			m.appendLog("installer", "Downloading the "+patchline+" build.")
		}
		m.appendLog("installer", "Launching official Hytale Downloader. Complete the device authorization shown below.")
		cmd := exec.Command(downloader, args...)
		cmd.Dir = m.toolsDir
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			m.appendLog("installer", err.Error())
			return
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			m.appendLog("installer", err.Error())
			return
		}
		if err := cmd.Start(); err != nil {
			m.appendLog("installer", "Downloader failed to start: "+err.Error())
			return
		}
		// Read both pipes to the end before Wait, which closes them;
		// otherwise the downloader's last lines can be lost.
		done := make(chan struct{}, 2)
		go func() { m.scanPipe(stdout, "installer"); done <- struct{}{} }()
		go func() { m.scanPipe(stderr, "installer"); done <- struct{}{} }()
		<-done
		<-done
		if err := cmd.Wait(); err != nil {
			m.appendLog("installer", "Downloader exited with an error: "+err.Error())
			return
		}
		m.appendLog("installer", "Download complete; importing game.zip")
		if err := m.importBundleSync(bundle); err != nil {
			m.appendLog("installer", "Import failed: "+err.Error())
			return
		}
		m.appendLog("installer", "Hytale server files installed successfully")
	}()
	return nil
}

func (m *ServerManager) ImportBundle(path string) error {
	m.mu.Lock()
	if m.installBusy {
		m.mu.Unlock()
		return errors.New("an install job is already running")
	}
	if !fileExists(path) {
		m.mu.Unlock()
		return errors.New("game.zip was not found in this server's imports folder")
	}
	m.installBusy = true
	m.mu.Unlock()
	go func() {
		defer func() { m.mu.Lock(); m.installBusy = false; m.mu.Unlock() }()
		m.appendLog("installer", "Importing mounted game.zip")
		if err := m.importBundleSync(path); err != nil {
			m.appendLog("installer", "Import failed: "+err.Error())
			return
		}
		m.appendLog("installer", "Hytale server files installed successfully")
	}()
	return nil
}

func (m *ServerManager) importBundleSync(path string) error {
	tmp := filepath.Join(pathsFor(m.rootDir).Tmp(), "import")
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0755); err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := extractZip(path, tmp); err != nil {
		return err
	}
	root, err := locateGameRoot(tmp)
	if err != nil {
		return err
	}
	dest := pathsFor(m.rootDir).Game()
	if fileExists(filepath.Join(dest, "Server", "HytaleServer.jar")) {
		return errors.New("a server is already installed; use Hytale's built-in updater instead of overwriting it")
	}
	_ = os.RemoveAll(dest)
	if err := copyDir(root, dest); err != nil {
		return err
	}
	if fileExists(filepath.Join(dest, "start.sh")) {
		_ = os.Chmod(filepath.Join(dest, "start.sh"), 0755)
	}
	return nil
}

func isZipFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	b := make([]byte, 4)
	if _, err := io.ReadFull(f, b); err != nil {
		return false
	}
	return string(b) == "PK\x03\x04" || string(b) == "PK\x05\x06" || string(b) == "PK\x07\x08"
}

func locateDownloaderBinary(root string) (string, error) {
	var matches []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := strings.ToLower(d.Name())
		if name == "hytale-downloader" || strings.HasPrefix(name, "hytale-downloader-linux") {
			matches = append(matches, path)
		}
		return nil
	})
	if len(matches) == 0 {
		return "", errors.New("archive does not contain a Linux hytale-downloader binary")
	}
	sort.Strings(matches)
	return matches[0], nil
}

func extractZip(src, dst string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		clean := filepath.Clean(f.Name)
		if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("unsafe archive path: %s", f.Name)
		}
		target := filepath.Join(dst, clean)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		mode := f.Mode()
		if mode == 0 {
			mode = 0644
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			rc.Close()
			return err
		}
		_, e1 := io.Copy(out, rc)
		e2 := out.Close()
		e3 := rc.Close()
		if e1 != nil {
			return e1
		}
		if e2 != nil {
			return e2
		}
		if e3 != nil {
			return e3
		}
	}
	return nil
}

func locateGameRoot(root string) (string, error) {
	candidates := []string{root, pathsFor(root).Game(), filepath.Join(root, "HytaleServer")}
	for _, c := range candidates {
		if fileExists(filepath.Join(c, "Server", "HytaleServer.jar")) && fileExists(filepath.Join(c, "Assets.zip")) {
			return c, nil
		}
	}
	var found string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if fileExists(filepath.Join(path, "Server", "HytaleServer.jar")) && fileExists(filepath.Join(path, "Assets.zip")) {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	if found == "" {
		return "", errors.New("game.zip did not contain Server/HytaleServer.jar and Assets.zip")
	}
	return found, nil
}

func cloneGameInstall(srcRoot, dstRoot string) error {
	src := pathsFor(srcRoot).Game()
	dst := pathsFor(dstRoot).Game()
	if !fileExists(filepath.Join(src, "Server", "HytaleServer.jar")) {
		return errors.New("source server files are missing")
	}
	_ = os.RemoveAll(dst)
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == filepath.Join("Server", "universe") || strings.HasPrefix(rel, filepath.Join("Server", "universe")+string(os.PathSeparator)) || rel == filepath.Join("Server", "backups") || strings.HasPrefix(rel, filepath.Join("Server", "backups")+string(os.PathSeparator)) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(path, target, info.Mode())
	})
}
