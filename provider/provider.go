package provider

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/mokto/terraform-provider-hrobot/internal/client"
)

type hrobotProvider struct {
	version string
}

// ProviderData holds both client and cache manager for resources
type ProviderData struct {
	Client       *client.Client
	CacheManager *client.CacheManager
	UsedIPs      map[string]bool // Track assigned private IPs (10.1.0.x)
	IPMutex      sync.Mutex      // Protect IP assignment from race conditions
}

func New(version string) func() provider.Provider {
	return func() provider.Provider { return &hrobotProvider{version: version} }
}

type providerConfig struct {
	Username       types.String `tfsdk:"username"`
	Password       types.String `tfsdk:"password"`
	BaseURL        types.String `tfsdk:"base_url"`
	TimeoutSeconds types.Int64  `tfsdk:"timeout_seconds"`
}

func (p *hrobotProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "hrobot"
	resp.Version = p.version
}

func (p *hrobotProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"username": schema.StringAttribute{
				Optional:    true,
				Description: "Hetzner Robot webservice username (or HROBOT_USERNAME).",
			},
			"password": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Hetzner Robot webservice password (or HROBOT_PASSWORD).",
			},
			"base_url": schema.StringAttribute{
				Optional:    true,
				Description: "Robot base URL.",
				// Computed:    true,
			},
			"timeout_seconds": schema.Int64Attribute{
				Optional:    true,
				Description: "HTTP timeout seconds.",
				// Computed:    true,
			},
		},
	}
}

func (p *hrobotProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerConfig
	diags := req.Config.Get(ctx, &cfg)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	username := firstNonEmpty(cfg.Username.ValueString(), getenv("HROBOT_USERNAME"))
	password := firstNonEmpty(cfg.Password.ValueString(), getenv("HROBOT_PASSWORD"))
	if username == "" || password == "" {
		resp.Diagnostics.AddError("Missing credentials", "Set username/password or HROBOT_USERNAME/HROBOT_PASSWORD")
		return
	}
	base := cfg.BaseURL.ValueString()
	if base == "" {
		base = "https://robot-ws.your-server.de"
	}
	timeout := time.Duration(30) * time.Second
	if !cfg.TimeoutSeconds.IsNull() && !cfg.TimeoutSeconds.IsUnknown() && cfg.TimeoutSeconds.ValueInt64() > 0 {
		timeout = time.Duration(cfg.TimeoutSeconds.ValueInt64()) * time.Second
	}

	httpClient := &http.Client{Timeout: timeout}
	c := client.New(base, username, password, httpClient)
	cacheManager := client.NewCacheManager()

	providerData := &ProviderData{
		Client:       c,
		CacheManager: cacheManager,
		UsedIPs:      make(map[string]bool),
	}

	tflog.Info(ctx, "Configured hrobot provider", map[string]interface{}{"base_url": base})
	resp.DataSourceData = providerData
	resp.ResourceData = providerData
}

func (p *hrobotProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewResourceServerOrder,
		NewResourceServerAuctionOrder,
		NewResourceConfiguration,
		NewResourceVSwitch,
	}
}

func (p *hrobotProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewDataServers,
	}
}

// GetNextAvailableIP assigns the next available IP in the range 10.1.0.2 to 10.1.0.127
func (pd *ProviderData) GetNextAvailableIP() (string, error) {
	pd.IPMutex.Lock()
	defer pd.IPMutex.Unlock()

	// Range: 10.1.0.2 to 10.1.0.127
	for i := 2; i <= 127; i++ {
		ip := fmt.Sprintf("10.1.0.%d", i)
		if !pd.UsedIPs[ip] {
			pd.UsedIPs[ip] = true
			return ip, nil
		}
	}

	return "", fmt.Errorf("no available IP addresses in range 10.1.0.2-10.1.0.127")
}

// ReleaseIP marks an IP as available for reuse
func (pd *ProviderData) ReleaseIP(ip string) {
	pd.IPMutex.Lock()
	defer pd.IPMutex.Unlock()
	delete(pd.UsedIPs, ip)
}
