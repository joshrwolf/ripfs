package cli

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	config "github.com/ipfs/go-ipfs-config"
	httpapi "github.com/ipfs/go-ipfs-http-client"
	"github.com/ipfs/go-ipfs/plugin/loader"
	"github.com/ipfs/go-ipfs/repo"
	"github.com/ipfs/go-ipfs/repo/fsrepo"
	iface "github.com/ipfs/interface-go-ipfs-core"
	"github.com/multiformats/go-multiaddr"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/joshrwolf/ripfs/internal/ipfs"
)

func New() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ripfs",
		Short: "Registry backed by IPFS",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	viper.AutomaticEnv()
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))

	cmd.AddCommand(
		newManagerCommand(),
		newServeCommand(),
		newAddCommand(),
		newInstallCommand(),
	)

	return cmd
}

var ipfsOpts = ipfsSharedOpts{}

type ipfsSharedOpts struct {
	RepoPath       string
	ApiAddress     string
	GatewayAddress string
	BootstrapPeers []string
}

func (o *ipfsSharedOpts) Flags(cmd *cobra.Command) {
	f := cmd.Flags()

	f.StringVar(&o.RepoPath, "ipfs-path", "/data/ipfs",
		"The path to the ipfs repo.")
	viper.BindPFlag("ipfs-path", f.Lookup("ipfs-path"))

	f.StringVar(&o.ApiAddress, "ipfs-api-address", "/ip4/127.0.0.1/tcp/5001",
		"The multicast address to use for the ipfs api.")
	f.StringVar(&o.GatewayAddress, "ipfs-gateway-address", "/ip4/127.0.0.1/tcp/8080",
		"The multicast address to use for the ipfs gateway.")

	f.StringSliceVar(&o.BootstrapPeers, "ipfs-bootstrap-peers", []string{},
		"List of bootstrap peers to configure.")
	viper.BindPFlag("ipfs-bootstrap-peers", f.Lookup("ipfs-bootstrap-peers"))
}

func (o *ipfsSharedOpts) initRepo() error {
	if err := func() error {
		plugins, err := loader.NewPluginLoader("")
		if err != nil {
			return err
		}

		if err := plugins.Initialize(); err != nil {
			return err
		}

		if err := plugins.Inject(); err != nil {
			return err
		}
		return nil
	}(); err != nil {
		return fmt.Errorf("loading plugins: %v", err)
	}

	repoPath := viper.GetString("ipfs-path")
	if fsrepo.IsInitialized(repoPath) {
		return nil
	}

	if err := os.MkdirAll(repoPath, os.ModePerm); err != nil {
		return err
	}

	cfg, err := config.Init(io.Discard, 2048)
	if err != nil {
		return err
	}

	cfg.Addresses.API = []string{o.ApiAddress}
	cfg.Addresses.Gateway = []string{o.GatewayAddress}
	cfg.Datastore = config.Datastore{
		StorageMax: "50GB",
		Spec: map[string]interface{}{
			"type":   "measure",
			"prefix": "badger.datastore",
			"child": map[string]interface{}{
				"type":       "badgerds",
				"path":       "badgerds",
				"syncWrites": false,
				"truncate":   true,
			},
		},
	}

	// TODO: Make this work without mDNS?
	// cfg.Discovery.MDNS.Enabled = false

	// https://docs.ipfs.io/how-to/configure-node/#swarm
	cfg.Swarm.DisableNatPortMap = true

	fmt.Println("bootstrap peers: ", viper.GetStringSlice("ipfs-bootstrap-peers"))
	cfg.Bootstrap = viper.GetStringSlice("ipfs-bootstrap-peers")

	if err := fsrepo.Init(repoPath, cfg); err != nil {
		return err
	}

	swarmKeyPath := filepath.Join(repoPath, "swarm.key")
	if _, err := os.Stat(swarmKeyPath); err != nil {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return fmt.Errorf("generating swarm key: %v", err)
		}

		swarmKey := fmt.Sprintf("/key/swarm/psk/1.0.0/\n/base16/\n%s", hex.EncodeToString(key))
		if err := os.WriteFile(swarmKeyPath, []byte(swarmKey), 0644); err != nil {
			return err
		}
	}

	return nil
}

func (o *ipfsSharedOpts) initIpfs(bootstrapper bool) (*ipfs.Daemon, iface.CoreAPI, repo.Repo, error) {
	ipfsRepoPath := viper.GetString("ipfs-path")

	if err := o.initRepo(); err != nil {
		return nil, nil, nil, err
	}

	d, err := ipfs.NewDaemon(ipfsRepoPath, bootstrapper)
	if err != nil {
		return nil, nil, nil, err
	}

	r, err := d.Open()
	if err != nil {
		return nil, nil, nil, err
	}

	ma, err := multiaddr.NewMultiaddr(o.ApiAddress)
	if err != nil {
		return nil, nil, nil, err
	}

	c, err := httpapi.NewApi(ma)
	if err != nil {
		return nil, nil, nil, err
	}

	return d, c, r, nil
}
