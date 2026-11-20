package docker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	manifestlist "github.com/distribution/distribution/v3/manifest/manifestlist"
	manifestv2 "github.com/distribution/distribution/v3/manifest/schema2"
	"github.com/opencontainers/go-digest"
	oci "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestManifestCreated(t *testing.T) {
	const (
		amdManifest  = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
		armManifest  = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
		attManifest  = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
		amdConfig    = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		armConfig    = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		noDateConfig = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	)
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)

	manifest := func(config string) []byte {
		b, _ := json.Marshal(oci.Manifest{
			MediaType: manifestv2.MediaTypeManifest,
			Config:    oci.Descriptor{Digest: digest.Digest(config)},
		})
		return b
	}
	config := func(created *time.Time) []byte {
		b, _ := json.Marshal(oci.Image{Created: created, Platform: oci.Platform{OS: "linux", Architecture: "amd64"}})
		return b
	}
	index, _ := json.Marshal(oci.Index{
		MediaType: oci.MediaTypeImageIndex,
		Manifests: []oci.Descriptor{
			{Digest: amdManifest, Platform: &oci.Platform{OS: "linux", Architecture: "amd64"}},
			{Digest: armManifest, Platform: &oci.Platform{OS: "linux", Architecture: "arm64"}},
			// attestation manifests must not be fetched
			{Digest: attManifest, Platform: &oci.Platform{OS: "unknown", Architecture: "unknown"}},
		},
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/example/image/manifests/single":
			w.Header().Set("Content-Type", manifestv2.MediaTypeManifest)
			_, _ = w.Write(manifest(amdConfig))
		case "/v2/example/image/manifests/multi":
			w.Header().Set("Content-Type", manifestlist.MediaTypeManifestList)
			_, _ = w.Write(index)
		case "/v2/example/image/manifests/nodate":
			w.Header().Set("Content-Type", manifestv2.MediaTypeManifest)
			_, _ = w.Write(manifest(noDateConfig))
		case "/v2/example/image/manifests/" + amdManifest:
			w.Header().Set("Content-Type", manifestv2.MediaTypeManifest)
			_, _ = w.Write(manifest(amdConfig))
		case "/v2/example/image/manifests/" + armManifest:
			w.Header().Set("Content-Type", manifestv2.MediaTypeManifest)
			_, _ = w.Write(manifest(armConfig))
		case "/v2/example/image/blobs/" + amdConfig:
			_, _ = w.Write(config(&older))
		case "/v2/example/image/blobs/" + armConfig:
			_, _ = w.Write(config(&newer))
		case "/v2/example/image/blobs/" + noDateConfig:
			_, _ = w.Write(config(nil))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	reg := New(server.URL, "", "")

	got, err := reg.ManifestCreated("example/image", "single")
	if err != nil || !got.Equal(older) {
		t.Errorf("single manifest: want %s, got %s (err=%v)", older, got, err)
	}

	got, err = reg.ManifestCreated("example/image", "multi")
	if err != nil || !got.Equal(newer) {
		t.Errorf("image index must return the newest platform build: want %s, got %s (err=%v)", newer, got, err)
	}

	if _, err = reg.ManifestCreated("example/image", "nodate"); err == nil {
		t.Error("expected an error when the image config has no creation time")
	}
	if _, err = reg.ManifestCreated("example/image", "missing"); err == nil {
		t.Error("expected an error for an unknown tag")
	}
}
