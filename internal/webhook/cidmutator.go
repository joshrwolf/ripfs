package webhook

import (
	"context"
	"encoding/json"
	"net/http"
	"path"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/joshrwolf/ripfs/internal/registry"
)

var _ admission.Handler = (*podRelocatorHandler)(nil)

type podRelocatorHandler struct {
	decoder   *admission.Decoder
	cidMapper registry.CidMapper
	registry  string
}

func (h *podRelocatorHandler) Handle(ctx context.Context, req admission.Request) admission.Response {
	l := log.FromContext(ctx).WithName("mutator")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	pod := &corev1.Pod{}

	l.V(2).Info("decoding pod information")
	if err := h.decoder.Decode(req, pod); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	l.Info("handling mutator", "pod", pod.GetName())

	changed := make(map[string]string)
	for i, c := range pod.Spec.InitContainers {
		l.Info("processing init container", "container", c.Name, "image", c.Image)
		cid, err := h.cidMapper.Resolve(ctx, c.Image)
		if err != nil {
			l.Info("no matching cid found", "name", c.Name, "image", c.Image)
			continue
		}

		l.Info("resolved image reference to cid", "cid", cid, "image", c.Image)

		resolved := h.prefixIpfsRegistry(cid)
		pod.Spec.InitContainers[i].Image = resolved
		changed[c.Image] = resolved
	}

	for i, c := range pod.Spec.Containers {
		l.Info("processing container", "container", c.Name, "image", c.Image)
		cid, err := h.cidMapper.Resolve(ctx, c.Image)
		if err != nil {
			l.Info("no matching cid found", "name", c.Name, "image", c.Image)
			continue
		}

		l.Info("resolved image reference to cid", "cid", cid, "image", c.Image)

		resolved := h.prefixIpfsRegistry(cid)
		pod.Spec.Containers[i].Image = resolved
		changed[c.Image] = resolved
	}

	marshaledPod, err := json.Marshal(pod)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}

	if len(changed) > 0 {
		l.Info("successfully mutated pod images", "n", len(changed))
		return admission.PatchResponseFromRaw(req.Object.Raw, marshaledPod)
	} else {
		l.Info("no pod images matched, returning empty patch")
		return admission.Allowed("no image resolutions found")
	}
}

// prefixIpfsRegistry prefixes the registry (TODO: replace with a registry)
func (h *podRelocatorHandler) prefixIpfsRegistry(cid string) string {
	return path.Join(h.registry, cid)
}

func (h *podRelocatorHandler) InjectDecoder(d *admission.Decoder) error {
	h.decoder = d
	return nil
}

func AddPodRelocatorToManager(mgr manager.Manager, cm registry.CidMapper, registry string) error {
	wh := &admission.Webhook{
		Handler: &podRelocatorHandler{
			cidMapper: cm,
			registry:  registry,
		},
	}

	mgr.GetWebhookServer().Register("/mutate", &webhook.Admission{
		Handler: wh,
	})

	return nil
}
