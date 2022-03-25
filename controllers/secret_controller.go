/*
Copyright 2022.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"
	"reflect"
	"time"

	files "github.com/ipfs/go-ipfs-files"
	"github.com/ipfs/go-ipfs/repo"
	iface "github.com/ipfs/interface-go-ipfs-core"
	"github.com/ipfs/interface-go-ipfs-core/options"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/joshrwolf/ripfs/internal/consts"
)

// SecretReconciler reconciles a Secret object
type SecretReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	IpfsClient iface.CoreAPI
	IpfsRepo   repo.Repo

	ClusterSecretKey   types.NamespacedName
	CidMapperSecretKey types.NamespacedName
}

// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core,resources=secrets/finalizers,verbs=update

// TODO: Make these their own SA
// +kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=mutatingwebhookconfigurations,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Secret object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.0/pkg/reconcile
func (r *SecretReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	switch req.NamespacedName {
	case r.ClusterSecretKey:
		return r.reconcileClusterConfig(ctx)

	case r.CidMapperSecretKey:
		return r.reconcileCidMapper(ctx)

	}

	// Ensure cluster config secret
	if err := r.ensureSecret(ctx, r.ClusterSecretKey); err != nil {
		return ctrl.Result{}, err
	}

	// Ensure cid mapper secret
	if err := r.ensureSecret(ctx, r.CidMapperSecretKey); err != nil {
		return ctrl.Result{}, err
	}

	// l.Info("doin it", "req", req)
	// if _, err := ctrl.CreateOrUpdate(ctx, r.Client, cfg, func() error {
	// 	return cfgBuilder.Build(cfg)
	// }); err != nil {
	// 	return ctrl.Result{}, nil
	// }

	return ctrl.Result{}, nil
}

func (r *SecretReconciler) ensureSecret(ctx context.Context, n types.NamespacedName) error {
	var (
		l = log.FromContext(ctx)
		s = &corev1.Secret{}
	)

	if err := r.Get(ctx, n, s); errors.IsNotFound(err) {
		l.Info("creating new secret", "name", n)

		s.Name = n.Name
		s.Namespace = n.Namespace

		if err := r.Create(ctx, s, &client.CreateOptions{}); err != nil {
			return err
		}

	} else if err != nil {
		return err
	}
	return nil
}

func (r *SecretReconciler) reconcileClusterConfig(ctx context.Context) (ctrl.Result, error) {
	obj := &corev1.Secret{}
	if err := r.Get(ctx, r.ClusterSecretKey, obj); err != nil {
		return ctrl.Result{}, err
	}

	swarmKey, err := r.IpfsRepo.SwarmKey()
	if err != nil {
		return ctrl.Result{}, err
	}

	cfg, err := r.IpfsRepo.Config()
	if err != nil {
		return ctrl.Result{}, err
	}

	bootstrapPeer := fmt.Sprintf("/dns/%s.%s.svc/tcp/4001/ipfs/%s", consts.BootstrapServiceName, obj.GetNamespace(), cfg.Identity.PeerID)

	want := make(map[string][]byte)
	want["swarm.key"] = swarmKey
	want["bootstrap-peers"] = []byte(bootstrapPeer)

	if reflect.DeepEqual(want, obj.Data) {
		// Nothing to do here!
		return ctrl.Result{}, nil
	}

	obj.Data = want

	if err := r.Update(ctx, obj, &client.UpdateOptions{}); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *SecretReconciler) reconcileCidMapper(ctx context.Context) (ctrl.Result, error) {
	obj := &corev1.Secret{}
	if err := r.Get(ctx, r.CidMapperSecretKey, obj); err != nil {
		return ctrl.Result{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	peers, err := r.IpfsClient.Swarm().Peers(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}

	if len(peers) == 0 {
		// Try again
		return ctrl.Result{Requeue: true}, err
	}

	if _, ok := obj.Data[consts.CidMapperSecretKey]; ok {
		return ctrl.Result{}, nil
	}

	obj.Data = make(map[string][]byte)

	// Create a new one
	f := files.NewBytesFile([]byte(`{}`))
	p, err := r.IpfsClient.Unixfs().Add(ctx, f, options.Unixfs.Pin(true), options.Unixfs.CidVersion(1))
	if err != nil {
		return ctrl.Result{}, err
	}

	e, err := r.IpfsClient.Name().Publish(ctx, p, func(s *options.NamePublishSettings) error {
		s.AllowOffline = true
		return nil
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	obj.Data[consts.CidMapperSecretKey] = []byte(e.Name())

	if err := r.Update(ctx, obj, &client.UpdateOptions{}); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SecretReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Secret{}).
		Complete(r)
}
