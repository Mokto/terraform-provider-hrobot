package provider

import (
	"context"
	"net/http"
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

	tflog.Info(ctx, "Configured hrobot provider", map[string]interface{}{"base_url": base})
	resp.DataSourceData = c
	resp.ResourceData = c
}

func (p *hrobotProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewResourceServerOrder,
		NewResourceConfiguration,
		NewResourceVSwitch,
	}
}

func (p *hrobotProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewDataOrderTransaction,
	}
}
