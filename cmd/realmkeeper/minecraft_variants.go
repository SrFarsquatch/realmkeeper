package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
)

// Minecraft Java servers come in these variants: Mojang's vanilla server,
// Paper (Bukkit-style plugins in plugins/), and the Fabric, Quilt, Forge and
// NeoForge mod loaders (mods in mods/). The variant is chosen when the server
// is created and fixed afterwards.
var minecraftVariants = []string{"vanilla", "paper", "fabric", "quilt", "forge", "neoforge"}

var (
	paperProjectURL = "https://fill.papermc.io/v3/projects/paper"
	fabricMetaURL   = "https://meta.fabricmc.net/v2"
	// trustedPaperDownload accepts only official HTTPS PaperMC download URLs.
	trustedPaperDownload = func(raw string) bool { return isHTTPSHost(raw, "papermc.io") }
)

// fabricLauncherJar is Fabric's server launcher. On first start it downloads
// Mojang's server.jar next to itself and loads the mods folder.
const fabricLauncherJar = "fabric-server-launch.jar"

func validMinecraftVariant(v string) bool {
	for _, known := range minecraftVariants {
		if v == known {
			return true
		}
	}
	return false
}

// minecraftLaunchJar is the jar `java -jar` runs for a variant.
func minecraftLaunchJar(variant string) string {
	switch variant {
	case "fabric":
		return fabricLauncherJar
	case "quilt":
		return quiltLauncherJar
	}
	return "server.jar"
}

// appUserAgent identifies Realmkeeper to download services. PaperMC rejects
// requests with a generic or missing User-Agent.
func appUserAgent() string {
	return "Realmkeeper/" + version + " (+https://github.com/SrFarsquatch/realmkeeper)"
}

func getJSON(ctx context.Context, client *http.Client, rawURL string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", appUserAgent())
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned HTTP %d", rawURL, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v)
}

// downloadVerified streams rawURL to dst, refusing anything over limit bytes
// and, when sha256Hex is set, anything whose checksum differs.
func downloadVerified(ctx context.Context, client *http.Client, rawURL, dst string, limit int64, sha256Hex string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", appUserAgent())
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(out, hash), io.LimitReader(resp.Body, limit+1))
	if err := out.Close(); copyErr == nil {
		copyErr = err
	}
	switch {
	case copyErr != nil:
		err = copyErr
	case written > limit:
		err = fmt.Errorf("download exceeded the %d MiB safety limit", limit>>20)
	case sha256Hex != "" && !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), sha256Hex):
		err = errors.New("downloaded file does not match its published SHA-256 checksum")
	}
	if err != nil {
		_ = os.Remove(dst)
	}
	return err
}

func isHTTPSHost(raw string, domain string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == domain || strings.HasSuffix(host, "."+domain)
}

type paperBuild struct {
	ID        int    `json:"id"`
	Channel   string `json:"channel"`
	Downloads map[string]struct {
		Name      string `json:"name"`
		URL       string `json:"url"`
		Checksums struct {
			SHA256 string `json:"sha256"`
		} `json:"checksums"`
	} `json:"downloads"`
}

// resolvePaper finds the newest stable Paper build for a Minecraft version.
// "latest" means the newest Minecraft version that has a stable Paper build,
// which can trail Mojang's latest release by a few days.
func resolvePaper(ctx context.Context, client *http.Client, want string) (mcVersion, downloadURL, sha string, err error) {
	var project struct {
		Versions map[string][]string `json:"versions"`
	}
	if err := getJSON(ctx, client, paperProjectURL, &project); err != nil {
		return "", "", "", fmt.Errorf("fetch Paper versions: %w", err)
	}
	var all []string
	for _, group := range project.Versions {
		for _, v := range group {
			if !strings.Contains(v, "-") { // skip pre-releases and release candidates
				all = append(all, v)
			}
		}
	}
	sort.Slice(all, func(i, j int) bool { return compareVersions(all[i], all[j]) > 0 })
	candidates := all
	if want != "" && !strings.EqualFold(want, "latest") {
		candidates = nil
		for _, v := range all {
			if v == want {
				candidates = []string{v}
			}
		}
		if candidates == nil {
			return "", "", "", fmt.Errorf("Paper does not publish Minecraft %s", want)
		}
	} else if len(candidates) > 5 {
		candidates = candidates[:5]
	}
	for _, v := range candidates {
		var builds []paperBuild
		if err := getJSON(ctx, client, paperProjectURL+"/versions/"+url.PathEscape(v)+"/builds", &builds); err != nil {
			return "", "", "", fmt.Errorf("fetch Paper %s builds: %w", v, err)
		}
		best := -1
		for i, b := range builds {
			stable := b.Channel == "STABLE" || b.Channel == "RECOMMENDED"
			if _, ok := b.Downloads["server:default"]; stable && ok && (best < 0 || b.ID > builds[best].ID) {
				best = i
			}
		}
		if best < 0 {
			continue
		}
		dl := builds[best].Downloads["server:default"]
		if !trustedPaperDownload(dl.URL) {
			return "", "", "", errors.New("Paper download URL is not an official HTTPS papermc.io URL")
		}
		if len(dl.Checksums.SHA256) != 64 {
			return "", "", "", errors.New("Paper build has no SHA-256 checksum")
		}
		return v, dl.URL, dl.Checksums.SHA256, nil
	}
	if len(candidates) == 1 {
		return "", "", "", fmt.Errorf("Paper has no stable build for Minecraft %s yet; choose an earlier version", candidates[0])
	}
	return "", "", "", errors.New("Paper has no stable builds for recent Minecraft versions")
}

type fabricVersion struct {
	Version string `json:"version"`
	Stable  bool   `json:"stable"`
}

// resolveFabric returns the server launcher URL for a Minecraft version with
// the newest stable Fabric loader and installer.
func resolveFabric(ctx context.Context, client *http.Client, want string) (mcVersion, downloadURL string, err error) {
	return resolveFabricLoader(ctx, client, want, "")
}

// fabricLoaderFromURL reads the loader version back from a Fabric server
// launcher URL (.../versions/loader/<game>/<loader>/<installer>/server/jar).
func fabricLoaderFromURL(raw string) string {
	_, rest, ok := strings.Cut(raw, "/versions/loader/")
	parts := strings.Split(rest, "/")
	if !ok || len(parts) < 2 {
		return ""
	}
	v, _ := url.PathUnescape(parts[1])
	return v
}

// resolveFabricLoader is resolveFabric with a given loader version when pin
// is set, as a mod pack names one.
func resolveFabricLoader(ctx context.Context, client *http.Client, want, pin string) (mcVersion, downloadURL string, err error) {
	var games, loaders, installers []fabricVersion
	for path, dst := range map[string]*[]fabricVersion{"/versions/game": &games, "/versions/loader": &loaders, "/versions/installer": &installers} {
		if err := getJSON(ctx, client, fabricMetaURL+path, dst); err != nil {
			return "", "", fmt.Errorf("fetch Fabric metadata: %w", err)
		}
	}
	firstStable := func(list []fabricVersion) string {
		for _, v := range list {
			if v.Stable {
				return v.Version
			}
		}
		return ""
	}
	game := firstStable(games)
	if want != "" && !strings.EqualFold(want, "latest") {
		game = ""
		for _, g := range games {
			if g.Version == want {
				game = g.Version
			}
		}
		if game == "" {
			return "", "", fmt.Errorf("Fabric does not support Minecraft %s", want)
		}
	}
	loader, installer := firstStable(loaders), firstStable(installers)
	if pin != "" {
		loader = ""
		for _, l := range loaders {
			if l.Version == pin {
				loader = pin
			}
		}
		if loader == "" {
			return "", "", fmt.Errorf("Fabric has no loader version %s", pin)
		}
	}
	if game == "" || loader == "" || installer == "" {
		return "", "", errors.New("Fabric metadata lists no stable game, loader or installer version")
	}
	for _, part := range []string{game, loader, installer} {
		if strings.ContainsAny(part, "/\\?#") {
			return "", "", errors.New("Fabric metadata returned an invalid version")
		}
	}
	return game, fabricMetaURL + "/versions/loader/" + url.PathEscape(game) + "/" + url.PathEscape(loader) + "/" + url.PathEscape(installer) + "/server/jar", nil
}
