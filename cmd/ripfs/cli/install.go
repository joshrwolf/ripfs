package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/fluxcd/pkg/ssa"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/mholt/archiver/v4"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/joshrwolf/ripfs/config"
	"github.com/joshrwolf/ripfs/internal/consts"
	"github.com/joshrwolf/ripfs/internal/k8s"
	"github.com/joshrwolf/ripfs/internal/k8s/offline"
	"github.com/joshrwolf/ripfs/internal/manifests"
)

type installCommandOpts struct {
	Offline   string
	Namespace string
	Timeout   time.Duration
	Export    bool
}

func newInstallCommand() *cobra.Command {
	o := &installCommandOpts{}

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install ripfs into a cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			return o.Run(cmd.Context())
		},
	}

	f := cmd.Flags()

	f.StringVar(&o.Offline, "offline", "",
		"Performs an offline installation with the specified payload.")
	f.StringVar(&o.Namespace, "namespace", "ripfs-system",
		"The installation namespace.")
	f.DurationVarP(&o.Timeout, "timeout", "t", 1*time.Minute,
		"Timeout duration for the install.")
	f.BoolVar(&o.Export, "export", false,
		"When enabled, manifests will be written to stdout and not applied to the cluster.")

	return cmd
}

func (o *installCommandOpts) Run(ctx context.Context) error {
	l := zerolog.New(zerolog.NewConsoleWriter()).With().Timestamp().Logger()
	ctx = l.WithContext(ctx)

	kcfg := ctrl.GetConfigOrDie()

	mopts := manifests.DefaultOpts()

	if o.Offline != "" {
		// hoh boy... hold on to your seats
		pl, teardown, err := o.prepPayload(ctx)
		if err != nil {
			return err
		}
		defer teardown()

		// TODO: This is cheating
		rimgs, err := o.getImage(pl.(*offline.LayoutPayload).Path)
		if err != nil {
			return fmt.Errorf("loading image: %v", err)
		}

		s := offline.NewSeeder(kcfg, pl)
		mi, err := s.Seed(ctx, nil, rimgs)
		if err != nil {
			return err
		}

		if len(mi) != 1 {
			return fmt.Errorf("expecting 1 image to be seeded, got %d", len(mi))
		}

		l.Info().Msgf("successfully seeded ripfs image(s) to target cluster")
		mopts.ManagerImage = mi[0]
	}

	gen := manifests.NewGenerator(mopts)
	data, err := gen.Generate(ctx, config.EmbeddedManifests)
	if err != nil {
		return err
	}

	if o.Export {
		fmt.Println(string(data))
		return nil
	}

	buf := bytes.NewReader(data)
	objs, err := ssa.ReadObjects(buf)
	if err != nil {
		return err
	}

	a, err := k8s.NewApplier(kcfg)
	if err != nil {
		return err
	}

	l.Info().Msgf("applying ripfs components to cluster")
	cs, err := a.Apply(ctx, objs)
	if err != nil {
		return err
	}
	_ = cs

	l.Info().Msgf("successfully installed ripfs!")

	return nil
}

func (o *installCommandOpts) prepPayload(ctx context.Context) (offline.Payload, func() error, error) {
	tmp, err := os.MkdirTemp("", consts.Name)
	if err != nil {
		return nil, nil, err
	}

	af, err := os.Open(o.Offline)
	if err != nil {
		return nil, nil, err
	}
	defer af.Close()

	format, input, err := archiver.Identify(o.Offline, af)
	if err != nil {
		return nil, nil, err
	}

	if ex, ok := format.(archiver.Extractor); ok {
		if err := ex.Extract(ctx, input, nil, func(ctx context.Context, f archiver.File) error {
			wp := filepath.Join(tmp, f.NameInArchive)
			if f.IsDir() {
				if err := os.MkdirAll(wp, os.ModePerm); err != nil {
					return err
				}
				return nil
			}

			rc, err := f.Open()
			if err != nil {
				return err
			}
			defer rc.Close()

			wf, err := os.Create(wp)
			if err != nil {
				return err
			}
			defer wf.Close()

			if _, err := io.Copy(wf, rc); err != nil {
				return err
			}

			return nil
		}); err != nil {
			return nil, nil, err
		}
	}

	lp, err := offline.NewLayoutPayload(filepath.Join(tmp, "payload/oci"))
	if err != nil {
		return nil, nil, err
	}

	teardown := func() error {
		return os.RemoveAll(tmp)
	}

	return lp, teardown, nil
}

func (o *installCommandOpts) getImage(path string) (v1.Image, error) {
	// TODO: This is cheating
	l, err := layout.FromPath(path)
	if err != nil {
		return nil, err
	}

	p := v1.Platform{OS: "linux", Architecture: "amd64"}

	idx, err := l.ImageIndex()
	if err != nil {
		return nil, err
	}

	idxm, err := idx.IndexManifest()
	if err != nil {
		return nil, err
	}

	// TODO: A better human than me would use recursion
	var img v1.Image
	for _, m := range idxm.Manifests {
		switch m.MediaType {
		case types.DockerManifestList, types.OCIImageIndex:
			idx, err := idx.ImageIndex(m.Digest)
			if err != nil {
				return nil, err
			}

			idxm, err := idx.IndexManifest()
			if err != nil {
				return nil, err
			}

			for _, m := range idxm.Manifests {
				if m.Platform.Equals(p) {
					img, err = idx.Image(m.Digest)
					break
				}
			}
		}
	}

	if img == nil {
		return nil, fmt.Errorf("couldn't find image")
	}
	return img, nil
}
