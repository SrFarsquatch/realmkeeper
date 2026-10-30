package main

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Forge, NeoForge and Quilt are installed by running the loader's own
// installer, which downloads Mojang's server and the loader's libraries.
//
// Forge and NeoForge write a unix_args.txt that the server is started with
// (`java @libraries/.../unix_args.txt`). Forge is supported from Minecraft
// 1.17, the first release that uses it; older Forge needs Java 8, which the
// image does not ship. NeoForge starts at 1.20.2 (its 1.20.1 builds were
// published as Forge). Quilt's installer writes quilt-server-launch.jar,
// which loads server.jar from the same folder.

var (
	forgePromotionsURL = "https://files.minecraftforge.net/net/minecraftforge/forge/promotions_slim.json"
	forgeMavenURL      = "https://maven.minecraftforge.net/net/minecraftforge/forge"
	neoforgeMavenURL   = "https://maven.neoforged.net/releases/net/neoforged/neoforge"
	quiltInstallerURL  = "https://quiltmc.org/api/v1/download-latest-installer/java-universal"
	// javaCommand runs installers; tests point it at a stand-in.
	javaCommand = "java"
)

// quiltLauncherJar is Quilt's server launcher; it loads server.jar.
const quiltLauncherJar = "quilt-server-launch.jar"

// minecraftLoaderHosts are the only hosts a loader installer is fetched from,
// including after redirects.
var minecraftLoaderHosts = map[string]string{"forge": "minecraftforge.net", "neoforge": "neoforged.net", "quilt": "quiltmc.org"}

// loaderInstall is a resolved loader build.
type loaderInstall struct {
	MinecraftVersion string
	LoaderVersion    string
	InstallerURL     string
	// ArgsFile is the unix_args.txt the installer writes (Forge, NeoForge).
	ArgsFile string
}

var safeVersionPart = regexp.MustCompile(`^[0-9A-Za-z.+_-]{1,64}$`)

// compareDotted compares versions such as 21.1.228 part by part, numerically.
func compareDotted(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	return 0
}

func isLatest(v string) bool { return v == "" || strings.EqualFold(v, "latest") }

// resolveForge picks the recommended Forge build for a Minecraft version, or
// the latest build when none is recommended.
func resolveForge(ctx context.Context, client *http.Client, want string) (loaderInstall, error) {
	var promos struct {
		Promos map[string]string `json:"promos"`
	}
	if err := getJSON(ctx, client, forgePromotionsURL, &promos); err != nil {
		return loaderInstall{}, fmt.Errorf("fetch Forge versions: %w", err)
	}
	mc := want
	if isLatest(want) {
		mc = ""
		for key := range promos.Promos {
			if v, ok := strings.CutSuffix(key, "-latest"); ok && strings.HasPrefix(v, "1.") && (mc == "" || compareDotted(v, mc) > 0) {
				mc = v
			}
		}
		if mc == "" {
			return loaderInstall{}, errors.New("Forge lists no Minecraft versions")
		}
	}
	if strings.HasPrefix(mc, "1.") && compareDotted(mc, "1.17") < 0 {
		return loaderInstall{}, fmt.Errorf("Forge for Minecraft %s needs Java 8, which this image does not include; choose Minecraft 1.17 or newer", mc)
	}
	forge := promos.Promos[mc+"-recommended"]
	if forge == "" {
		forge = promos.Promos[mc+"-latest"]
	}
	if forge == "" {
		return loaderInstall{}, fmt.Errorf("Forge has no build for Minecraft %s", mc)
	}
	if !safeVersionPart.MatchString(mc) || !safeVersionPart.MatchString(forge) {
		return loaderInstall{}, errors.New("Forge returned an invalid version")
	}
	full := mc + "-" + forge
	return loaderInstall{
		MinecraftVersion: mc, LoaderVersion: forge,
		InstallerURL: forgeMavenURL + "/" + full + "/forge-" + full + "-installer.jar",
		ArgsFile:     "libraries/net/minecraftforge/forge/" + full + "/unix_args.txt",
	}, nil
}

// pinnedForge is a given Forge build, as a mod pack names it.
func pinnedForge(mc, forge string) (loaderInstall, error) {
	if !safeVersionPart.MatchString(mc) || !safeVersionPart.MatchString(forge) {
		return loaderInstall{}, errors.New("invalid Forge version")
	}
	if strings.HasPrefix(mc, "1.") && compareDotted(mc, "1.17") < 0 {
		return loaderInstall{}, fmt.Errorf("Forge for Minecraft %s needs Java 8, which this image does not include", mc)
	}
	full := mc + "-" + forge
	return loaderInstall{
		MinecraftVersion: mc, LoaderVersion: forge,
		InstallerURL: forgeMavenURL + "/" + full + "/forge-" + full + "-installer.jar",
		ArgsFile:     "libraries/net/minecraftforge/forge/" + full + "/unix_args.txt",
	}, nil
}

// pinnedNeoForge is a given NeoForge build.
func pinnedNeoForge(neo string) (loaderInstall, error) {
	if !safeVersionPart.MatchString(neo) || neoforgeMinecraftVersion(neo) == "" {
		return loaderInstall{}, errors.New("invalid NeoForge version")
	}
	return loaderInstall{
		MinecraftVersion: neoforgeMinecraftVersion(neo), LoaderVersion: neo,
		InstallerURL: neoforgeMavenURL + "/" + neo + "/neoforge-" + neo + "-installer.jar",
		ArgsFile:     "libraries/net/neoforged/neoforge/" + neo + "/unix_args.txt",
	}, nil
}

// NeoForge versions encode the Minecraft version: 21.1.x is Minecraft 1.21.1
// and 21.0.x is 1.21.
func neoforgePrefix(mc string) (string, error) {
	rest, ok := strings.CutPrefix(mc, "1.")
	parts := strings.Split(rest, ".")
	if !ok || len(parts) > 2 || parts[0] == "" {
		return "", fmt.Errorf("NeoForge does not support Minecraft %s", mc)
	}
	minor := "0"
	if len(parts) == 2 {
		minor = parts[1]
	}
	return parts[0] + "." + minor + ".", nil
}

func neoforgeMinecraftVersion(neo string) string {
	parts := strings.Split(neo, ".")
	if len(parts) < 2 {
		return ""
	}
	if parts[1] == "0" {
		return "1." + parts[0]
	}
	return "1." + parts[0] + "." + parts[1]
}

// resolveNeoForge picks the newest stable NeoForge build for a Minecraft
// version, or the newest stable build of all for "latest".
func resolveNeoForge(ctx context.Context, client *http.Client, want string) (loaderInstall, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, neoforgeMavenURL+"/maven-metadata.xml", nil)
	if err != nil {
		return loaderInstall{}, err
	}
	req.Header.Set("User-Agent", appUserAgent())
	resp, err := client.Do(req)
	if err != nil {
		return loaderInstall{}, fmt.Errorf("fetch NeoForge versions: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return loaderInstall{}, fmt.Errorf("NeoForge versions returned HTTP %d", resp.StatusCode)
	}
	var meta struct {
		Versions []string `xml:"versioning>versions>version"`
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&meta); err != nil {
		return loaderInstall{}, fmt.Errorf("parse NeoForge versions: %w", err)
	}
	prefix := ""
	if !isLatest(want) {
		if want == "1.20.1" {
			return loaderInstall{}, errors.New("NeoForge for Minecraft 1.20.1 was published as Forge; create a Forge server instead")
		}
		if prefix, err = neoforgePrefix(want); err != nil {
			return loaderInstall{}, err
		}
	}
	best := ""
	for _, v := range meta.Versions {
		// Betas carry a suffix such as 21.4.0-beta.
		if strings.Contains(v, "-") || !strings.HasPrefix(v, prefix) || !safeVersionPart.MatchString(v) {
			continue
		}
		if best == "" || compareDotted(v, best) > 0 {
			best = v
		}
	}
	if best == "" {
		if prefix == "" {
			return loaderInstall{}, errors.New("NeoForge lists no stable builds")
		}
		return loaderInstall{}, fmt.Errorf("NeoForge has no stable build for Minecraft %s", want)
	}
	return loaderInstall{
		MinecraftVersion: neoforgeMinecraftVersion(best), LoaderVersion: best,
		InstallerURL: neoforgeMavenURL + "/" + best + "/neoforge-" + best + "-installer.jar",
		ArgsFile:     "libraries/net/neoforged/neoforge/" + best + "/unix_args.txt",
	}, nil
}

// minecraftLatestRelease is the newest release in Mojang's version manifest.
func minecraftLatestRelease(ctx context.Context, client *http.Client) (string, error) {
	var manifest minecraftManifest
	if err := getJSON(ctx, client, minecraftVersionManifestURL, &manifest); err != nil {
		return "", fmt.Errorf("fetch the Minecraft version manifest: %w", err)
	}
	if !safeVersionPart.MatchString(manifest.Latest.Release) {
		return "", errors.New("the Minecraft version manifest has no latest release")
	}
	return manifest.Latest.Release, nil
}

// loaderClient refuses redirects that leave the loader's own domain.
func loaderClient(base *http.Client, domain string) *http.Client {
	c := *base
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if !isHTTPSHost(req.URL.String(), domain) {
			return fmt.Errorf("refusing a redirect to %s", req.URL.Host)
		}
		return nil
	}
	return &c
}

// publishedSHA256 reads the checksum Maven repositories publish next to a
// file. It returns "" when there is none; the download is then trusted to
// HTTPS alone.
func publishedSHA256(ctx context.Context, client *http.Client, fileURL string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL+".sha256", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", appUserAgent())
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	fields := strings.Fields(string(b))
	if len(fields) == 0 || len(fields[0]) != 64 || strings.Trim(strings.ToLower(fields[0]), "0123456789abcdef") != "" {
		return ""
	}
	return fields[0]
}

// runJavaInstaller runs a loader installer with its output in the console.
func (m *ServerManager) runJavaInstaller(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, javaCommand, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+pathsFor(m.rootDir).Home())
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the installer: %w", err)
	}
	m.scanPipe(stdout, "installer")
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("the installer failed: %w", err)
	}
	return nil
}

// installMinecraftLoader installs Forge, NeoForge or Quilt: the given loader
// build when pin is set (a mod pack names one), else the recommended or
// newest stable build.
func (m *ServerManager) installMinecraftLoader(ctx context.Context, mojang *http.Client, version, variant, pin string) error {
	client := loaderClient(mojang, minecraftLoaderHosts[variant])
	var li loaderInstall
	var err error
	switch {
	case variant == "forge" && pin != "":
		li, err = pinnedForge(version, pin)
	case variant == "forge":
		li, err = resolveForge(ctx, client, version)
	case variant == "neoforge" && pin != "":
		li, err = pinnedNeoForge(pin)
	case variant == "neoforge":
		li, err = resolveNeoForge(ctx, client, version)
	case variant == "quilt":
		if pin != "" && !safeVersionPart.MatchString(pin) {
			return errors.New("invalid Quilt loader version")
		}
		li = loaderInstall{MinecraftVersion: version, LoaderVersion: pin, InstallerURL: quiltInstallerURL}
	}
	if err != nil {
		return err
	}
	if isLatest(li.MinecraftVersion) {
		// Quilt's installer needs a release name.
		if li.MinecraftVersion, err = minecraftLatestRelease(ctx, mojang); err != nil {
			return err
		}
	}
	// The loaders run Mojang's server code, so Mojang's Java requirement
	// applies. If it cannot be read, the check is skipped.
	_, _, javaMajor, err := resolveMinecraftServerURL(ctx, mojang, li.MinecraftVersion)
	if err != nil {
		m.appendLog("installer", "Could not read the Java requirement for Minecraft "+li.MinecraftVersion+": "+err.Error())
	}
	if javaMajor > 0 {
		if have, err := detectJavaMajor(); err == nil && have < javaMajor {
			return fmt.Errorf("Minecraft %s needs Java %d or newer, but this container has Java %d", li.MinecraftVersion, javaMajor, have)
		}
	}

	p := pathsFor(m.rootDir)
	gameDir := p.Game()
	work := filepath.Join(p.Tmp(), variant+"-install")
	_ = os.RemoveAll(work)
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(work)
	if err := os.MkdirAll(gameDir, 0o755); err != nil {
		return err
	}
	installer := filepath.Join(work, "installer.jar")
	name := minecraftVariantNames[variant]
	m.appendLog("installer", "Downloading the "+name+" installer for Minecraft "+li.MinecraftVersion+"…")
	sha := publishedSHA256(ctx, client, li.InstallerURL)
	if err := downloadVerified(ctx, client, li.InstallerURL, installer, 128<<20, sha); err != nil {
		return fmt.Errorf("download the %s installer: %w", name, err)
	}
	if err := validateMinecraftJar(installer); err != nil {
		return err
	}
	m.appendLog("installer", "Running the "+name+" installer. It downloads Minecraft and the loader's libraries, which can take a few minutes…")

	if variant == "quilt" {
		args := []string{"-jar", installer, "install", "server", li.MinecraftVersion}
		if li.LoaderVersion != "" {
			args = append(args, li.LoaderVersion)
		}
		if err := m.runJavaInstaller(ctx, work, append(args, "--download-server")...); err != nil {
			return err
		}
		out := filepath.Join(work, "server")
		if !fileExists(filepath.Join(out, quiltLauncherJar)) || !fileExists(filepath.Join(out, "server.jar")) {
			return errors.New("the Quilt installer did not produce " + quiltLauncherJar + " and server.jar")
		}
		entries, err := os.ReadDir(out)
		if err != nil {
			return err
		}
		for _, e := range entries {
			dst := filepath.Join(gameDir, e.Name())
			_ = os.RemoveAll(dst)
			if err := os.Rename(filepath.Join(out, e.Name()), dst); err != nil {
				return err
			}
		}
	} else {
		if err := m.runJavaInstaller(ctx, gameDir, "-jar", installer, "--installServer"); err != nil {
			return err
		}
		if !fileExists(filepath.Join(gameDir, filepath.FromSlash(li.ArgsFile))) {
			return errors.New("the " + name + " installer did not produce " + li.ArgsFile)
		}
		// The installer's own start scripts and log; the app starts the server.
		for _, leftover := range []string{"run.bat", "run.sh", "installer.jar.log"} {
			_ = os.Remove(filepath.Join(gameDir, leftover))
		}
	}
	if err := os.WriteFile(filepath.Join(gameDir, "eula.txt"), []byte("# Accepted by the server owner in Realmkeeper. See https://aka.ms/MinecraftEULA.\neula=true\n"), 0o600); err != nil {
		return err
	}
	if err := writeMinecraftInstallInfo(gameDir, minecraftInstallInfo{
		Version: li.MinecraftVersion, JavaMajor: javaMajor, Variant: variant, LoaderVersion: li.LoaderVersion, ArgsFile: li.ArgsFile,
	}); err != nil {
		return err
	}
	label := name
	if li.LoaderVersion != "" {
		label += " " + li.LoaderVersion
	}
	m.appendLog("installer", "Installed "+label+" for Minecraft "+li.MinecraftVersion)
	return nil
}
