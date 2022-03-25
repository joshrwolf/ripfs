package cli

import (
	"context"
	"fmt"

	iface "github.com/ipfs/interface-go-ipfs-core"
	"github.com/open-policy-agent/cert-controller/pkg/rotator"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/joshrwolf/ripfs/controllers"
	"github.com/joshrwolf/ripfs/internal/consts"
	"github.com/joshrwolf/ripfs/internal/registry"
	"github.com/joshrwolf/ripfs/internal/webhook"
)

type managerCommandOpts struct {
	ipfsOpts *ipfsSharedOpts

	MetricsBindAddress   string
	ProbeAddress         string
	EnableLeaderElection bool
	Debug                bool
	CertsDir             string
	Namespace            string
	Registry             string
}

func newManagerCommand() *cobra.Command {
	o := &managerCommandOpts{ipfsOpts: &ipfsOpts}

	cmd := &cobra.Command{
		Use:   "manager",
		Short: "Start the ripfs manager",
		RunE: func(cmd *cobra.Command, args []string) error {
			return o.Run(cmd.Context())
		},
	}

	f := cmd.Flags()
	f.StringVar(&o.MetricsBindAddress, "metrics-bind-address", ":8000",
		"The address the metric endpoint binds to.")
	f.StringVar(&o.ProbeAddress, "health-probe-bind-address", ":8001",
		"The address the probe endpoint binds to.")
	f.StringVar(&o.CertsDir, "certs-dir", "/tmp/k8s-webhook-server/serving-certs/",
		"Path to where certificates will be generated or loaded.")
	f.BoolVar(&o.EnableLeaderElection, "leader-elect", false,
		"Toggle leader election.")
	f.StringVarP(&o.Registry, "registry", "r", "localhost:31609",
		"Hostname of the internal registry.")
	f.StringVar(&o.Namespace, "namespace", "",
		"If specified, scope all operations to the given namespace.")
	viper.BindPFlag("namespace", f.Lookup("namespace"))

	f.BoolVar(&o.Debug, "debug", false,
		"Toggle debug verbosity in logs")

	o.ipfsOpts.Flags(cmd)

	return cmd
}

func (o *managerCommandOpts) Run(ctx context.Context) error {
	var (
		scheme   = runtime.NewScheme()
		setupLog = ctrl.Log.WithName("setup")
		ns       = viper.GetString("namespace")
	)

	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	// +kubebuilder:scaffold:scheme

	ctrl.SetLogger(zap.New())

	ipfsDaemon, ipfsClient, ipfsRepo, err := o.ipfsOpts.initIpfs(true)
	if err != nil {
		return err
	}
	defer ipfsDaemon.Unlock()

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     o.MetricsBindAddress,
		Port:                   9443,
		CertDir:                o.CertsDir,
		HealthProbeBindAddress: o.ProbeAddress,
		LeaderElection:         o.EnableLeaderElection,
		LeaderElectionID:       consts.BootstrapLeaderElectionID,
		Namespace:              ns,
	})
	if err != nil {
		return fmt.Errorf("unable to start manager")
	}

	clusterSecretKey := types.NamespacedName{Name: consts.ClusterConfigSecretName, Namespace: ns}
	cidMapperSecretKey := types.NamespacedName{Name: consts.CidMapperSecretName, Namespace: ns}

	reconciler := &controllers.SecretReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),

		IpfsClient: ipfsClient,
		IpfsRepo:   ipfsRepo,

		ClusterSecretKey:   clusterSecretKey,
		CidMapperSecretKey: cidMapperSecretKey,
	}

	setupc := make(chan struct{})
	crotator := &rotator.CertRotator{
		SecretKey: types.NamespacedName{
			Name:      consts.MutatorCertsSecretName,
			Namespace: ns,
		},
		CertDir:        o.CertsDir,
		CAName:         consts.MutatorCAName,
		CAOrganization: consts.MutatorCAOrg,
		DNSName:        consts.BootstrapServiceName + "." + ns + ".svc",
		IsReady:        setupc,
		Webhooks: []rotator.WebhookInfo{
			{
				Name: consts.MutatorMWHConfigurationName,
				Type: rotator.Mutating,
			},
		},
	}

	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up health check: %v", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up ready check: %v", err)
	}

	// Register (and subsequently start) the webhook server certificate rotator
	if err := rotator.AddRotator(mgr, crotator); err != nil {
		return fmt.Errorf("setting up certificate rotator: %v", err)
	}

	// Register (and subsequently start) the embedded ipfs daemon as a runnable
	if err := mgr.Add(ipfsDaemon); err != nil {
		return fmt.Errorf("unable to set up ipfs: %v", err)
	}

	go o.setup(ctx, mgr, reconciler, ipfsClient, cidMapperSecretKey, setupc)

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		return fmt.Errorf("problem running manager: %v", err)
	}

	return nil
}

func (o *managerCommandOpts) setup(ctx context.Context, mgr ctrl.Manager, reconciler *controllers.SecretReconciler, ic iface.CoreAPI, cidMapperKey types.NamespacedName, setupf chan struct{}) error {
	l := log.FromContext(ctx)

	l.Info("waiting for certs to be generated and uploaded")
	select {
	case <-ctx.Done():
		return fmt.Errorf("timed out waiting for setup to finish")

	case <-setupf:
		l.Info("successfully generated and updated certs")
	}

	if err := reconciler.SetupWithManager(mgr); err != nil {
		return err
	}

	m := registry.NewIpfsCidMapper(ic, registry.NewSecretFetcher(ctrl.GetConfigOrDie(), cidMapperKey))

	l.Info("registering webhook server with manager")
	return webhook.AddPodRelocatorToManager(mgr, m, o.Registry)
}
