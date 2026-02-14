package hub

// Options holds configuration for the hub server.
type Options struct {
	DataDir               string
	ListenAddr            string
	Kubeconfig            string
	ExternalKCPKubeconfig string
	DexIssuerURL          string
	DexClientID           string
	DexClientSecret       string
	ServingCertFile       string
	ServingKeyFile        string
	HubExternalURL        string
	DevMode               bool
}

// NewOptions returns default Options.
func NewOptions() *Options {
	return &Options{
		DataDir:        "/tmp/kedge-data",
		ListenAddr:     ":8443",
		HubExternalURL: "https://localhost:8443",
	}
}
