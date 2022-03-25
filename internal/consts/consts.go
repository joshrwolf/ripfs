package consts

type Const string

const (
	Name = "ripfs"

	CidMapperSecretName = Name + "-cid-mapper"
	CidMapperSecretKey  = "ipns-cid"

	ClusterConfigSecretName = Name + "-cluster-config"

	MutatorMWHConfigurationName = Name + "-webhook"
	MutatorCertsSecretName      = Name + "-webhook-certs"
	MutatorCAName               = Name + "-ca"
	MutatorCAOrg                = Name

	BootstrapServiceName      = Name + "-controller-manager"
	BootstrapLeaderElectionID = "48b90513.ripfs.dev"
)
