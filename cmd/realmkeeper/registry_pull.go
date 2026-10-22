package main

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"
)

// pullViaRegistry downloads an image from its registry itself and loads it
// into Docker with POST /images/load. The updater uses it when Docker's own
// pull fails: the Docker engine resolves names with the host's DNS, which can
// be broken (for example a misbehaving Tailscale MagicDNS resolver) while
// this container, with its own DNS servers, can still reach the registry.
//
// The image is streamed into a docker-save archive (manifest.json, the
// config, and each layer as stored in the registry; Docker decompresses
// layers when loading), so nothing is written to disk here. Every blob is
// checked against its digest.
func pullViaRegistry(ctx context.Context, d *dockerAPI, client *http.Client, image string) error {
	ref, tag, ok := strings.Cut(image, ":")
	host, repo, ok2 := strings.Cut(ref, "/")
	if !ok || !ok2 || !strings.Contains(host, ".") {
		return fmt.Errorf("unsupported image reference %q", image)
	}
	r := &registryClient{client: client, base: registryBase(host), repo: repo}
	if err := r.authenticate(ctx); err != nil {
		return err
	}
	manifest, err := r.platformManifest(ctx, tag)
	if err != nil {
		return err
	}
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(r.writeArchive(ctx, pw, image, manifest)) }()
	resp, err := d.request(ctx, http.MethodPost, "/images/load?quiet=1", pr)
	if err != nil {
		pr.CloseWithError(err)
		return fmt.Errorf("load the image into Docker: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Docker refused the image: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if msg := pullStreamError(body); msg != "" {
		return fmt.Errorf("Docker could not load the image: %s", msg)
	}
	return nil
}

// registryBase is the registry's API root; tests point it at a fake.
var registryBase = func(host string) string { return "https://" + host }

type registryClient struct {
	client *http.Client
	base   string
	repo   string
	token  string
}

type registryDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	Platform  *struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		Variant      string `json:"variant"`
	} `json:"platform,omitempty"`
}

type registryManifest struct {
	MediaType string               `json:"mediaType"`
	Config    registryDescriptor   `json:"config"`
	Layers    []registryDescriptor `json:"layers"`
	Manifests []registryDescriptor `json:"manifests"`
}

// authenticate gets an anonymous pull token, as Docker does for public
// images (GHCR requires one even for public packages).
func (r *registryClient) authenticate(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, r.base+"/token?scope="+url.QueryEscape("repository:"+r.repo+":pull"), nil)
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("reach the registry: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("registry token request returned HTTP %d", resp.StatusCode)
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil {
		return fmt.Errorf("read the registry token: %w", err)
	}
	r.token = tok.Token
	if r.token == "" {
		r.token = tok.AccessToken
	}
	return nil
}

func (r *registryClient) get(ctx context.Context, path, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.base+"/v2/"+r.repo+path, nil)
	if err != nil {
		return nil, err
	}
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("registry returned HTTP %d for %s", resp.StatusCode, path)
	}
	return resp, nil
}

const manifestAccept = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

func (r *registryClient) manifest(ctx context.Context, ref string) (registryManifest, error) {
	var m registryManifest
	resp, err := r.get(ctx, "/manifests/"+ref, manifestAccept)
	if err != nil {
		return m, err
	}
	defer resp.Body.Close()
	err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&m)
	return m, err
}

// platformManifest resolves a tag to the image manifest for this machine,
// following a multi-architecture index.
func (r *registryClient) platformManifest(ctx context.Context, tag string) (registryManifest, error) {
	m, err := r.manifest(ctx, tag)
	if err != nil || len(m.Manifests) == 0 {
		return m, err
	}
	for _, desc := range m.Manifests {
		if p := desc.Platform; p != nil && p.OS == "linux" && p.Architecture == runtime.GOARCH && (runtime.GOARCH != "arm" || p.Variant == "v7") {
			return r.manifest(ctx, desc.Digest)
		}
	}
	return m, fmt.Errorf("the image has no build for linux/%s", runtime.GOARCH)
}

// writeArchive writes a docker-save archive of the image to w.
func (r *registryClient) writeArchive(ctx context.Context, w io.Writer, image string, m registryManifest) error {
	if !validDigest(m.Config.Digest) || len(m.Layers) == 0 {
		return errors.New("the image manifest is incomplete")
	}
	tw := tar.NewWriter(w)
	configName := strings.TrimPrefix(m.Config.Digest, "sha256:") + ".json"
	if err := r.copyBlob(ctx, tw, configName, m.Config); err != nil {
		return err
	}
	var layers []string
	for i, layer := range m.Layers {
		if !validDigest(layer.Digest) {
			return errors.New("the image manifest has an invalid layer digest")
		}
		name := strings.TrimPrefix(layer.Digest, "sha256:") + "/layer.tar"
		helperLog(fmt.Sprintf("Downloading layer %d of %d (%d MB)", i+1, len(m.Layers), layer.Size>>20))
		if err := r.copyBlob(ctx, tw, name, layer); err != nil {
			return err
		}
		layers = append(layers, name)
	}
	index, _ := json.Marshal([]map[string]any{{"Config": configName, "RepoTags": []string{image}, "Layers": layers}})
	if err := tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o644, Size: int64(len(index)), ModTime: time.Now()}); err != nil {
		return err
	}
	if _, err := tw.Write(index); err != nil {
		return err
	}
	return tw.Close()
}

func validDigest(d string) bool {
	h, ok := strings.CutPrefix(d, "sha256:")
	_, err := hex.DecodeString(h)
	return ok && len(h) == 64 && err == nil
}

// copyBlob streams one blob into the archive, checking size and digest. A
// mismatch fails the whole load, so a bad download is never installed.
func (r *registryClient) copyBlob(ctx context.Context, tw *tar.Writer, name string, desc registryDescriptor) error {
	resp, err := r.get(ctx, "/blobs/"+desc.Digest, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: desc.Size, ModTime: time.Now()}); err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tw, h), io.LimitReader(resp.Body, desc.Size))
	if err != nil {
		return fmt.Errorf("download %s: %w", desc.Digest, err)
	}
	if n != desc.Size || "sha256:"+hex.EncodeToString(h.Sum(nil)) != desc.Digest {
		return fmt.Errorf("download of %s does not match its digest", desc.Digest)
	}
	return nil
}

// pullStreamError returns the first error in a Docker JSON message stream.
func pullStreamError(b []byte) string {
	for _, line := range strings.Split(string(b), "\n") {
		var item struct {
			Error string `json:"error"`
		}
		if json.Unmarshal([]byte(line), &item) == nil && item.Error != "" {
			return item.Error
		}
	}
	return ""
}
