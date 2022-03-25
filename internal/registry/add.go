package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/ipfs/go-cid"
	files "github.com/ipfs/go-ipfs-files"
	"github.com/ipfs/interface-go-ipfs-core"
	iopts "github.com/ipfs/interface-go-ipfs-core/options"
	"github.com/ipfs/interface-go-ipfs-core/path"
	"golang.org/x/sync/errgroup"
)

const (
	IPFSSchema = "ipfs://"
)

var addOpts = []iopts.UnixfsAddOption{
	iopts.Unixfs.Pin(true),
	iopts.Unixfs.CidVersion(1),
}

type IpfsManifest struct {
	MediaType types.MediaType `json:"mediaType"`
	Digest    v1.Hash         `json:"digest"`
	Size      int64           `json:"size"`
	URLs      []string        `json:"urls,omitempty"`
}

// AddImage adds an image to a given ipfs backend
func AddImage(ctx context.Context, api iface.CoreAPI, img v1.Image) (path.Resolved, error) {
	cidMap, err := writeLayers(ctx, api, img)
	if err != nil {
		return nil, err
	}

	// TODO: .RawConfigFile returns the "real" config, but .ConfigFile doesn't? somehow the two don't byte equal
	cfgData, err := img.RawConfigFile()
	if err != nil {
		return nil, err
	}

	cfgPath, err := api.Unixfs().Add(ctx, files.NewBytesFile(cfgData), addOpts...)
	if err != nil {
		return nil, fmt.Errorf("writing config to ipfs: %v", err)
	}

	manifest, err := img.Manifest()
	if err != nil {
		return nil, err
	}

	cidMap[manifest.Config.Digest] = cfgPath.Cid()

	ipfsManifest, err := convertIpfs(manifest, cidMap)
	if err != nil {
		return nil, err
	}

	ipfsManifestPath, ipfsManifestHash, ipfsManifestSize, err := writeObj(ctx, api, ipfsManifest)
	if err != nil {
		return nil, err
	}

	// Build/store the image's index
	idx := v1.IndexManifest{
		SchemaVersion: 2,
		MediaType:     types.OCIImageIndex,
		Manifests: []v1.Descriptor{
			{
				MediaType: ipfsManifest.MediaType,
				Size:      ipfsManifestSize,
				Digest:    ipfsManifestHash,
				URLs:      []string{IPFSSchema + ipfsManifestPath.Cid().String()},
			},
		},
	}

	idxPath, idxHash, idxSize, err := writeObj(ctx, api, idx)
	if err != nil {
		return nil, err
	}

	// TODO: Why does this use a non-standard struct?
	ipfsIdx := IpfsManifest{
		MediaType: types.OCIImageIndex,
		Digest:    idxHash,
		Size:      idxSize,
		URLs:      []string{IPFSSchema + idxPath.Cid().String()},
	}

	ipfsIdxPath, _, _, err := writeObj(ctx, api, ipfsIdx)
	return ipfsIdxPath, err
}

func convertIpfs(m *v1.Manifest, cids map[v1.Hash]cid.Cid) (*v1.Manifest, error) {
	n := m.DeepCopy()

	// Config
	if _, ok := cids[n.Config.Digest]; !ok {
		return nil, fmt.Errorf("config descriptor %s not found", n.Config.Digest.String())
	}
	n.Config.URLs = []string{IPFSSchema + cids[n.Config.Digest].String()}

	// Layers
	for i, layer := range n.Layers {
		if _, ok := cids[layer.Digest]; !ok {
			return nil, fmt.Errorf("layer descriptor %s not found", layer.Digest.String())
		}
		n.Layers[i].URLs = []string{IPFSSchema + cids[layer.Digest].String()}
	}
	return n, nil
}

// writeObj adds any marshallable object
func writeObj(ctx context.Context, api iface.CoreAPI, obj interface{}) (path.Resolved, v1.Hash, int64, error) {
	data, err := json.Marshal(obj)
	if err != nil {
		return nil, v1.Hash{}, 0, err
	}

	h, size, err := v1.SHA256(bytes.NewReader(data))
	if err != nil {
		return nil, v1.Hash{}, 0, err
	}

	p, err := api.Unixfs().Add(ctx, files.NewBytesFile(data), addOpts...)
	if err != nil {
		return nil, v1.Hash{}, 0, err
	}

	return p, h, size, nil
}

func writeLayers(ctx context.Context, api iface.CoreAPI, img v1.Image) (map[v1.Hash]cid.Cid, error) {
	cidMap := make(map[v1.Hash]cid.Cid)

	var g errgroup.Group
	layers, err := img.Layers()
	if err != nil {
		return nil, err
	}

	for _, layer := range layers {
		layer := layer
		g.Go(func() error {
			rc, err := layer.Compressed()
			if err != nil {
				return err
			}
			defer rc.Close()

			d, err := layer.Digest()
			if err != nil {
				return err
			}

			p, err := api.Unixfs().Add(ctx, files.NewReaderFile(rc), addOpts...)
			if err != nil {
				return err
			}

			cidMap[d] = p.Cid()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	return cidMap, nil
}
