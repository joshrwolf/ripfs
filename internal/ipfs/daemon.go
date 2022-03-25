package ipfs

import (
	"context"
	"fmt"
	"sync"
	"time"

	config "github.com/ipfs/go-ipfs-config"
	files "github.com/ipfs/go-ipfs-files"
	httpapi "github.com/ipfs/go-ipfs-http-client"
	"github.com/ipfs/go-ipfs/commands"
	"github.com/ipfs/go-ipfs/core"
	"github.com/ipfs/go-ipfs/core/corehttp"
	"github.com/ipfs/go-ipfs/core/node/libp2p"
	"github.com/ipfs/go-ipfs/repo"
	"github.com/ipfs/go-ipfs/repo/fsrepo"
	iface "github.com/ipfs/interface-go-ipfs-core"
	"github.com/ipfs/interface-go-ipfs-core/options"
	"github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

var _ manager.Runnable = (*Daemon)(nil)

type Daemon struct {
	path string
	repo repo.Repo

	bootstrapper bool
}

// NewDaemon returns a Daemon
func NewDaemon(repoPath string, bootstrapper bool) (*Daemon, error) {
	if !fsrepo.IsInitialized(repoPath) {
		return nil, fmt.Errorf("repo at %s not initialized", repoPath)
	}

	return &Daemon{
		path:         repoPath,
		bootstrapper: bootstrapper,
	}, nil
}

func (d *Daemon) Open() (repo.Repo, error) {
	r, err := fsrepo.Open(d.path)
	if err != nil {
		return nil, err
	}
	d.repo = r
	return r, nil
}

func (d *Daemon) Unlock() error {
	if d.repo == nil {
		return nil
	}

	if err := d.repo.Close(); err != nil {
		return err
	}

	d.repo = nil
	return nil
}

func (d *Daemon) Start(ctx context.Context) error {
	if d.repo == nil {
		return fmt.Errorf("repo is unopened, open it first before starting the node")
	}

	cfg, err := d.repo.Config()
	if err != nil {
		return err
	}

	var (
		errc = make(chan error)
		wg   sync.WaitGroup
	)

	node, err := core.NewNode(ctx, &core.BuildCfg{
		Online:  true, // This doesn't do what you think it does
		Routing: libp2p.DHTOption,
		Repo:    d.repo,
	})
	if err != nil {
		return err
	}

	node.IsDaemon = true
	if node.PNetFingerprint != nil {
		fmt.Println("Swarm key fingerprint: ", node.PNetFingerprint)
	}

	// Start api servers
	apiOpts := []corehttp.ServeOption{
		corehttp.VersionOption(),
		corehttp.LogOption(),
		corehttp.CommandsOption(d.reqctx(node)),
	}
	for _, addr := range cfg.Addresses.API {
		a := addr
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := d.serve(node, a, apiOpts...); err != nil {
				errc <- err
			}
		}()
	}

	// Start gateway servers
	// gwOpts := []corehttp.ServeOption{
	// 	corehttp.HostnameOption(),
	// 	corehttp.GatewayOption(true, "/ipfs", "/ipns"),
	// 	corehttp.VersionOption(),
	// 	corehttp.CheckVersionOption(),
	// 	corehttp.CommandsROOption(d.reqctx(node)),
	// }
	// for _, addr := range cfg.Addresses.Gateway {
	// 	a := addr
	// 	wg.Add(1)
	// 	go func() {
	// 		defer wg.Done()
	// 		if err := d.serve(node, a, gwOpts...); err != nil {
	// 			errc <- err
	// 		}
	// 	}()
	// }

	if d.bootstrapper {
		go func() {
			for {
				time.Sleep(5 * time.Second)
				println("waiting for swarm peers...")
				c, err := d.httpClient()
				if err != nil {
					errc <- err
				}
				ci, err := c.Swarm().Peers(ctx)
				if err != nil {
					errc <- err
				}

				if len(ci) == 0 {
					continue
				}

				println("swarm established, adding index to ipns")
				e, err := d.createMapper(ctx, c)
				if err != nil {
					break
				}
				_ = e
				break
			}
		}()

		go func() {
			for {
				time.Sleep(10 * time.Second)
				c, err := d.httpClient()
				if err != nil {
					errc <- err
				}
				ci, err := c.Swarm().Peers(ctx)
				if err != nil {
					errc <- err
				}

				println("connected to swarm of ", len(ci)-1, " peers")
			}
		}()
	}

	select {
	case <-ctx.Done():

	case err := <-errc:
		return err
	}

	return nil
}

func (d *Daemon) serve(node *core.IpfsNode, addr string, opt ...corehttp.ServeOption) error {
	ma, err := multiaddr.NewMultiaddr(addr)
	if err != nil {
		return err
	}

	l, err := manet.Listen(ma)
	if err != nil {
		return err
	}

	return corehttp.Serve(node, manet.NetListener(l), opt...)
}

func (d *Daemon) reqctx(node *core.IpfsNode) commands.Context {
	return commands.Context{
		ConfigRoot: d.path,
		ReqLog:     &commands.ReqLog{},
		ConstructNode: func() (*core.IpfsNode, error) {
			return node, nil
		},
		LoadConfig: func(_ string) (*config.Config, error) {
			cfg, err := node.Repo.Config()
			if err != nil {
				return nil, err
			}
			return cfg.Clone()
		},
	}
}

func (d *Daemon) httpClient() (*httpapi.HttpApi, error) {
	cfg, err := d.repo.Config()
	if err != nil {
		return nil, err
	}

	ma, err := multiaddr.NewMultiaddr(cfg.Addresses.API[0])
	if err != nil {
		return nil, err
	}

	return httpapi.NewApi(ma)
}

func (d *Daemon) createMapper(ctx context.Context, c *httpapi.HttpApi) (iface.IpnsEntry, error) {
	f := files.NewBytesFile([]byte(`{}`))
	p, err := c.Unixfs().Add(ctx, f, options.Unixfs.Pin(true), options.Unixfs.CidVersion(1))
	if err != nil {
		return nil, err
	}

	e, err := c.Name().Publish(ctx, p, func(settings *options.NamePublishSettings) error {
		settings.AllowOffline = true
		return nil
	})
	if err != nil {
		return nil, err
	}

	// TODO: Make this it's own controller or something
	kcfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}

	kc, err := client.New(kcfg, client.Options{})
	if err != nil {
		return nil, err
	}

	idxIpnsSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ripfs-index",
			Namespace: "ripfs-system",
		},
		Data: map[string][]byte{
			"ipns": []byte(e.Name()),
		},
		Type: corev1.SecretTypeOpaque,
	}

	if err := kc.Create(ctx, idxIpnsSecret, &client.CreateOptions{}); err != nil {
		return nil, err
	}

	println("Created ipns secret: ", idxIpnsSecret.GetName(), " with ipns idx: ", e.Name())
	return e, nil
}
