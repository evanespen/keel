package docker

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"time"

	manifestlist "github.com/distribution/distribution/v3/manifest/manifestlist"
	manifestv2 "github.com/distribution/distribution/v3/manifest/schema2"
	oci "github.com/opencontainers/image-spec/specs-go/v1"
)

// ManifestCreated returns the build time of the image behind a tag, read from
// the "created" field of the image config blob.
//
// For an image index, the configs of all platform manifests are read and the
// most recent creation time is returned, so that an image is only considered
// old once every platform variant is old. Attestation manifests (platform
// "unknown") are ignored.
func (r *Registry) ManifestCreated(repository, reference string) (time.Time, error) {
	return r.manifestCreated(repository, reference, true)
}

func (r *Registry) manifestCreated(repository, reference string, allowIndex bool) (time.Time, error) {
	manifestURL := r.url("/v2/%s/manifests/%s", repository, reference)
	resp, err := r.request(http.MethodGet, manifestURL)
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return time.Time{}, fmt.Errorf("manifest request returned %s", resp.Status)
	}

	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid manifest content type: %w", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return time.Time{}, err
	}

	switch mediaType {
	case manifestlist.MediaTypeManifestList, oci.MediaTypeImageIndex:
		if !allowIndex {
			return time.Time{}, fmt.Errorf("nested image index is not supported")
		}
		var index oci.Index
		if err := json.Unmarshal(body, &index); err != nil {
			return time.Time{}, fmt.Errorf("decode image index: %w", err)
		}
		var newest time.Time
		found := false
		for _, descriptor := range index.Manifests {
			if descriptor.Platform == nil || descriptor.Platform.OS == "" || descriptor.Platform.OS == "unknown" ||
				descriptor.Platform.Architecture == "" || descriptor.Platform.Architecture == "unknown" {
				continue
			}
			created, err := r.manifestCreated(repository, descriptor.Digest.String(), false)
			if err != nil {
				return time.Time{}, err
			}
			found = true
			if created.After(newest) {
				newest = created
			}
		}
		if !found {
			return time.Time{}, fmt.Errorf("image index contains no platform manifest")
		}
		return newest, nil

	case manifestv2.MediaTypeManifest, oci.MediaTypeImageManifest:
		var manifest oci.Manifest
		if err := json.Unmarshal(body, &manifest); err != nil {
			return time.Time{}, fmt.Errorf("decode image manifest: %w", err)
		}
		if manifest.Config.Digest == "" {
			return time.Time{}, fmt.Errorf("image manifest has no config digest")
		}
		return r.configCreated(repository, manifest.Config.Digest.String())
	default:
		return time.Time{}, fmt.Errorf("unsupported manifest content type %q", mediaType)
	}
}

func (r *Registry) configCreated(repository, configDigest string) (time.Time, error) {
	configURL := r.url("/v2/%s/blobs/%s", repository, configDigest)
	resp, err := r.request(http.MethodGet, configURL)
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return time.Time{}, fmt.Errorf("image config request returned %s", resp.Status)
	}

	var config oci.Image
	if err := json.NewDecoder(resp.Body).Decode(&config); err != nil {
		return time.Time{}, fmt.Errorf("decode image config: %w", err)
	}
	if config.Created == nil || config.Created.IsZero() {
		return time.Time{}, fmt.Errorf("image config contains no creation time")
	}
	return *config.Created, nil
}
