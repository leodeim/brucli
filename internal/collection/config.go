package collection

type Config struct {
	Proxy              Proxy               `yaml:"proxy"`
	ClientCertificates []ClientCertificate `yaml:"clientCertificates"`
	Protobuf           struct {
		ProtoFiles  []ConfigPath `yaml:"protoFiles"`
		ImportPaths []ConfigPath `yaml:"importPaths"`
	} `yaml:"protobuf"`
}

type ConfigPath struct {
	Path     string `yaml:"path"`
	Type     string `yaml:"type"`
	Disabled bool   `yaml:"disabled"`
	Enabled  *bool  `yaml:"enabled"`
}

type Proxy struct {
	Disabled bool `yaml:"disabled"`
	Inherit  bool `yaml:"inherit"`
	Config   struct {
		Protocol string `yaml:"protocol"`
		Hostname string `yaml:"hostname"`
		Port     string `yaml:"port"`
		Bypass   string `yaml:"bypassProxy"`
		Auth     struct {
			Disabled bool   `yaml:"disabled"`
			Username string `yaml:"username"`
			Password string `yaml:"password"`
		} `yaml:"auth"`
	} `yaml:"config"`
}

type ClientCertificate struct {
	Domain              string `yaml:"domain"`
	Type                string `yaml:"type"`
	Disabled            bool   `yaml:"disabled"`
	CertificateFilePath string `yaml:"certificateFilePath"`
	PrivateKeyFilePath  string `yaml:"privateKeyFilePath"`
	PKCS12FilePath      string `yaml:"pkcs12FilePath"`
	Passphrase          string `yaml:"passphrase"`
	// CLI and legacy .bru configuration names.
	CertFilePath string `yaml:"certFilePath"`
	KeyFilePath  string `yaml:"keyFilePath"`
	PFXFilePath  string `yaml:"pfxFilePath"`
	BaseDir      string `yaml:"-"`
}
