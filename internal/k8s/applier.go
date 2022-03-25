package k8s

import (
	"context"
	"time"

	"github.com/fluxcd/pkg/ssa"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type applier struct {
	manager *ssa.ResourceManager
	wopts   ssa.WaitOptions
}

func NewApplier(kcfg *rest.Config) (*applier, error) {
	mgr, err := NewManager(kcfg)
	if err != nil {
		return nil, err
	}

	return &applier{
		manager: mgr,
		wopts: ssa.WaitOptions{
			Interval: 2 * time.Second,
			Timeout:  1 * time.Minute,
		},
	}, nil
}

func (a *applier) Apply(ctx context.Context, objs []*unstructured.Unstructured) (*ssa.ChangeSet, error) {
	cobjs, objs := a.split(objs)

	_, err := a.applyAndWait(ctx, cobjs)
	if err != nil {
		return nil, err
	}

	return a.applyAndWait(ctx, objs)
}

func (a *applier) Get(ctx context.Context, key client.ObjectKey, obj client.Object) error {
	return a.manager.Client().Get(ctx, key, obj)
}

func (a *applier) Delete(ctx context.Context, objs []*unstructured.Unstructured) (*ssa.ChangeSet, error) {
	cs, err := a.manager.DeleteAll(ctx, objs, ssa.DefaultDeleteOptions())
	if err != nil {
		return nil, err
	}

	// if err := a.manager.WaitForSet(cs.ToObjMetadataSet(), a.wopts); err != nil {
	// 	return nil, err
	// }
	return cs, nil
}

func (a *applier) applyAndWait(ctx context.Context, objs []*unstructured.Unstructured) (*ssa.ChangeSet, error) {
	if len(objs) == 0 {
		return nil, nil
	}

	cs, err := a.manager.ApplyAll(ctx, objs, ssa.DefaultApplyOptions())
	if err != nil {
		return nil, err
	}

	if err := a.manager.WaitForSet(cs.ToObjMetadataSet(), a.wopts); err != nil {
		return nil, err
	}

	return cs, nil
}

// split will split objects into cluster objects and non cluster wide objects
func (a *applier) split(objects []*unstructured.Unstructured) ([]*unstructured.Unstructured, []*unstructured.Unstructured) {
	var (
		cobjs []*unstructured.Unstructured
		objs  []*unstructured.Unstructured
	)

	for _, obj := range objects {
		switch ssa.IsClusterDefinition(obj) {
		case true:
			cobjs = append(cobjs, obj)
		default:
			objs = append(objs, obj)
		}
	}
	return cobjs, objs
}
