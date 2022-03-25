package k8s

import (
	"context"
	"io"
	"net/http"
	"os"

	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

type Target struct {
	Name      string
	Namespace string
	Container string
}

// Tunneler defines a desired port-forward
type Tunneler struct {
	restConfig *rest.Config
	client     *corev1client.CoreV1Client
}

func NewTunneler(restConfig *rest.Config) (*Tunneler, error) {
	coreclient, err := corev1client.NewForConfig(restConfig)
	if err != nil {
		return nil, err
	}

	return &Tunneler{
		restConfig: restConfig,
		client:     coreclient,
	}, nil
}

// Tunnel
// ports = <host>:<container>
func (t *Tunneler) Tunnel(ctx context.Context, pod Target, ports []string) (*portforward.PortForwarder, error) {
	req := t.client.RESTClient().
		Post().
		Resource("pods").
		Namespace(pod.Namespace).
		Name(pod.Name).
		SubResource("portforward")

	transport, upgrader, err := spdy.RoundTripperFor(t.restConfig)
	if err != nil {
		return nil, err
	}

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", req.URL())

	var (
		stopCh  = make(chan struct{})
		readyCh = make(chan struct{})
		errCh   = make(chan error)
	)
	// defer close(stopCh)

	forwarder, err := portforward.New(dialer, ports, stopCh, readyCh, io.Discard, os.Stderr)
	if err != nil {
		return nil, err
	}

	go func() {
		errCh <- forwarder.ForwardPorts()
	}()

	select {
	case err = <-errCh:
		return nil, err
	case <-forwarder.Ready:
	}

	return forwarder, nil
}
