package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/mokto/terraform-provider-hrobot/internal/client"
)

type orderTxnData struct{ api *client.Client }
type orderTxnModel struct {
	ID           types.String `tfsdk:"transaction_id"`
	Status       types.String `tfsdk:"status"`
	ServerNumber types.Int64  `tfsdk:"server_number"`
	ServerIP     types.String `tfsdk:"server_ip"`
}

func NewDataOrderTransaction() datasource.DataSource { return &orderTxnData{} }

func (d *orderTxnData) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_order_transaction"
}

func (d *orderTxnData) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		Attributes: map[string]dschema.Attribute{
			"transaction_id": dschema.StringAttribute{Required: true},
			"status":         dschema.StringAttribute{Computed: true},
			"server_number":  dschema.Int64Attribute{Computed: true},
			"server_ip":      dschema.StringAttribute{Computed: true},
		},
	}
}

func (d *orderTxnData) Configure(_ context.Context, req datasource.ConfigureRequest, _ *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	d.api = req.ProviderData.(*client.Client)
}

func (d *orderTxnData) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var in orderTxnModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &in)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tx, err := d.api.GetOrderTransaction(in.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("read transaction", err.Error())
		return
	}

	out := orderTxnModel{
		ID:       in.ID,
		Status:   types.StringValue(tx.Status),
		ServerIP: types.StringValue(tx.ServerIP),
	}
	if tx.ServerNumber != nil {
		out.ServerNumber = types.Int64Value(int64(*tx.ServerNumber))
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &out)...)
}
