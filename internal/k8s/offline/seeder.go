package offline

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/ipfs/go-ipfs-http-client"
	"github.com/ipfs/interface-go-ipfs-core"
	"github.com/multiformats/go-multiaddr"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/kubernetes/scheme"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/tools/remotecommand"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/joshrwolf/ripfs/internal/k8s"
	"github.com/joshrwolf/ripfs/internal/registry"
)

// seeder seeds a local image into a kubernetes cluster
type seeder struct {
	kcfg    *rest.Config
	payload Payload
}

func NewSeeder(kcfg *rest.Config, payload Payload) *seeder {
	return &seeder{
		kcfg:    kcfg,
		payload: payload,
	}
}

// Seed seeds a specified node with a specified image
func (s *seeder) Seed(ctx context.Context, nodes []string, imgs ...v1.Image) ([]string, error) {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	l := zerolog.Ctx(ctx)

	ap, err := k8s.NewApplier(s.kcfg)
	if err != nil {
		return nil, err
	}

	l.Info().Msgf("creating seed configmap payload")
	scm, err := s.configMap()
	if err != nil {
		return nil, err
	}
	scmObj, _ := uconverter(scm)

	seedObjs := []*unstructured.Unstructured{scmObj}
	if _, err := ap.Apply(ctx, seedObjs); err != nil {
		return nil, err
	}

	pods, objs, err := s.seeds(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer ap.Delete(ctx, objs)

	errs, ctx := errgroup.WithContext(ctx)
	refs := make([]string, len(imgs))
	for _, pod := range pods.Items {
		nl := l.With().Str("node", pod.Spec.NodeName).Logger()
		node := pod.Spec.NodeName

		target := k8s.Target{
			Name:      pod.Name,
			Namespace: pod.Namespace,
			Container: pod.Spec.Containers[0].Name,
		}

		errs.Go(func() error {
			go func() {
				nl.Info().Msgf("starting registry")
				s.exec(target, []string{"/ripfs/bin/ripfs", "serve", "--standalone"})
			}()

			// TODO: lol, do an actual healthcheck
			time.Sleep(5 * time.Second)

			c, fwder, err := s.connect(ctx, target)
			if err != nil {
				return err
			}
			defer fwder.Close()

			nl.Info().Msgf("connected to registry at %s", target.Name)

			// TODO: Run these in goroutine, just scared of overloading api server
			for i, img := range imgs {
				ref, err := s.load(ctx, c, node, img)
				if err != nil {
					return fmt.Errorf("loading: %v", err)
				}
				refs[i] = ref
			}
			return nil
		})

	}

	if err := errs.Wait(); err != nil {
		return nil, err
	}

	return refs, nil
}

// findHostImage either looks up or finds an existing image in the cluster to act as the seed pod
func (s *seeder) findHostImage() string {
	// TODO: actually implement this
	return "docker.io/rancher/mirrored-pause:3.6"
}

func (s *seeder) listNodes(ctx context.Context) (*corev1.NodeList, error) {
	mgr, err := k8s.NewManager(s.kcfg)
	if err != nil {
		return nil, err
	}

	nodes := &corev1.NodeList{}
	if err := mgr.Client().List(ctx, nodes, &client.ListOptions{}); err != nil {
		return nil, err
	}

	return nodes, nil
}

// configMap builds the configmap for the right platform from the embedded busybox's
func (s *seeder) configMap() (*corev1.ConfigMap, error) {
	f, err := busybox.Open("payload/busybox-linux-amd64")
	if err != nil {
		return nil, err
	}

	bbdata, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	cm := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "seed-payload",
			Namespace: "default",
		},
		BinaryData: map[string][]byte{
			"busybox": bbdata,
		},
	}

	return cm, nil
}

// load will spin up a loader pod to seed the node
func (s *seeder) load(ctx context.Context, c iface.CoreAPI, node string, img v1.Image) (string, error) {
	l := zerolog.Ctx(ctx).With().Str("node", node).Logger()

	h, err := img.Digest()
	if err != nil {
		return "", err
	}

	l.Info().Msgf("seeding registry with image: %s", h.String())
	p, err := registry.AddImage(ctx, c, img)
	if err != nil {
		return "", err
	}

	image := fmt.Sprintf("localhost:31619%s", p.String())
	var perm = int32(int64(0777))

	r := rand.String(5)
	job := &batchv1.Job{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "batch/v1",
			Kind:       "Job",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "loader-" + r,
			Namespace: "default",
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:            "loader",
							Image:           image,
							ImagePullPolicy: corev1.PullAlways,
							Command:         []string{"/tmp/busybox", "sh", "-c", "id"},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "busybox",
									MountPath: "/tmp",
								},
							},
						},
					},
					NodeName: node,
					Volumes: []corev1.Volume{
						{
							Name: "busybox",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "seed-payload",
									},
									DefaultMode: &perm,
								},
							},
						},
					},
				},
			},
		},
	}
	jobObj, _ := uconverter(job)

	ap, err := k8s.NewApplier(s.kcfg)
	if err != nil {
		return "", err
	}

	l.Info().Msgf("creating loader job: %s", job.Name)
	objs := []*unstructured.Unstructured{jobObj}
	if _, err := ap.Apply(ctx, objs); err != nil {
		return "", err
	}
	defer ap.Delete(ctx, objs)

	return image, nil
}

// connect will open a connection to a seed pod
func (s *seeder) connect(ctx context.Context, target k8s.Target) (iface.CoreAPI, *portforward.PortForwarder, error) {
	t, err := k8s.NewTunneler(s.kcfg)
	if err != nil {
		return nil, nil, err
	}

	fwd, err := t.Tunnel(ctx, target, []string{":5001"})
	if err != nil {
		return nil, nil, err
	}

	ports, err := fwd.GetPorts()
	if err != nil {
		return nil, nil, err
	}

	if len(ports) > 1 {
		return nil, nil, fmt.Errorf("expected 1 port to be forwarded, got %d", len(ports))
	}

	addr := fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", ports[0].Local)
	ma, err := multiaddr.NewMultiaddr(addr)
	if err != nil {
		return nil, nil, err
	}

	api, err := httpapi.NewApi(ma)
	if err != nil {
		return nil, nil, err
	}

	return api, fwd, nil
}

func (s *seeder) seeds(ctx context.Context, nodeSelector []string) (*corev1.PodList, []*unstructured.Unstructured, error) {
	l := zerolog.Ctx(ctx)

	mgr, err := k8s.NewManager(s.kcfg)
	if err != nil {
		return nil, nil, err
	}

	nodes := &corev1.NodeList{}
	if err := mgr.Client().List(ctx, nodes, &client.ListOptions{
		// TODO: Selector
	}); err != nil {
		return nil, nil, err
	}

	ap, err := k8s.NewApplier(s.kcfg)
	if err != nil {
		return nil, nil, err
	}

	var (
		objs     []*unstructured.Unstructured
		selector = map[string]string{
			"app":  "ripfs",
			"type": "seeder",
		}
	)

	for _, no := range nodes.Items {
		d, err := s.payload.Deployment(s.findHostImage(), no.Name, selector)
		if err != nil {
			return nil, nil, err
		}
		obj, err := uconverter(d)
		if err != nil {
			return nil, nil, err
		}

		objs = append(objs, obj)
	}

	// Apply service
	svc := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "seeder",
			Namespace: "default",
		},
		Spec: corev1.ServiceSpec{
			Type:     "NodePort",
			Selector: selector,
			Ports: []corev1.ServicePort{
				{
					Name:     "tcp-registry",
					Protocol: "TCP",
					Port:     5050,
					NodePort: 31619,
				},
			},
		},
	}
	svcObj, err := uconverter(svc)
	if err != nil {
		return nil, nil, err
	}
	objs = append(objs, svcObj)

	if _, err := ap.Apply(ctx, objs); err != nil {
		return nil, nil, err
	}

	// Get PodList
	set := labels.Set(svc.Spec.Selector)
	pods := &corev1.PodList{}
	if err := mgr.Client().List(ctx, pods, &client.ListOptions{
		LabelSelector: set.AsSelector(),
	}); err != nil {
		return nil, nil, err
	}

	// Copy payload to all pods
	errs, ctx := errgroup.WithContext(ctx)

	for _, pod := range pods.Items {
		p := pod
		errs.Go(func() error {
			bin, err := s.payload.Bin()
			if err != nil {
				return nil
			}
			defer bin.Close()

			target := k8s.Target{Name: p.Name, Namespace: p.Namespace, Container: p.Spec.Containers[0].Name}
			l.Info().Str("pod", p.Name).Msgf("copying ripfs to seed pod")
			if err := s.copy(bin, target, "ripfs"); err != nil {
				return err
			}
			return nil
		})
	}

	if err := errs.Wait(); err != nil {
		return nil, nil, err
	}

	// Return PodList containing all the seed pods
	return pods, objs, nil
}

// copy will copy contents to a file within a pod
func (s *seeder) copy(f fs.File, target k8s.Target, dest string) error {
	var (
		wg             = sync.WaitGroup{}
		reader, writer = io.Pipe()
		errc           = make(chan error, 1)
	)
	wg.Add(1)
	defer close(errc)

	// build a tgz copy writer
	tgzStream := func(w io.Writer) error {
		tw := tar.NewWriter(w)
		defer tw.Close()

		fi, err := f.Stat()
		if err != nil {
			return err
		}

		if err := tw.WriteHeader(&tar.Header{
			Name: dest,
			Mode: 0777,
			Size: fi.Size(),
		}); err != nil {
			return err
		}

		_, err = io.Copy(tw, f)
		return err
	}

	go func() {
		defer writer.Close()
		errc <- tgzStream(writer)
		wg.Done()
	}()

	exec, err := s.executor(target, []string{"/ripfs/bin/busybox", "sh", "-c", "tar xvf - -C /ripfs/bin/"})
	if err != nil {
		return err
	}

	if err := exec.Stream(remotecommand.StreamOptions{
		Stdin:  reader,
		Stdout: io.Discard,
		Stderr: io.Discard,
		Tty:    false,
	}); err != nil {
		return err
	}

	wg.Wait()
	return <-errc
}

func (s *seeder) exec(target k8s.Target, cmd []string) error {
	exec, err := s.executor(target, cmd)
	if err != nil {
		return err
	}

	if err := exec.Stream(remotecommand.StreamOptions{
		Stdin:  os.Stdin,
		Stdout: io.Discard,
		Stderr: io.Discard,
		Tty:    false,
	}); err != nil {
		return err
	}

	return nil
}

func (s *seeder) executor(target k8s.Target, cmd []string) (remotecommand.Executor, error) {
	coreclient, err := corev1client.NewForConfig(s.kcfg)
	if err != nil {
		return nil, err
	}

	req := coreclient.RESTClient().
		Post().
		Namespace(target.Namespace).
		Resource("pods").
		Name(target.Name).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: target.Container,
			Command:   cmd,
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
			TTY:       false,
		}, scheme.ParameterCodec)

	return remotecommand.NewSPDYExecutor(s.kcfg, "POST", req.URL())
}
