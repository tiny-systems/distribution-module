package containerregistry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"cuelabs.dev/go/oci/ociregistry"
	"cuelabs.dev/go/oci/ociregistry/ocimem"
)

// diskRegistry implements ociregistry.Interface backed by the filesystem.
type diskRegistry struct {
	*ociregistry.Funcs // embed for private() method
	root               string
	mu                 sync.RWMutex
}

type manifestMeta struct {
	MediaType string `json:"mediaType"`
	Size      int64  `json:"size"`
	Digest    string `json:"digest"`
	Data      []byte `json:"data"`
}

// NewDiskRegistry creates a filesystem-backed OCI registry.
func NewDiskRegistry(root string) ociregistry.Interface {
	d := &diskRegistry{root: root}
	d.Funcs = &ociregistry.Funcs{
		GetBlob_:              d.getBlob,
		GetBlobRange_:         d.getBlobRange,
		GetManifest_:          d.getManifest,
		GetTag_:               d.getTag,
		ResolveBlob_:          d.resolveBlob,
		ResolveManifest_:      d.resolveManifest,
		ResolveTag_:           d.resolveTag,
		PushBlob_:             d.pushBlob,
		PushBlobChunked_:      d.pushBlobChunked,
		PushBlobChunkedResume_: d.pushBlobChunkedResume,
		MountBlob_:            d.mountBlob,
		PushManifest_:         d.pushManifest,
		DeleteBlob_:           d.deleteBlob,
		DeleteManifest_:       d.deleteManifest,
		DeleteTag_:            d.deleteTag,
		Repositories_:         d.repositories,
		Tags_:                 d.tags,
		Referrers_:            d.referrers,
	}
	return d
}

func (d *diskRegistry) blobPath(digest ociregistry.Digest) string {
	algo, hex := splitDigest(digest)
	return filepath.Join(d.root, "blobs", algo, hex)
}

func (d *diskRegistry) manifestPath(repo string, digest ociregistry.Digest) string {
	algo, hex := splitDigest(digest)
	return filepath.Join(d.root, "manifests", repo, algo, hex)
}

func (d *diskRegistry) tagPath(repo, tag string) string {
	return filepath.Join(d.root, "tags", repo, tag)
}

func splitDigest(digest ociregistry.Digest) (string, string) {
	s := string(digest)
	if i := strings.Index(s, ":"); i >= 0 {
		return s[:i], s[i+1:]
	}
	return "sha256", s
}

// --- Reader ---

func (d *diskRegistry) getBlob(_ context.Context, repo string, digest ociregistry.Digest) (ociregistry.BlobReader, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	data, err := os.ReadFile(d.blobPath(digest))
	if err != nil {
		return nil, ociregistry.ErrBlobUnknown
	}
	return ocimem.NewBytesReader(data, ociregistry.Descriptor{
		Digest:    digest,
		Size:      int64(len(data)),
		MediaType: "application/octet-stream",
	}), nil
}

func (d *diskRegistry) getBlobRange(_ context.Context, repo string, digest ociregistry.Digest, offset0, offset1 int64) (ociregistry.BlobReader, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	data, err := os.ReadFile(d.blobPath(digest))
	if err != nil {
		return nil, ociregistry.ErrBlobUnknown
	}
	end := int64(len(data))
	if offset1 >= 0 && offset1 < end {
		end = offset1
	}
	if offset0 >= int64(len(data)) {
		offset0 = int64(len(data))
	}
	slice := data[offset0:end]
	return ocimem.NewBytesReader(slice, ociregistry.Descriptor{
		Digest: digest,
		Size:   int64(len(slice)),
	}), nil
}

func (d *diskRegistry) getManifest(_ context.Context, repo string, digest ociregistry.Digest) (ociregistry.BlobReader, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	meta, err := d.readManifest(repo, digest)
	if err != nil {
		return nil, ociregistry.ErrManifestUnknown
	}
	return ocimem.NewBytesReader(meta.Data, ociregistry.Descriptor{
		MediaType: meta.MediaType,
		Digest:    ociregistry.Digest(meta.Digest),
		Size:      meta.Size,
	}), nil
}

func (d *diskRegistry) getTag(_ context.Context, repo string, tagName string) (ociregistry.BlobReader, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	digest, err := d.readTag(repo, tagName)
	if err != nil {
		return nil, ociregistry.ErrManifestUnknown
	}
	meta, err := d.readManifest(repo, digest)
	if err != nil {
		return nil, ociregistry.ErrManifestUnknown
	}
	return ocimem.NewBytesReader(meta.Data, ociregistry.Descriptor{
		MediaType: meta.MediaType,
		Digest:    ociregistry.Digest(meta.Digest),
		Size:      meta.Size,
	}), nil
}

func (d *diskRegistry) resolveBlob(_ context.Context, repo string, digest ociregistry.Digest) (ociregistry.Descriptor, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	info, err := os.Stat(d.blobPath(digest))
	if err != nil {
		return ociregistry.Descriptor{}, ociregistry.ErrBlobUnknown
	}
	return ociregistry.Descriptor{
		Digest: digest,
		Size:   info.Size(),
	}, nil
}

func (d *diskRegistry) resolveManifest(_ context.Context, repo string, digest ociregistry.Digest) (ociregistry.Descriptor, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	meta, err := d.readManifest(repo, digest)
	if err != nil {
		return ociregistry.Descriptor{}, ociregistry.ErrManifestUnknown
	}
	return ociregistry.Descriptor{
		MediaType: meta.MediaType,
		Digest:    ociregistry.Digest(meta.Digest),
		Size:      meta.Size,
	}, nil
}

func (d *diskRegistry) resolveTag(_ context.Context, repo string, tagName string) (ociregistry.Descriptor, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	digest, err := d.readTag(repo, tagName)
	if err != nil {
		return ociregistry.Descriptor{}, ociregistry.ErrManifestUnknown
	}
	meta, err := d.readManifest(repo, digest)
	if err != nil {
		return ociregistry.Descriptor{}, ociregistry.ErrManifestUnknown
	}
	return ociregistry.Descriptor{
		MediaType: meta.MediaType,
		Digest:    ociregistry.Digest(meta.Digest),
		Size:      meta.Size,
	}, nil
}

// --- Writer ---

func (d *diskRegistry) pushBlob(_ context.Context, repo string, desc ociregistry.Descriptor, r io.Reader) (ociregistry.Descriptor, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return ociregistry.Descriptor{}, err
	}

	digest := desc.Digest
	if digest == "" {
		h := sha256.Sum256(data)
		digest = ociregistry.Digest(fmt.Sprintf("sha256:%x", h))
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	path := d.blobPath(digest)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return ociregistry.Descriptor{}, err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return ociregistry.Descriptor{}, err
	}

	return ociregistry.Descriptor{
		Digest:    digest,
		Size:      int64(len(data)),
		MediaType: desc.MediaType,
	}, nil
}

func (d *diskRegistry) pushBlobChunked(_ context.Context, repo string, chunkSize int) (ociregistry.BlobWriter, error) {
	return &diskBlobWriter{reg: d, repo: repo, buf: &bytes.Buffer{}}, nil
}

func (d *diskRegistry) pushBlobChunkedResume(_ context.Context, repo, id string, offset int64, chunkSize int) (ociregistry.BlobWriter, error) {
	return &diskBlobWriter{reg: d, repo: repo, buf: &bytes.Buffer{}}, nil
}

func (d *diskRegistry) mountBlob(ctx context.Context, fromRepo, toRepo string, digest ociregistry.Digest) (ociregistry.Descriptor, error) {
	return d.resolveBlob(ctx, fromRepo, digest)
}

func (d *diskRegistry) pushManifest(_ context.Context, repo string, tag string, contents []byte, mediaType string) (ociregistry.Descriptor, error) {
	h := sha256.Sum256(contents)
	digest := ociregistry.Digest(fmt.Sprintf("sha256:%x", h))

	meta := manifestMeta{
		MediaType: mediaType,
		Size:      int64(len(contents)),
		Digest:    string(digest),
		Data:      contents,
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	path := d.manifestPath(repo, digest)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return ociregistry.Descriptor{}, err
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return ociregistry.Descriptor{}, err
	}
	if err := os.WriteFile(path, metaJSON, 0o644); err != nil {
		return ociregistry.Descriptor{}, err
	}

	if tag != "" {
		tagFile := d.tagPath(repo, tag)
		if err := os.MkdirAll(filepath.Dir(tagFile), 0o755); err != nil {
			return ociregistry.Descriptor{}, err
		}
		if err := os.WriteFile(tagFile, []byte(string(digest)), 0o644); err != nil {
			return ociregistry.Descriptor{}, err
		}
	}

	return ociregistry.Descriptor{
		MediaType: mediaType,
		Digest:    digest,
		Size:      int64(len(contents)),
	}, nil
}

// --- Deleter ---

func (d *diskRegistry) deleteBlob(_ context.Context, repo string, digest ociregistry.Digest) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return os.Remove(d.blobPath(digest))
}

func (d *diskRegistry) deleteManifest(_ context.Context, repo string, digest ociregistry.Digest) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return os.Remove(d.manifestPath(repo, digest))
}

func (d *diskRegistry) deleteTag(_ context.Context, repo string, name string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return os.Remove(d.tagPath(repo, name))
}

// --- Lister ---

func (d *diskRegistry) repositories(_ context.Context, startAfter string) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		d.mu.RLock()
		defer d.mu.RUnlock()

		seen := make(map[string]bool)
		for _, dir := range []string{"manifests", "tags"} {
			for _, name := range listEntries(filepath.Join(d.root, dir)) {
				seen[name] = true
			}
		}
		repos := make([]string, 0, len(seen))
		for r := range seen {
			repos = append(repos, r)
		}
		sort.Strings(repos)

		for _, repo := range repos {
			if startAfter != "" && repo <= startAfter {
				continue
			}
			if !yield(repo, nil) {
				return
			}
		}
	}
}

func (d *diskRegistry) tags(_ context.Context, repo string, startAfter string) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		d.mu.RLock()
		defer d.mu.RUnlock()

		tags := listEntries(filepath.Join(d.root, "tags", repo))
		sort.Strings(tags)

		for _, tag := range tags {
			if startAfter != "" && tag <= startAfter {
				continue
			}
			if !yield(tag, nil) {
				return
			}
		}
	}
}

func (d *diskRegistry) referrers(_ context.Context, repo string, digest ociregistry.Digest, artifactType string) iter.Seq2[ociregistry.Descriptor, error] {
	return func(yield func(ociregistry.Descriptor, error) bool) {}
}

// --- Helpers ---

func (d *diskRegistry) readManifest(repo string, digest ociregistry.Digest) (*manifestMeta, error) {
	data, err := os.ReadFile(d.manifestPath(repo, digest))
	if err != nil {
		return nil, err
	}
	var meta manifestMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func (d *diskRegistry) readTag(repo, tag string) (ociregistry.Digest, error) {
	data, err := os.ReadFile(d.tagPath(repo, tag))
	if err != nil {
		return "", err
	}
	return ociregistry.Digest(strings.TrimSpace(string(data))), nil
}

func listEntries(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// diskBlobWriter implements ociregistry.BlobWriter for chunked uploads
type diskBlobWriter struct {
	reg  *diskRegistry
	repo string
	buf  *bytes.Buffer
}

func (w *diskBlobWriter) Write(p []byte) (int, error)  { return w.buf.Write(p) }
func (w *diskBlobWriter) Close() error                  { return nil }
func (w *diskBlobWriter) Size() int64                   { return int64(w.buf.Len()) }
func (w *diskBlobWriter) ChunkSize() int                { return 0 }
func (w *diskBlobWriter) ID() string                    { return "upload" }
func (w *diskBlobWriter) Cancel() error                 { w.buf.Reset(); return nil }

func (w *diskBlobWriter) Commit(digest ociregistry.Digest) (ociregistry.Descriptor, error) {
	return w.reg.pushBlob(context.Background(), w.repo, ociregistry.Descriptor{Digest: digest}, bytes.NewReader(w.buf.Bytes()))
}
