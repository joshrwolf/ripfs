package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/random"
	config "github.com/ipfs/go-ipfs-config"
	files "github.com/ipfs/go-ipfs-files"
	"github.com/ipfs/go-ipfs/core"
	"github.com/ipfs/go-ipfs/core/coreapi"
	"github.com/ipfs/go-ipfs/core/node/libp2p"
	"github.com/ipfs/go-ipfs/plugin/loader"
	"github.com/ipfs/go-ipfs/repo/fsrepo"
	iface "github.com/ipfs/interface-go-ipfs-core"
	"github.com/ipfs/interface-go-ipfs-core/path"

	"github.com/joshrwolf/ripfs/internal/consts"
)

func TestServe(t *testing.T) {
	ctx := context.Background()

	client := testingIpfs(t, ctx)

	img, p := addImage(t, ctx, client)

	n, err := client.Unixfs().Get(ctx, p)
	if err != nil {
		t.Fatal(err)
	}

	f := files.ToFile(n)

	var idx struct {
		Digest string `json:"digest,omitempty"`
	}
	if err := json.NewDecoder(f).Decode(&idx); err != nil {
		t.Fatal(err)
	}

	layers, err := img.Layers()
	if err != nil {
		t.Fatal(err)
	}

	h, err := layers[0].Digest()
	if err != nil {
		t.Fatal(err)
	}

	s := NewIpfsRegistry(client)

	tests := []struct {
		name    string
		target  string
		wantErr bool
	}{
		{
			name:   "manifest root",
			target: fmt.Sprintf("/v2/ipfs/%s/manifests/latest", p.Cid().String()),
		},
		{
			name:   "index",
			target: fmt.Sprintf("/v2/ipfs/%s/manifests/%s", p.Cid().String(), idx.Digest),
		},
		{
			name:   "manifest",
			target: fmt.Sprintf("/v2/ipfs/%s/manifests/%s", p.Cid().String(), h.String()),
		},
		{
			name:   "blob",
			target: fmt.Sprintf("/v2/ipfs/%s/blobs/%s", p.Cid().String(), h.String()),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			rr := httptest.NewRecorder()

			s.Router.ServeHTTP(rr, req)

			r := rr.Result()

			println("content type: ", r.Header.Get("Content-Type"))
			println("content length: ", r.Header.Get("Content-Length"))

			d, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}

			println(string(d))
		})
	}
}

func testingIpfs(t *testing.T, ctx context.Context) iface.CoreAPI {
	tmp, err := os.MkdirTemp("", consts.Name)
	if err != nil {
		t.Fatal(err)
	}

	plugins, err := loader.NewPluginLoader("")
	if err != nil {
		t.Fatal(err)
	}

	if err := plugins.Initialize(); err != nil {
		t.Fatal(err)
	}

	if err := plugins.Inject(); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Init(ioutil.Discard, 2048)
	if err != nil {
		t.Fatal(err)
	}

	if err := fsrepo.Init(tmp, cfg); err != nil {
		t.Fatal(err)
	}

	repo, err := fsrepo.Open(tmp)
	if err != nil {
		t.Fatal(err)
	}

	nopts := &core.BuildCfg{
		Online:  false,
		Routing: libp2p.DHTOption,
		Repo:    repo,
	}

	node, err := core.NewNode(ctx, nopts)
	if err != nil {
		t.Fatal(err)
	}

	api, err := coreapi.NewCoreAPI(node)
	if err != nil {
		t.Fatal(err)
	}
	return api
}

func addImage(t *testing.T, ctx context.Context, client iface.CoreAPI) (v1.Image, path.Resolved) {
	img, err := random.Image(1024, 3)
	if err != nil {
		t.Fatal(err)
	}

	p, err := AddImage(ctx, client, img)
	if err != nil {
		t.Fatal(err)
	}

	return img, p
}
