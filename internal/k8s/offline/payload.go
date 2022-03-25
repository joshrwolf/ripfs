package offline

import (
	"context"
	"embed"
	"fmt"
	"io"
	"io/fs"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/mholt/archiver/v4"
	"github.com/spf13/afero"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/rand"
)

//go:embed payload
var busybox embed.FS

type Payload interface {
	// Deployment returns a deployment with the appropriate image and configmap mounts
	Deployment(image string, nodeName string, selector map[string]string) (*appsv1.Deployment, error)

	// Bin returns the payloads executable
	Bin() (fs.File, error)
}

type LayoutPayload struct {
	Path string

	layout   layout.Path
	platform *v1.Platform
}

func (l LayoutPayload) Image() (v1.Image, error) {
	return nil, nil
}

func (l LayoutPayload) Deployment(image string, nodeName string, selector map[string]string) (*appsv1.Deployment, error) {
	var (
		gen      = rand.String(5)
		perm     = int32(int64(0777))
		replicas = int32(int64(1))
	)

	dep := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "seeder-" + gen,
			Namespace: "default",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: selector,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: selector,
				},
				Spec: corev1.PodSpec{
					NodeName: nodeName,
					Containers: []corev1.Container{
						{
							Name:    "seeder",
							Image:   image,
							Command: []string{"/ripfs/bin/busybox", "sh", "-c", "tail -f /dev/null"},
							Ports: []corev1.ContainerPort{
								{
									Name:          "tcp-registry",
									ContainerPort: 5050,
									Protocol:      "TCP",
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "bin",
									MountPath: "/ripfs/bin",
								},
								{
									Name:      "busybox",
									MountPath: "/ripfs/bin/busybox",
									SubPath:   "busybox",
								},
								{
									Name:      "ipfs-data",
									MountPath: "/data/ipfs",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "bin",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
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
						{
							Name: "ipfs-data",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
		},
	}

	return dep, nil
}

func (l LayoutPayload) Bin() (fs.File, error) {
	idx, err := l.layout.ImageIndex()
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
				if m.Platform.Equals(*l.platform) {
					img, err = idx.Image(m.Digest)
					break
				}
			}
		}
	}

	if img == nil {
		return nil, fmt.Errorf("couldn't find payload")
	}

	// don't look
	layers, err := img.Layers()
	if err != nil {
		return nil, err
	}

	if len(layers) != 3 {
		return nil, fmt.Errorf("expected 3 layers, got %d", len(layers))
	}

	// TODO: This is an ugly way of getting a file out of an image, there are better ways to do this!
	format := archiver.CompressedArchive{
		Compression: archiver.Gz{},
		Archival:    archiver.Tar{},
	}

	rc, err := layers[2].Compressed()
	if err != nil {
		return nil, err
	}

	fsys := afero.NewMemMapFs()
	if err := format.Extract(context.Background(), rc, nil, func(ctx context.Context, f archiver.File) error {
		if f.NameInArchive != "/ko-app/ripfs" {
			return nil
		}

		rc, err := f.Open()
		if err != nil {
			return nil
		}
		defer rc.Close()

		binf, err := fsys.Create("ripfs")
		if err != nil {
			return err
		}

		if _, err := io.Copy(binf, rc); err != nil {
			return err
		}
		defer binf.Close()

		return nil
	}); err != nil {
		return nil, err
	}

	bin, err := fsys.Open("ripfs")
	if err != nil {
		return nil, err
	}

	return bin, nil
}

func NewLayoutPayload(path string) (*LayoutPayload, error) {
	l, err := layout.FromPath(path)
	if err != nil {
		return nil, err
	}

	return &LayoutPayload{
		Path:   path,
		layout: l,

		// TODO: Make this configurable
		platform: &v1.Platform{OS: "linux", Architecture: "amd64"},
	}, nil
}

var uconverter = func(obj interface{}) (*unstructured.Unstructured, error) {
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}

	u := &unstructured.Unstructured{}
	u.SetUnstructuredContent(content)
	return u, nil
}
