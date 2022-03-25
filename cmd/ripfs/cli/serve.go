package cli

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/hashicorp/go-multierror"
	iface "github.com/ipfs/interface-go-ipfs-core"
	"github.com/spf13/cobra"

	"github.com/joshrwolf/ripfs/internal/registry"
)

type serveCommandOpts struct {
	ipfsOpts *ipfsSharedOpts

	Address    string
	Standalone bool
}

func newServeCommand() *cobra.Command {
	o := &serveCommandOpts{ipfsOpts: &ipfsOpts}

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the ripfs registry server",
		RunE: func(cmd *cobra.Command, args []string) error {
			return o.Run(cmd.Context())
		},
	}

	f := cmd.Flags()
	f.StringVarP(&o.Address, "address", "a", "0.0.0.0:5050",
		"Address to serve on.")
	f.BoolVar(&o.Standalone, "standalone", false,
		"Toggle standalone mode (not part of a swarm), useful for localized deployments.")

	o.ipfsOpts.Flags(cmd)

	return cmd
}

func (o *serveCommandOpts) Run(ctx context.Context) error {
	ipfsDaemon, ipfsClient, _, err := o.ipfsOpts.initIpfs(false)
	if err != nil {
		return err
	}

	opts := &registry.IpfsRegistryOpts{}
	h := registry.NewIpfsRegistry(ipfsClient, opts)

	errc := make(chan error)
	go func() {
		defer ipfsDaemon.Unlock()
		if err := ipfsDaemon.Start(ctx); err != nil {
			errc <- err
		}
	}()

	http.Handle("/", h.Router)

	if !o.Standalone {
		if err := o.ensureSwarmed(ctx, ipfsClient); err != nil {
			return err
		}
	}

	go func() {
		fmt.Println("starting registry on: ", o.Address)
		if err := http.ListenAndServe(o.Address, nil); err != nil {
			errc <- err
		}
	}()

	select {
	case <-ctx.Done():

	case <-errc:
		var errs error
		for e := range errc {
			errs = multierror.Append(e)
		}
		if errs != nil {
			return errs
		}
	}

	return nil
}

func (o *serveCommandOpts) ensureSwarmed(ctx context.Context, client iface.CoreAPI) error {
	// TODO: Make this timeout
	for {
		println("waiting to join a swarm...")
		time.Sleep(5 * time.Second)
		peers, err := client.Swarm().Peers(ctx)
		if err != nil {
			return err
		}

		if len(peers) < 1 {
			continue
		}

		println("swarm joined")
		break
	}
	return nil
}
