package main

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// fakeRegistry serves a two-platform image whose layer for this machine is
// "layer for <arch>". corrupt makes the layer blob differ from its digest.
func fakeRegistry(t *testing.T, corrupt bool) {
	t.Helper()
	config := []byte(`{"architecture":"` + runtime.GOARCH + `"}`)
	layer := []byte("layer for " + runtime.GOARCH)
	served := layer
	if corrupt {
		served = []byte("tampered for " + runtime.GOARCH)[:len(layer)]
	}
	image, _ := json.Marshal(map[string]any{
		"mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config":    map[string]any{"digest": digestOf(config), "size": len(config)},
		"layers":    []any{map[string]any{"digest": digestOf(layer), "size": len(layer), "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip"}},
	})
	index, _ := json.Marshal(map[string]any{
		"mediaType": "application/vnd.oci.image.index.v1+json",
		"manifests": []any{
			map[string]any{"digest": digestOf([]byte("other")), "platform": map[string]string{"os": "linux", "architecture": "s390x"}},
			map[string]any{"digest": digestOf(image), "platform": map[string]string{"os": "linux", "architecture": runtime.GOARCH, "variant": "v7"}},
		},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"token":"anon"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer anon" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v2/srfarsquatch/realmkeeper/manifests/latest":
			_, _ = w.Write(index)
		case "/v2/srfarsquatch/realmkeeper/manifests/" + digestOf(image):
			_, _ = w.Write(image)
		case "/v2/srfarsquatch/realmkeeper/blobs/" + digestOf(config):
			_, _ = w.Write(config)
		case "/v2/srfarsquatch/realmkeeper/blobs/" + digestOf(layer):
			_, _ = w.Write(served)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := registryBase
	registryBase = func(string) string { return srv.URL }
	t.Cleanup(func() { registryBase = old })
}

// fakeDockerLoad accepts /images/load and records the archive's files.
func fakeDockerLoad(t *testing.T, files map[string]string) *dockerAPI {
	return fakeDockerAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images/load" {
			http.NotFound(w, r)
			return
		}
		tr := tar.NewReader(r.Body)
		for {
			h, err := tr.Next()
			if err != nil {
				if err != io.EOF {
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"message":"` + err.Error() + `"}`))
					return
				}
				break
			}
			b, _ := io.ReadAll(tr)
			files[h.Name] = string(b)
		}
		_, _ = w.Write([]byte(`{"stream":"Loaded image: ghcr.io/srfarsquatch/realmkeeper:latest\n"}`))
	})
}

func TestPullViaRegistryLoadsThisMachinesImage(t *testing.T) {
	fakeRegistry(t, false)
	files := map[string]string{}
	d := fakeDockerLoad(t, files)
	if err := pullViaRegistry(context.Background(), d, http.DefaultClient, "ghcr.io/srfarsquatch/realmkeeper:latest"); err != nil {
		t.Fatal(err)
	}
	var index []struct {
		Config   string
		RepoTags []string
		Layers   []string
	}
	if err := json.Unmarshal([]byte(files["manifest.json"]), &index); err != nil || len(index) != 1 {
		t.Fatalf("manifest.json = %q", files["manifest.json"])
	}
	m := index[0]
	if strings.Join(m.RepoTags, ",") != "ghcr.io/srfarsquatch/realmkeeper:latest" || len(m.Layers) != 1 {
		t.Errorf("manifest = %+v", m)
	}
	if files[m.Layers[0]] != "layer for "+runtime.GOARCH || !strings.Contains(files[m.Config], runtime.GOARCH) {
		t.Errorf("archive files = %v", files)
	}
}

func TestPullViaRegistryRefusesATamperedLayer(t *testing.T) {
	fakeRegistry(t, true)
	files := map[string]string{}
	d := fakeDockerLoad(t, files)
	err := pullViaRegistry(context.Background(), d, http.DefaultClient, "ghcr.io/srfarsquatch/realmkeeper:latest")
	if err == nil {
		t.Fatal("a layer that does not match its digest was loaded")
	}
	if _, ok := files["manifest.json"]; ok {
		t.Error("the archive was completed despite the bad layer")
	}
}
