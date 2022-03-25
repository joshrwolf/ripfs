package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"
	files "github.com/ipfs/go-ipfs-files"
	httpapi "github.com/ipfs/go-ipfs-http-client"
	iface "github.com/ipfs/interface-go-ipfs-core"
	iopts "github.com/ipfs/interface-go-ipfs-core/options"
	"github.com/ipfs/interface-go-ipfs-core/path"
	"github.com/multiformats/go-multiaddr"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/joshrwolf/ripfs/internal/consts"
	"github.com/joshrwolf/ripfs/internal/k8s"
	"github.com/joshrwolf/ripfs/internal/registry"
)

type addCommandOpts struct {
	IPFSApiAddress string

	Name      string
	Namespace string
	Container string

	OS           string
	Architecture string
	Variant      string
}

func newAddCommand() *cobra.Command {
	o := &addCommandOpts{}

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add an image to the registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return o.Run(cmd.Context(), args[0])
		},
	}

	f := cmd.Flags()
	f.StringVarP(&o.IPFSApiAddress, "ipfs-api-address", "i", "/ip4/127.0.0.1/tcp/5001",
		"IPFS api address to use for communicating with the IPFS store.")
	f.StringVar(&o.Name, "pod-name", "ripfs-controller-manager",
		"Name of the service containing the IPFS api")
	f.StringVar(&o.Namespace, "pod-namespace", "ripfs-system",
		"Namespace of the service containing the IPFS api")
	f.StringVar(&o.Container, "container", "manager",
		"Container within pod to forward to.")

	f.StringVar(&o.Architecture, "arch", "amd64",
		"Image's architecture (only valid for remote images).")
	f.StringVar(&o.OS, "os", "linux",
		"Image's OS (only valid for remote images).")
	f.StringVar(&o.Variant, "variant", "",
		"Image's variant (only valid for remote images).")

	return cmd
}

func (o *addCommandOpts) Run(ctx context.Context, reference string) error {
	l := zerolog.New(zerolog.NewConsoleWriter()).With().Timestamp().Logger()
	ctx = l.WithContext(ctx)

	l.Debug().Msgf("loading k8s config")
	kcfg := ctrl.GetConfigOrDie()

	imgs, err := o.loadImages(ctx, reference)
	if err != nil {
		return fmt.Errorf("loading image: %v", err)
	}

	if o.Container != "" {
		// Open a tunnel to the ipfs pod
		t, err := k8s.NewTunneler(kcfg)
		if err != nil {
			return err
		}

		ipfsTargetPod, err := o.fwdTarget(ctx, kcfg)
		if err != nil {
			return err
		}

		l.Debug().Msgf("opening tunnel to ipfs api")
		fwd, err := t.Tunnel(ctx, ipfsTargetPod, []string{"5001:5001"})
		if err != nil {
			return err
		}
		defer fwd.Close()
	}

	// Add the image
	ma, err := multiaddr.NewMultiaddr(o.IPFSApiAddress)
	if err != nil {
		return err
	}

	client, err := httpapi.NewApi(ma)
	if err != nil {
		return err
	}

	for ref, img := range imgs {
		p, err := registry.AddImage(ctx, client, img)
		if err != nil {
			return err
		}
		l.Info().Msgf("added image with root cid [%s]", p.String())

		_, e, err := updateCidMap(ctx, client, kcfg, ref, p)
		if err != nil {
			return err
		}

		l.Info().Msgf("updated mapping [%s] with [%s] => [%s]", e.Name(), ref, p.String())
	}

	return nil
}

// loadImages takes an arbitrary reference and either:
// 		1) loads an image from a remote reference (ex: alpine:latest)
// 		2) loads images from an oci layout directory (ex: path/to/oci/layout
// 		3) loads images from a tarball (ex: path/to/tar.gz
func (o *addCommandOpts) loadImages(ctx context.Context, reference string) (map[string]v1.Image, error) {
	var (
		imgs = make(map[string]v1.Image)
		err  error
	)

	// Check if we've got a valid remote reference first
	iref, rerr := name.ParseReference(reference)
	if rerr == nil {
		err = o.loadImagesFromRemote(ctx, iref, imgs)
		return imgs, err
	}

	fi, err := os.Stat(reference)
	if err != nil {
		return nil, fmt.Errorf("provided reference %s is neither a valid remote image or local path", reference)
	}

	if fi.IsDir() {
		err = o.loadImagesFromLayout(reference, imgs)
	} else {
		err = o.loadImagesFromTar(reference, imgs)
	}

	return imgs, err
}

func (o *addCommandOpts) loadImagesFromRemote(ctx context.Context, ref name.Reference, imgMap map[string]v1.Image) error {
	l := zerolog.Ctx(ctx)

	p := v1.Platform{
		Architecture: o.Architecture,
		OS:           o.OS,
		Variant:      o.Variant,
	}

	opts := []remote.Option{
		remote.WithPlatform(p),
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
	}

	desc, err := remote.Get(ref, opts...)
	if err != nil {
		return err
	}

	switch desc.MediaType {
	case types.OCIImageIndex, types.DockerManifestList:
		// TODO: actually handle indexes

	case types.DockerManifestSchema1, types.DockerManifestSchema1Signed:
		return fmt.Errorf("lol no: %v", desc.MediaType)

	}

	l.Info().Msgf("loading remote image: %s", ref.Name())
	img, err := remote.Image(ref, opts...)
	if err != nil {
		return err
	}

	imgMap[ref.Name()] = img
	return nil
}

func (o *addCommandOpts) loadImagesFromTar(path string, imgMap map[string]v1.Image) error {
	img, err := tarball.ImageFromPath(path, nil)
	if err != nil {
		return err
	}
	// TODO: Fix
	imgMap["temp"] = img

	// TODO: handle multi image saves
	return nil
}

func (o *addCommandOpts) loadImagesFromLayout(path string, imgMap map[string]v1.Image) error {
	p, err := layout.FromPath(path)
	if err != nil {
		return err
	}

	idx, err := p.ImageIndex()
	if err != nil {
		return err
	}

	idxm, err := idx.IndexManifest()
	if err != nil {
		return err
	}

	for _, m := range idxm.Manifests {
		img, err := idx.Image(m.Digest)
		if err != nil {
			return fmt.Errorf("layout appears to be mismatched: %v", err)
		}

		imgMap[idxm.Annotations[ocispec.AnnotationTitle]] = img
	}

	return nil
}

func updateCidMap(ctx context.Context, api iface.CoreAPI, kcfg *rest.Config, ref string, op path.Resolved) (path.Resolved, iface.IpnsEntry, error) {
	kc, err := corev1client.NewForConfig(kcfg)
	if err != nil {
		return nil, nil, err
	}

	s, err := kc.Secrets("ripfs-system").Get(ctx, consts.CidMapperSecretName, metav1.GetOptions{})
	if err != nil {
		return nil, nil, err
	}

	idxPath, ok := s.Data[consts.CidMapperSecretKey]
	if !ok {
		return nil, nil, fmt.Errorf("couldn't find ipns key in secret: %v", s.Name)
	}

	// Update and publish the new ipns index
	p, err := api.Name().Resolve(ctx, string(idxPath))
	if err != nil {
		return nil, nil, err
	}

	nd, err := api.Unixfs().Get(ctx, p)
	if err != nil {
		return nil, nil, err
	}

	f, ok := nd.(files.File)
	if !ok {
		return nil, nil, fmt.Errorf("expected a file for the index, didn't get that")
	}
	defer f.Close()

	cidMap := make(map[string]string)
	if err := json.NewDecoder(f).Decode(&cidMap); err != nil {
		return nil, nil, err
	}

	cidMap[ref] = op.String()

	data, err := json.Marshal(cidMap)
	if err != nil {
		return nil, nil, err
	}

	nf := files.NewBytesFile(data)
	defer nf.Close()

	ap, err := api.Unixfs().Add(ctx, nf, iopts.Unixfs.Pin(true), iopts.Unixfs.CidVersion(1))
	if err != nil {
		return nil, nil, err
	}

	e, err := api.Name().Publish(ctx, ap, func(settings *iopts.NamePublishSettings) error {
		settings.AllowOffline = true
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	return ap, e, nil
}

func (o *addCommandOpts) fwdTarget(ctx context.Context, kcfg *rest.Config) (k8s.Target, error) {
	c, err := corev1client.NewForConfig(kcfg)
	if err != nil {
		return k8s.Target{}, err
	}

	svc, err := c.Services(o.Namespace).Get(ctx, o.Name, metav1.GetOptions{})
	if err != nil {
		return k8s.Target{}, err
	}

	var sls []string
	for k, v := range svc.Spec.Selector {
		sls = append(sls, k+"="+v)
	}

	pods, err := c.Pods(o.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: strings.Join(sls, ","),
		Limit:         1,
	})
	if err != nil {
		return k8s.Target{}, err
	}
	fwdPod := pods.Items[0]

	found := false
	for _, c := range fwdPod.Spec.Containers {
		if c.Name == o.Container {
			found = true
		}
	}

	if !found {
		return k8s.Target{}, fmt.Errorf("couldn't find container: %s in pod %s", o.Container, fwdPod.Name)
	}

	return k8s.Target{
		Name:      fwdPod.Name,
		Namespace: fwdPod.Namespace,
		Container: o.Container,
	}, nil
}
