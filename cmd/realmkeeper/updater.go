package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	latestReleaseAPI = "https://api.github.com/repos/SrFarsquatch/realmkeeper/releases/latest"
	containerImage   = "ghcr.io/srfarsquatch/realmkeeper"
	dockerSocketPath = "/var/run/docker.sock"
)

type AppRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
}

type AppUpdateInfo struct {
	CurrentVersion  string    `json:"currentVersion"`
	LatestVersion   string    `json:"latestVersion"`
	UpdateAvailable bool      `json:"updateAvailable"`
	ReleaseURL      string    `json:"releaseUrl,omitempty"`
	ReleaseNotes    string    `json:"releaseNotes,omitempty"`
	PublishedAt     time.Time `json:"publishedAt,omitempty"`
	UpdaterReady    bool      `json:"updaterReady"`
	Status          string    `json:"status"`
	Message         string    `json:"message,omitempty"`
}

type appUpdateState struct {
	mu        sync.RWMutex
	status    string
	message   string
	target    string
	startedAt time.Time
}

// snapshot expires an update state that has stopped making progress. The manager
// process can survive a failed helper, so an in-memory "updating" flag must not
// disable retries forever.
func (s *appUpdateState) snapshot(now time.Time) (status, message, target string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if (s.status == "starting" || s.status == "updating") &&
		!s.startedAt.IsZero() && now.Sub(s.startedAt) > 12*time.Minute {
		s.status = "failed"
		s.message = "The update has not restarted the manager after 12 minutes. The helper may have failed; inspect the hsm-update-helper container logs, then retry."
		s.target = ""
	}
	return s.status, s.message, s.target
}

var updateState = appUpdateState{status: "idle"}

func (a *App) handleAppUpdateCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	info, err := checkAppUpdate(ctx)
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	info.UpdaterReady = dockerSocketAvailable()
	if !info.UpdaterReady {
		info.Message = "Container updates require the Docker socket mount. Add it to the Compose configuration and recreate this container."
	}
	info.Status, info.Message, _ = updateState.snapshot(time.Now())
	if info.Status == "failed" {
		info.UpdateAvailable = compareVersions(info.LatestVersion, info.CurrentVersion) > 0
	}
	jsonOut(w, http.StatusOK, info)
}

func (a *App) handleAppUpdateApply(w http.ResponseWriter, r *http.Request) {
	updateState.mu.Lock()
	if updateState.status == "starting" || updateState.status == "updating" {
		updateState.mu.Unlock()
		jsonError(w, http.StatusConflict, "An application update is already in progress")
		return
	}
	updateState.status = "starting"
	updateState.message = "Checking the latest release…"
	updateState.startedAt = time.Now()
	updateState.mu.Unlock()

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	info, err := checkAppUpdate(ctx)
	cancel()
	if err != nil {
		setUpdateState("failed", err.Error(), "")
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	if !info.UpdateAvailable {
		setUpdateState("idle", "You're already running the latest release.", "")
		jsonOut(w, http.StatusOK, info)
		return
	}
	if !dockerSocketAvailable() {
		setUpdateState("failed", "Docker socket is not available to the updater.", "")
		jsonError(w, http.StatusServiceUnavailable, "In-container updates aren't enabled in this deployment. Mount /var/run/docker.sock and recreate the container first.")
		return
	}
	containerID, err := currentContainerID()
	if err != nil {
		setUpdateState("failed", err.Error(), "")
		jsonError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	targetImage := containerImage + ":latest"
	setUpdateState("updating", "Starting the updater helper. The dashboard will briefly reconnect when the new version starts.", info.LatestVersion)
	helperID, err := launchUpdateHelper(containerID, targetImage)
	if err != nil {
		setUpdateState("failed", err.Error(), "")
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	go watchUpdateHelper(newDockerAPI(), helperID, 3*time.Second)
	jsonOut(w, http.StatusAccepted, map[string]any{
		"ok":            true,
		"status":        "updating",
		"targetVersion": info.LatestVersion,
		"message":       "Update started. The manager will restart automatically; refresh the page if it does not reconnect.",
	})
}

func checkAppUpdate(ctx context.Context) (AppUpdateInfo, error) {
	info := AppUpdateInfo{CurrentVersion: version, LatestVersion: version, Status: "idle"}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestReleaseAPI, nil)
	if err != nil {
		return info, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "Realmkeeper-Updater")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return info, fmt.Errorf("could not check GitHub releases: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		info.Message = "No stable release has been published yet."
		return info, nil
	}
	if resp.StatusCode != http.StatusOK {
		return info, fmt.Errorf("GitHub releases returned HTTP %d", resp.StatusCode)
	}
	var release AppRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&release); err != nil {
		return info, fmt.Errorf("could not read release information: %w", err)
	}
	tag := strings.TrimSpace(strings.TrimPrefix(release.TagName, "v"))
	if tag == "" || release.Draft || release.Prerelease {
		info.Message = "No stable release is available."
		return info, nil
	}
	if !validReleaseVersion(tag) {
		return info, fmt.Errorf("release tag %q is not a supported version", release.TagName)
	}
	info.LatestVersion = tag
	info.ReleaseURL = release.HTMLURL
	info.ReleaseNotes = release.Body
	info.PublishedAt = release.PublishedAt
	info.UpdateAvailable = compareVersions(tag, version) > 0
	if !info.UpdateAvailable {
		info.Message = "You're running the latest release."
	}
	return info, nil
}

var releaseVersionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$`)

func validReleaseVersion(v string) bool { return releaseVersionPattern.MatchString(v) }

func compareVersions(a, b string) int {
	parse := func(v string) ([3]int, string) {
		var nums [3]int
		base, pre, _ := strings.Cut(v, "-")
		parts := strings.Split(base, ".")
		for i := 0; i < len(parts) && i < 3; i++ {
			nums[i], _ = strconv.Atoi(parts[i])
		}
		return nums, pre
	}
	av, ap := parse(strings.TrimPrefix(a, "v"))
	bv, bp := parse(strings.TrimPrefix(b, "v"))
	for i := 0; i < 3; i++ {
		if av[i] > bv[i] {
			return 1
		}
		if av[i] < bv[i] {
			return -1
		}
	}
	if ap == bp {
		return 0
	}
	if ap == "" {
		return 1
	}
	if bp == "" {
		return -1
	}
	if ap > bp {
		return 1
	}
	return -1
}

// handleAppUpdateStatus reports an update's progress without asking GitHub,
// so the page can poll it while an update runs.
func (a *App) handleAppUpdateStatus(w http.ResponseWriter, r *http.Request) {
	status, message, target := updateState.snapshot(time.Now())
	jsonOut(w, http.StatusOK, map[string]any{"status": status, "message": message, "targetVersion": target, "currentVersion": version})
}

func setUpdateState(status, message, target string) {
	updateState.mu.Lock()
	updateState.status, updateState.message, updateState.target = status, message, target
	updateState.mu.Unlock()
}

func dockerSocketAvailable() bool {
	info, err := os.Stat(dockerSocketPath)
	return err == nil && info.Mode()&os.ModeSocket != 0
}

func isDockerContainerID(value string) bool {
	return len(value) >= 12 && len(value) <= 64 && regexp.MustCompile("^[a-fA-F0-9]+$").MatchString(value)
}

func currentContainerID() (string, error) {
	host := strings.TrimSpace(os.Getenv("HOSTNAME"))
	if host == "" {
		b, err := os.ReadFile("/etc/hostname")
		if err == nil {
			host = strings.TrimSpace(string(b))
		}
	}
	// Docker normally sets HOSTNAME to the container ID, but CasaOS and
	// Compose deployments may override it with a human-readable container name.
	if isDockerContainerID(host) {
		return host, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	d := newDockerAPI()
	listBytes, err := d.call(ctx, http.MethodGet, "/containers/json?all=1", nil, http.StatusOK)
	if err != nil {
		return "", fmt.Errorf("could not resolve container hostname %q through the Docker socket: %w", host, err)
	}
	var containers []struct {
		ID    string   `json:"Id"`
		Names []string `json:"Names"`
	}
	if err := json.Unmarshal(listBytes, &containers); err != nil {
		return "", fmt.Errorf("decode Docker container list: %w", err)
	}

	// Prefer an exact Docker container-name match; CasaOS commonly sets
	// HOSTNAME to the same friendly name as container_name.
	var matches []string
	for _, container := range containers {
		for _, name := range container.Names {
			if strings.TrimPrefix(name, "/") == host && container.ID != "" {
				matches = append(matches, container.ID)
				break
			}
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("hostname %q matches multiple Docker containers; set a unique container name", host)
	}

	// Some orchestrators set a custom hostname independently of the name.
	for _, container := range containers {
		if container.ID == "" {
			continue
		}
		inspectBytes, inspectErr := d.call(ctx, http.MethodGet, "/containers/"+url.PathEscape(container.ID)+"/json", nil, http.StatusOK)
		if inspectErr != nil {
			continue
		}
		var inspect struct {
			Config struct {
				Hostname string `json:"Hostname"`
			} `json:"Config"`
		}
		if json.Unmarshal(inspectBytes, &inspect) == nil && inspect.Config.Hostname == host {
			matches = append(matches, container.ID)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("hostname %q matches multiple Docker containers; set a unique container hostname", host)
	}
	return "", fmt.Errorf("could not identify this Docker container: HOSTNAME %q is not a container ID and did not match a Docker container name or hostname; check the Docker socket mount or update the app from CasaOS", host)
}

type dockerAPI struct{ client *http.Client }

func newDockerAPI() *dockerAPI {
	return &dockerAPI{client: &http.Client{
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", dockerSocketPath)
		}},
		Timeout: 0,
	}}
}

func (d *dockerAPI) request(ctx context.Context, method, endpoint string, body any) (*http.Response, error) {
	var reader io.Reader
	contentType := "application/json"
	if stream, ok := body.(io.Reader); ok {
		// A stream (an image archive for /images/load) is sent as is.
		reader, contentType = stream, "application/x-tar"
	} else if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+endpoint, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	return d.client.Do(req)
}

func (d *dockerAPI) call(ctx context.Context, method, endpoint string, body any, allowed ...int) ([]byte, error) {
	resp, err := d.request(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if readErr != nil {
		return nil, readErr
	}
	for _, code := range allowed {
		if resp.StatusCode == code {
			return b, nil
		}
	}
	var payload struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(b, &payload)
	if payload.Message == "" {
		payload.Message = strings.TrimSpace(string(b))
	}
	return nil, fmt.Errorf("Docker API %s %s returned HTTP %d: %s", method, endpoint, resp.StatusCode, payload.Message)
}

func launchUpdateHelper(containerID, targetImage string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d := newDockerAPI()
	inspectBytes, err := d.call(ctx, http.MethodGet, "/containers/"+url.PathEscape(containerID)+"/json", nil, http.StatusOK)
	if err != nil {
		return "", fmt.Errorf("inspect current container: %w", err)
	}
	var inspect struct {
		Config     map[string]any `json:"Config"`
		Image      string         `json:"Image"`
		HostConfig struct {
			DNS []string `json:"Dns"`
		} `json:"HostConfig"`
	}
	if err := json.Unmarshal(inspectBytes, &inspect); err != nil {
		return "", err
	}
	if inspect.Image == "" {
		return "", errors.New("Docker did not report the current application image")
	}
	hostConfig := map[string]any{
		"Binds":         []string{dockerSocketPath + ":" + dockerSocketPath},
		"RestartPolicy": map[string]any{"Name": "no"},
		"AutoRemove":    false,
	}
	// The helper may have to download the image itself (see
	// pullViaRegistry), so it uses the manager's DNS servers, which the
	// Compose files set because the host's resolver can be unreliable.
	if len(inspect.HostConfig.DNS) > 0 {
		hostConfig["Dns"] = inspect.HostConfig.DNS
	}
	socketInfo, err := os.Stat(dockerSocketPath)
	if err == nil {
		if st, ok := socketInfo.Sys().(*syscall.Stat_t); ok {
			hostConfig["GroupAdd"] = []string{strconv.FormatUint(uint64(st.Gid), 10)}
		}
	}
	name := "hsm-update-helper-" + strconv.FormatInt(time.Now().Unix(), 10)
	// The helper runs the binary directly as root rather than through the
	// image's entrypoint (tini, then gosu to the unprivileged user): it needs
	// only the Docker socket, and the entrypoint's /data and group setup is
	// for the manager.
	createBody := map[string]any{
		"Image":      inspect.Image,
		"Entrypoint": []string{"/usr/local/bin/realmkeeper"},
		"Cmd":        []string{"--update-helper"},
		"User":       "0:0",
		"Env":        []string{"REALMKEEPER_UPDATE_CONTAINER_ID=" + containerID, "REALMKEEPER_UPDATE_TARGET_IMAGE=" + targetImage},
		"HostConfig": hostConfig,
	}
	createBytes, err := d.call(ctx, http.MethodPost, "/containers/create?name="+url.QueryEscape(name), createBody, http.StatusCreated)
	if err != nil {
		return "", fmt.Errorf("create updater helper: %w", err)
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(createBytes, &created); err != nil || created.ID == "" {
		return "", errors.New("Docker created the updater helper but returned no container ID")
	}
	if _, err := d.call(ctx, http.MethodPost, "/containers/"+created.ID+"/start", nil, http.StatusNoContent, http.StatusNotModified); err != nil {
		_, _ = d.call(context.Background(), http.MethodDelete, "/containers/"+created.ID+"?force=true&v=true", nil, http.StatusNoContent)
		return "", fmt.Errorf("start updater helper: %w", err)
	}
	return created.ID, nil
}

func runUpdateHelper() {
	containerID := appEnv("UPDATE_CONTAINER_ID", "")
	targetImage := appEnv("UPDATE_TARGET_IMAGE", "")
	if containerID == "" || targetImage == "" {
		fmt.Fprintln(os.Stderr, "update helper requires REALMKEEPER_UPDATE_CONTAINER_ID and REALMKEEPER_UPDATE_TARGET_IMAGE")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	if err := performContainerUpdate(ctx, containerID, targetImage); err != nil {
		fmt.Println("Update failed:", err)
		os.Exit(1)
	}
}

func performContainerUpdate(ctx context.Context, containerID, targetImage string) error {
	if targetImage != containerImage+":latest" {
		return errors.New("refusing to install an image outside the official stable image tag")
	}
	d := newDockerAPI()
	repo, tag, hasTag := strings.Cut(targetImage, ":")
	if !hasTag || tag != "latest" {
		return errors.New("refusing to install an invalid image tag")
	}
	if !hasTag || repo != containerImage {
		return errors.New("invalid target image")
	}
	helperLog("Downloading " + targetImage + "…")
	if err := dockerPull(ctx, d, repo, tag); err != nil {
		// Docker resolves the registry with the host's DNS, which can fail
		// while this container's own DNS works; download it directly.
		helperLog("Docker could not download the image (" + err.Error() + "); downloading it directly instead")
		if err2 := pullViaRegistry(ctx, d, &http.Client{Timeout: 10 * time.Minute}, targetImage); err2 != nil {
			return fmt.Errorf("pull release image: Docker: %v; direct download: %w. Docker downloads with the host's DNS; if the host cannot resolve ghcr.io, fix the host's DNS (with Tailscale, for example: tailscale set --accept-dns=false)", err, err2)
		}
	}
	helperLog("Download complete")

	inspectBytes, err := d.call(ctx, http.MethodGet, "/containers/"+url.PathEscape(containerID)+"/json", nil, http.StatusOK)
	if err != nil {
		return fmt.Errorf("inspect current manager: %w", err)
	}
	var inspect map[string]any
	if err := json.Unmarshal(inspectBytes, &inspect); err != nil {
		return err
	}
	config, ok := inspect["Config"].(map[string]any)
	if !ok {
		return errors.New("Docker inspection did not include container configuration")
	}
	name, _ := inspect["Name"].(string)
	name = strings.TrimPrefix(name, "/")
	if name == "" {
		name = "realmkeeper"
	}
	config["Image"] = targetImage
	config["Hostname"] = ""
	hostConfig, ok := inspect["HostConfig"].(map[string]any)
	if !ok {
		return errors.New("Docker inspection did not include host configuration")
	}
	networking := make(map[string]any)
	if settings, ok := inspect["NetworkSettings"].(map[string]any); ok {
		if networks, ok := settings["Networks"].(map[string]any); ok {
			endpoints := make(map[string]any)
			for networkName, raw := range networks {
				n, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				ep := map[string]any{}
				for _, key := range []string{"Aliases", "Links", "DriverOpts", "IPAMConfig"} {
					if v, exists := n[key]; exists && v != nil {
						ep[key] = v
					}
				}
				endpoints[networkName] = ep
			}
			networking["EndpointsConfig"] = endpoints
		}
	}
	createBody := map[string]any{}
	for key, value := range config {
		createBody[key] = value
	}
	createBody["HostConfig"] = hostConfig
	if len(networking) > 0 {
		createBody["NetworkingConfig"] = networking
	}

	shortID := containerID
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}
	oldName := "hsm-rollback-" + shortID
	helperLog("Stopping the current manager…")
	if _, err := d.call(ctx, http.MethodPost, "/containers/"+url.PathEscape(containerID)+"/stop?t=30", nil, http.StatusNoContent, http.StatusNotModified); err != nil {
		return fmt.Errorf("stop current manager: %w", err)
	}
	if _, err := d.call(ctx, http.MethodPost, "/containers/"+url.PathEscape(containerID)+"/rename?name="+url.QueryEscape(oldName), nil, http.StatusNoContent); err != nil {
		_, _ = d.call(context.Background(), http.MethodPost, "/containers/"+url.PathEscape(containerID)+"/start", nil, http.StatusNoContent, http.StatusNotModified)
		return fmt.Errorf("stage previous container for rollback: %w", err)
	}
	helperLog("Starting the new version…")
	newBytes, err := d.call(ctx, http.MethodPost, "/containers/create?name="+url.QueryEscape(name), createBody, http.StatusCreated)
	if err != nil {
		rollbackOldContainer(d, containerID, oldName, name)
		return fmt.Errorf("create updated manager: %w", err)
	}
	var newContainer struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(newBytes, &newContainer); err != nil || newContainer.ID == "" {
		rollbackOldContainer(d, containerID, oldName, name)
		return errors.New("Docker did not return the new manager container ID")
	}
	if _, err := d.call(ctx, http.MethodPost, "/containers/"+newContainer.ID+"/start", nil, http.StatusNoContent, http.StatusNotModified); err != nil {
		_, _ = d.call(context.Background(), http.MethodDelete, "/containers/"+newContainer.ID+"?force=true&v=true", nil, http.StatusNoContent)
		rollbackOldContainer(d, containerID, oldName, name)
		return fmt.Errorf("start updated manager; previous container restored: %w", err)
	}
	// The new version is running with the old container's mounts, ports and restart policy.
	_, _ = d.call(context.Background(), http.MethodDelete, "/containers/"+url.PathEscape(containerID)+"?force=true&v=true", nil, http.StatusNoContent)
	helperLog("Update complete")
	return nil
}

// dockerPull asks Docker to pull the image, trying twice since a lookup
// failure can be momentary.
func dockerPull(ctx context.Context, d *dockerAPI, repo, tag string) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			time.Sleep(3 * time.Second)
		}
		var resp *http.Response
		resp, err = d.request(ctx, http.MethodPost, "/images/create?fromImage="+url.QueryEscape(repo)+"&tag="+url.QueryEscape(tag), nil)
		if err != nil {
			err = fmt.Errorf("pull release image: %w", err)
			continue
		}
		err = readPullProgress(resp)
		resp.Body.Close()
		if err == nil {
			return nil
		}
	}
	return err
}

// helperLog writes a progress line to the helper's output, which the
// manager reads from the container log to show progress and failures.
func helperLog(line string) { fmt.Println(line) }

// readPullProgress reads Docker's pull progress stream to the end (closing
// it early cancels the pull) and returns the first error it reports.
func readPullProgress(resp *http.Response) error {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return fmt.Errorf("pull release image returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	dec := json.NewDecoder(resp.Body)
	for {
		var item struct {
			Status string `json:"status"`
			ID     string `json:"id"`
			Error  string `json:"error"`
		}
		err := dec.Decode(&item)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read pull progress: %w", err)
		}
		if item.Error != "" {
			return fmt.Errorf("pull release image failed: %s", item.Error)
		}
		// Per-layer lines carry an ID; only the overall ones are worth showing.
		if item.ID == "" && item.Status != "" {
			helperLog(item.Status)
		}
	}
}

func rollbackOldContainer(d *dockerAPI, oldID, oldName, originalName string) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	_, _ = d.call(ctx, http.MethodPost, "/containers/"+url.PathEscape(oldID)+"/rename?name="+url.QueryEscape(originalName), nil, http.StatusNoContent)
	_, _ = d.call(ctx, http.MethodPost, "/containers/"+url.PathEscape(oldID)+"/start", nil, http.StatusNoContent, http.StatusNotModified)
	_ = oldName
}
