package k8s

import (
	"github.com/fluxcd/pkg/ssa"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/cli-utils/pkg/kstatus/polling"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	"github.com/joshrwolf/ripfs/internal/consts"
)

func NewManager(kcfg *rest.Config) (*ssa.ResourceManager, error) {
	restmapper, err := apiutil.NewDynamicRESTMapper(kcfg)
	if err != nil {
		return nil, err
	}

	kubeclient, err := client.New(kcfg, client.Options{Mapper: restmapper})
	if err != nil {
		return nil, err
	}

	poller := polling.NewStatusPoller(kubeclient, restmapper, polling.Options{})

	mgr := ssa.NewResourceManager(kubeclient, poller, ssa.Owner{
		Field: consts.Name,
	})
	return mgr, nil
}
