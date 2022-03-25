package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/httplog"
	"github.com/ipfs/go-cid"
	files "github.com/ipfs/go-ipfs-files"
	iface "github.com/ipfs/interface-go-ipfs-core"
	"github.com/ipfs/interface-go-ipfs-core/path"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	ipfsSchemePrefix = "ipfs"
)

// Reader defines data implementations that can satisfy all of a registry's read operations
type Reader interface {
	ReadManifest(ctx context.Context, name string, reference string) (io.ReadSeeker, string, error)

	ReadBlob(ctx context.Context, name string, d digest.Digest) (io.ReadSeeker, string, error)
}

// IpfsRegistry is a registry backed by ipfs
type IpfsRegistry struct {
	Router *chi.Mux

	reader Reader
	mapper CidMapper
}

type IpfsRegistryOpts struct {
	MapIpnsCid string
}

func NewIpfsRegistry(client iface.CoreAPI, opts *IpfsRegistryOpts) *IpfsRegistry {
	r := chi.NewRouter()
	r.Use(httplog.RequestLogger(httplog.NewLogger("ripfs", httplog.DefaultOptions)))

	reg := &IpfsRegistry{}
	reader := ipfs{client: client}

	// Health
	r.Get("/v2/", reg.buildHealthHandler(reader))

	r.Route("/v2/ipfs/{cid:[a-z0-9]+}", func(r chi.Router) {
		// HEAD: Manifests
		r.Head("/manifests/{reference}", reg.buildGetManifestHandler(reader))

		// GET: Manifests
		r.Get("/manifests/{reference}", reg.buildGetManifestHandler(reader))

		// HEAD: Blobs
		r.Head("/blobs/{reference}", reg.buildGetBlobsHandler(reader))

		// GET: Blobs
		r.Get("/blobs/{reference}", reg.buildGetBlobsHandler(reader))
	})

	reg.Router = r
	return reg
}

func (i *IpfsRegistry) buildHealthHandler(rdr Reader) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}
}

func (i *IpfsRegistry) buildGetManifestHandler(rdr Reader) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		content, mediaType, err := rdr.ReadManifest(ctx, chi.URLParam(r, "cid"), chi.URLParam(r, "reference"))
		if err != nil {
			return
		}

		w.Header().Set("Content-Type", mediaType)
		http.ServeContent(w, r, "", time.Now(), content)
	}
}

func (i *IpfsRegistry) buildGetBlobsHandler(rdr Reader) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		d, err := digest.Parse(chi.URLParam(r, "reference"))
		if err != nil {
			return
		}

		content, mediaType, err := rdr.ReadBlob(ctx, chi.URLParam(r, "cid"), d)
		if err != nil {
			return
		}

		w.Header().Set("Content-Type", mediaType)
		http.ServeContent(w, r, "", time.Now(), content)
	}
}

type ipfs struct {
	client iface.CoreAPI
}

// ReadManifest returns an io.ReadSeeker for the ipfs backed manifest
// ref: https://github.com/containerd/stargz-snapshotter/blob/v0.10.0/docs/ipfs.md#ipfs-enabled-oci-image
func (i ipfs) ReadManifest(ctx context.Context, name string, reference string) (io.ReadSeeker, string, error) {
	c, err := cid.Decode(name)
	if err != nil {
		return nil, "", err
	}

	if reference == "latest" {
		// This step is always the same
		rootf, err := i.open(ctx, c)
		if err != nil {
			return nil, "", err
		}

		idxc, _, idxmt, err := i.step(rootf)
		if err != nil {
			return nil, "", err
		}

		idxf, err := i.open(ctx, idxc)
		if err != nil {
			return nil, "", err
		}

		return idxf, idxmt, nil
	}

	d, err := digest.Parse(reference)
	if err != nil {
		return nil, "", fmt.Errorf("reference must either be 'latest' or a valid digest: %v", err)
	}

	// Everything not at the root gets walked
	return i.ReadBlob(ctx, name, d)
}

func (i ipfs) ReadBlob(ctx context.Context, name string, d digest.Digest) (io.ReadSeeker, string, error) {
	rootc, err := cid.Decode(name)
	if err != nil {
		return nil, "", err
	}

	var (
		found  bool
		fcid   cid.Cid
		fmtype string
	)

	if err := i.walk(ctx, rootc, func(c cid.Cid, _d digest.Digest, mt string) error {
		if d == _d {
			found = true
			fcid = c
			fmtype = mt
		}

		return nil
	}); err != nil {
		return nil, "", err
	}

	if !found {
		return nil, "", fmt.Errorf("didn't find desired digest %s", d.String())
	}

	ff, err := i.open(ctx, fcid)
	if err != nil {
		return nil, "", err
	}

	return ff, fmtype, nil
}

func (i ipfs) open(ctx context.Context, c cid.Cid) (files.File, error) {
	n, err := i.client.Unixfs().Get(ctx, path.IpfsPath(c))
	if err != nil {
		return nil, err
	}

	f, ok := n.(files.File)
	if !ok {
		return nil, fmt.Errorf("expected a file")
	}
	return f, nil
}

func (i ipfs) resolveCids(urls []string) (cid.Cid, error) {
	if len(urls) > 1 {
		return cid.Cid{}, fmt.Errorf("expected a single cid, got %d", len(urls))
	}

	u, err := url.Parse(urls[0])
	if err != nil {
		return cid.Cid{}, err
	}

	if u.Scheme != ipfsSchemePrefix {
		return cid.Cid{}, fmt.Errorf("expected ipfs scheme prefix, but got %s", u.Scheme)
	}

	return cid.Decode(u.Hostname())
}

type catch struct {
	MediaType string   `json:"mediaType"`
	Digest    string   `json:"digest,omitempty"`
	URLs      []string `json:"urls,omitempty"`
	Manifests []struct {
		MediaType string   `json:"mediaType,omitempty"`
		Digest    string   `json:"digest,omitempty"`
		URLs      []string `json:"urls,omitempty"`
	} `json:"manifests,omitempty"`
}

func (i ipfs) walk(ctx context.Context, rootc cid.Cid, fn func(c cid.Cid, d digest.Digest, mt string) error) error {
	rootf, err := i.open(ctx, rootc)
	if err != nil {
		return err
	}

	idxc, idxd, idxmt, err := i.step(rootf)

	if err := fn(idxc, idxd, idxmt); err != nil {
		return err
	}

	idxf, err := i.open(ctx, idxc)
	if err != nil {
		return err
	}

	mc, md, mmt, err := i.step(idxf)
	if err != nil {
		return err
	}

	if err := fn(mc, md, mmt); err != nil {
		return err
	}

	mf, err := i.open(ctx, mc)
	if err != nil {
		return err
	}

	var m v1.Manifest
	if err := json.NewDecoder(mf).Decode(&m); err != nil {
		return err
	}

	cc, err := i.resolveCids(m.Config.URLs)
	if err != nil {
		return err
	}

	if err := fn(cc, m.Config.Digest, m.Config.MediaType); err != nil {
		return err
	}

	for _, layer := range m.Layers {
		lc, err := i.resolveCids(layer.URLs)
		if err != nil {
			return err
		}

		if err := fn(lc, layer.Digest, layer.MediaType); err != nil {
			return err
		}
	}

	return nil
}

// step will search for a digest one step down, and will return a cid if found
func (i ipfs) step(f files.File) (cid.Cid, digest.Digest, string, error) {
	var robj catch
	if err := json.NewDecoder(f).Decode(&robj); err != nil {
		return cid.Cid{}, "", "", err
	}

	var (
		ds   string
		urls []string
	)

	if robj.URLs != nil {
		ds = robj.Digest
		urls = robj.URLs
	} else if len(robj.Manifests) == 1 {
		ds = robj.Manifests[0].Digest
		urls = robj.Manifests[0].URLs
	} else {
		return cid.Cid{}, "", "", fmt.Errorf("nope")
	}

	c, err := i.resolveCids(urls)
	if err != nil {
		return cid.Cid{}, "", "", err
	}

	d, err := digest.Parse(ds)
	if err != nil {
		return cid.Cid{}, "", "", err
	}

	return c, d, robj.MediaType, nil

}
