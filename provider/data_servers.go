package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

type serversDataSource struct {
	providerData *ProviderData
}

type serversModel struct {
	Servers []serverModel `tfsdk:"servers"`
}

type serverModel struct {
	ServerNumber types.Int64  `tfsdk:"server_number"`
	ServerName   types.String `tfsdk:"server_name"`
	ServerIP     types.String `tfsdk:"server_ip"`
	Status       types.String `tfsdk:"status"`
	Product      types.String `tfsdk:"product"`
	Location     types.String `tfsdk:"location"`
}

func NewDataServers() datasource.DataSource {
	return &serversDataSource{}
}

func (d *serversDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_servers"
}

func (d *serversDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		Description: "Fetches all servers from Hetzner Robot using bulk API call for efficiency.",
		Attributes: map[string]dschema.Attribute{
			"servers": dschema.ListNestedAttribute{
				Computed: true,
				Description: "List of all servers",
				NestedObject: dschema.NestedAttributeObject{
					Attributes: map[string]dschema.Attribute{
						"server_number": dschema.Int64Attribute{
							Computed:    true,
							Description: "The server number",
						},
						"server_name": dschema.StringAttribute{
							Computed:    true,
							Description: "The server name",
						},
						"server_ip": dschema.StringAttribute{
							Computed:    true,
							Description: "The server IP address",
						},
						"status": dschema.StringAttribute{
							Computed:    true,
							Description: "The server status",
						},
						"product": dschema.StringAttribute{
							Computed:    true,
							Description: "The server product",
						},
						"location": dschema.StringAttribute{
							Computed:    true,
							Description: "The server location",
						},
					},
				},
			},
		},
	}
}

func (d *serversDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, _ *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	d.providerData = req.ProviderData.(*ProviderData)
}

func (d *serversDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	tflog.Info(ctx, "Fetching all servers using bulk API call")

	// Use the cache manager to get all servers (fetches once per apply)
	servers, err := d.providerData.CacheManager.GetServers(d.providerData.Client)
	if err != nil {
		resp.Diagnostics.AddError("Failed to fetch servers", err.Error())
		return
	}

	tflog.Info(ctx, "Successfully fetched servers", map[string]interface{}{
		"count": len(servers),
	})

	var state serversModel
	state.Servers = make([]serverModel, len(servers))

	for i, server := range servers {
		state.Servers[i] = serverModel{
			ServerNumber: types.Int64Value(int64(server.ServerNumber)),
			ServerName:   types.StringValue(server.ServerName),
			ServerIP:     types.StringValue(server.ServerIP),
			Status:       types.StringValue(server.Status),
			Product:      types.StringValue(server.Product),
			Location:     types.StringValue(server.Location),
		}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
