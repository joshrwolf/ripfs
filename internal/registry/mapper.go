package registry

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/go-containerregistry/pkg/name"
	files "github.com/ipfs/go-ipfs-files"
	iface "github.com/ipfs/interface-go-ipfs-core"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"

	"github.com/joshrwolf/ripfs/internal/consts"
)

// CidMapper is anything that can resolve oci references to IPFS CIDs
type CidMapper interface {
	Resolve(ctx context.Context, reference string) (string, error)
}

// Updater is anything that can update the CidMapper
type Updater interface {
	Update(ctx context.Context) error
}

// Fetcher is anything that can fetch the ipns cid
type Fetcher interface {
	Fetch(ctx context.Context) (string, error)
}

type SecretFetcher struct {
	KCfg    *rest.Config
	Key     types.NamespacedName
	IpnsKey string
}

func NewSecretFetcher(kcfg *rest.Config, key types.NamespacedName) *SecretFetcher {
	return &SecretFetcher{
		KCfg: kcfg,
		Key:  key,

		// TODO: Decouple/make this configurable
		IpnsKey: consts.CidMapperSecretKey,
	}
}

func (f SecretFetcher) Fetch(ctx context.Context) (string, error) {
	c, err := corev1client.NewForConfig(f.KCfg)
	if err != nil {
		return "", err
	}

	s, err := c.Secrets(f.Key.Namespace).Get(ctx, f.Key.Name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	p, ok := s.Data[f.IpnsKey]
	if !ok {
		return "", fmt.Errorf("%s not found in secret %s", f.IpnsKey, s.GetName())
	}
	return string(p), nil
}

type IpnsCidMapper struct {
	client  iface.CoreAPI
	fetcher Fetcher
}

func NewIpfsCidMapper(client iface.CoreAPI, f Fetcher) *IpnsCidMapper {
	return &IpnsCidMapper{
		client:  client,
		fetcher: f,
	}
}

func (m *IpnsCidMapper) Resolve(ctx context.Context, reference string) (string, error) {
	mapper, err := m.fetch(ctx)
	if err != nil {
		return "", fmt.Errorf("unable to retrieve cid mapper")
	}

	ref, err := name.ParseReference(reference)
	if err != nil {
		return "", err
	}

	cid, ok := mapper[ref.Name()]
	if !ok {
		return "", fmt.Errorf("cid does not exist for reference %s", ref.Name())
	}

	return cid, nil
}

func (m *IpnsCidMapper) fetch(ctx context.Context) (map[string]string, error) {
	if !m.peered(ctx) {
		return nil, fmt.Errorf("swarm not initialized yet, ipns cannot exist")
	}

	cid, err := m.fetcher.Fetch(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching ipns cid: %v", err)
	}

	p, err := m.client.Name().Resolve(ctx, cid)
	if err != nil {
		return nil, err
	}

	nd, err := m.client.Unixfs().Get(ctx, p)
	if err != nil {
		return nil, err
	}

	f, ok := nd.(files.File)
	if !ok {
		return nil, fmt.Errorf("expected a file for the index, didn't get that")
	}
	defer f.Close()

	cidMap := make(map[string]string)
	if err := json.NewDecoder(f).Decode(&cidMap); err != nil {
		return nil, err
	}

	return cidMap, nil
}

func (m *IpnsCidMapper) peered(ctx context.Context) bool {
	peers, err := m.client.Swarm().Peers(ctx)
	if err != nil {
		return false
	}

	return len(peers) > 0
}
