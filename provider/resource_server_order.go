package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/mokto/terraform-provider-hrobot/internal/client"
)

type serverOrderResource struct {
	api *client.Client
}

type serverOrderModel struct {
	ID        types.String `tfsdk:"id"`
	ProductID types.String `tfsdk:"product_id"`
	Dist      types.String `tfsdk:"dist"`
	Location  types.String `tfsdk:"location"`
	Keys      types.List   `tfsdk:"authorized_key_fingerprints"`
	Password  types.String `tfsdk:"password"`
	Addons    types.List   `tfsdk:"addons"`
	Test      types.Bool   `tfsdk:"test"`

	TransactionID types.String `tfsdk:"transaction_id"`
	Status        types.String `tfsdk:"status"`
	ServerNumber  types.Int64  `tfsdk:"server_number"`
	ServerIP      types.String `tfsdk:"server_ip"`
}

func NewResourceServerOrder() resource.Resource { return &serverOrderResource{} }

func (r *serverOrderResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_server_order"
}

func (r *serverOrderResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = rschema.Schema{
		Description: "Manages a Hetzner Robot server order. When destroyed, the server will be scheduled for cancellation at the end of the billing period.",
		Attributes: map[string]rschema.Attribute{
			"product_id": rschema.StringAttribute{Required: true, Description: "Robot product id (e.g., EX101)"},
			"dist":       rschema.StringAttribute{Optional: true, Description: "Preinstall distribution label"},
			"location":   rschema.StringAttribute{Optional: true, Description: "FSN1 / NBG1 / HEL1"},
			"authorized_key_fingerprints": rschema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Authorized key fingerprints stored in Robot",
			},
			"password": rschema.StringAttribute{
				Optional: true, Sensitive: true,
				Description: "Root password alternative to keys",
			},
			"addons": rschema.ListAttribute{
				Optional: true, ElementType: types.StringType,
				Description: "Addon ids (e.g., primary_ipv4)",
			},
			"test": rschema.BoolAttribute{Optional: true, Description: "Dry-run order"},

			"transaction_id": rschema.StringAttribute{Computed: true},
			"status":         rschema.StringAttribute{Computed: true},
			"server_number":  rschema.Int64Attribute{Computed: true},
			"server_ip":      rschema.StringAttribute{Computed: true, Description: "The server's IP address (available when server is ready)"},
			"id":             rschema.StringAttribute{Computed: true},
		},
	}
}

func (r *serverOrderResource) Configure(_ context.Context, req resource.ConfigureRequest, _ *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	r.api = req.ProviderData.(*client.Client)
}

func (r *serverOrderResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan serverOrderModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	keys := mustStringSlice(ctx, resp, plan.Keys)
	addons := mustStringSlice(ctx, resp, plan.Addons)
	if resp.Diagnostics.HasError() {
		return
	}

	tx, err := r.api.OrderServer(client.OrderParams{
		ProductID: plan.ProductID.ValueString(),
		Dist:      optString(plan.Dist),
		Location:  optString(plan.Location),
		Password:  optString(plan.Password),
		Keys:      keys,
		Addons:    addons,
		Test:      !plan.Test.IsNull() && plan.Test.ValueBool(),
	})
	if err != nil {
		resp.Diagnostics.AddError("order failed", err.Error())
		return
	}

	state := plan
	state.ID = types.StringValue(tx.ID)
	state.TransactionID = types.StringValue(tx.ID)
	state.Status = types.StringValue(tx.Status)
	if tx.ServerNumber != nil {
		state.ServerNumber = types.Int64Value(int64(*tx.ServerNumber))
	} else {
		state.ServerNumber = types.Int64Null()
	}
	state.ServerIP = types.StringValue(tx.ServerIP)

	tflog.Info(ctx, "created order", map[string]interface{}{"transaction_id": tx.ID})
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *serverOrderResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state serverOrderModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ID.IsNull() || state.ID.ValueString() == "" {
		resp.State.RemoveResource(ctx)
		return
	}

	tx, err := r.api.GetOrderTransaction(state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("read transaction", err.Error())
		return
	}

	state.Status = types.StringValue(tx.Status)
	if tx.ServerNumber != nil {
		state.ServerNumber = types.Int64Value(int64(*tx.ServerNumber))
	} else {
		state.ServerNumber = types.Int64Null()
	}
	state.ServerIP = types.StringValue(tx.ServerIP)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *serverOrderResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// immutable; re-create on changes
	var plan serverOrderModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.AddAttributeError(
		path.Root("product_id"),
		"Update Not Supported",
		"Order is immutable; destroy and re-create if needed.",
	)
}

func (r *serverOrderResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state serverOrderModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// If we have a server number, schedule cancellation at the end of billing period
	if !state.ServerNumber.IsNull() && !state.ServerNumber.IsUnknown() {
		serverNumber := int(state.ServerNumber.ValueInt64())
		
		// Schedule cancellation at the end of the billing period (empty cancelDate means end of period)
		err := r.api.CancelServer(serverNumber, "")
		if err != nil {
			// Log the error but don't fail the delete operation
			// The server will be removed from Terraform state regardless
			tflog.Warn(ctx, "Failed to schedule server cancellation", map[string]interface{}{
				"server_number": serverNumber,
				"error":         err.Error(),
			})
			
			resp.Diagnostics.AddWarning(
				"Server Cancellation Failed",
				fmt.Sprintf("Failed to schedule cancellation for server %d: %s. Please cancel the server manually through the Hetzner Robot interface to stop billing.", serverNumber, err.Error()),
			)
		} else {
			tflog.Info(ctx, "Scheduled server cancellation", map[string]interface{}{
				"server_number": serverNumber,
			})
			
			resp.Diagnostics.AddWarning(
				"Server Cancellation Scheduled",
				fmt.Sprintf("Server %d has been scheduled for cancellation at the end of the billing period. The server will remain active until then.", serverNumber),
			)
		}
	} else {
		// No server number available, just remove from state
		tflog.Info(ctx, "Removing server order from state (no server number available)")
		
		resp.Diagnostics.AddWarning(
			"Manual Cancellation May Be Required",
			"The server order has been removed from Terraform state, but if a server was created, you may need to cancel it manually through the Hetzner Robot interface.",
		)
	}
}

// helpers
func mustStringSlice(ctx context.Context, resp *resource.CreateResponse, l types.List) []string {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	var out []string
	resp.Diagnostics.Append(l.ElementsAs(ctx, &out, false)...)
	return out
}
func optString(v types.String) *string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	s := v.ValueString()
	return &s
}
