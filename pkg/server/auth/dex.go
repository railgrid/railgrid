package auth

// DexConfig holds Dex-specific configuration.
type DexConfig struct {
	// Connectors defines the identity provider connectors.
	Connectors []ConnectorConfig `json:"connectors,omitempty"`
}

// ConnectorConfig defines an identity provider connector.
type ConnectorConfig struct {
	Type   string `json:"type"`   // github, google, ldap, etc.
	ID     string `json:"id"`
	Name   string `json:"name"`
	Config map[string]interface{} `json:"config,omitempty"`
}
