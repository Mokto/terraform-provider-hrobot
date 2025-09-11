package provider

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/mokto/terraform-provider-hrobot/internal/client"
)

type serverOrderResource struct {
	providerData *ProviderData
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

// Cache entry for transaction data
type transactionCacheEntry struct {
	transaction *client.Transaction
	lastUpdated time.Time
}

// JSON-serializable cache entry
type jsonCacheEntry struct {
	Transaction *client.Transaction `json:"transaction"`
	LastUpdated string              `json:"last_updated"`
}

// Global cache for transaction data to avoid hitting API rate limits
var (
	transactionCache = make(map[string]*transactionCacheEntry)
	cacheMutex       sync.RWMutex
	cacheExpiry      = 5 * time.Minute // Cache expires after 5 minutes
	cacheFile        = getCacheFilePath()
)

// getCacheFilePath returns the path to the cache file in the .cache directory
func getCacheFilePath() string {
	// Get the current working directory (should be the repository root)
	wd, err := os.Getwd()
	if err != nil {
		// Fallback to temp directory if we can't get working directory
		return filepath.Join(os.TempDir(), "terraform-provider-hrobot-cache.json")
	}

	// Create .cache directory if it doesn't exist
	cacheDir := filepath.Join(wd, ".cache")
	os.MkdirAll(cacheDir, 0755)

	return filepath.Join(cacheDir, "transaction-cache.json")
}

func NewResourceServerOrder() resource.Resource {
	// Load cache from disk on startup
	loadCacheFromDisk()
	return &serverOrderResource{}
}

// loadCacheFromDisk loads the cache from disk
func loadCacheFromDisk() {
	cacheMutex.Lock()
	defer cacheMutex.Unlock()

	data, err := os.ReadFile(cacheFile)
	if err != nil {
		// Cache file doesn't exist or can't be read, start with empty cache
		return
	}

	var diskCache map[string]*jsonCacheEntry
	if err := json.Unmarshal(data, &diskCache); err != nil {
		// Invalid cache file, start with empty cache
		return
	}

	// Only load non-expired entries
	now := time.Now()
	for id, jsonEntry := range diskCache {
		lastUpdated, err := time.Parse(time.RFC3339, jsonEntry.LastUpdated)
		if err != nil {
			continue // Skip invalid timestamp
		}

		if now.Sub(lastUpdated) <= cacheExpiry {
			transactionCache[id] = &transactionCacheEntry{
				transaction: jsonEntry.Transaction,
				lastUpdated: lastUpdated,
			}
		}
	}
}

// saveCacheToDisk saves the cache to disk
func saveCacheToDisk() {
	cacheMutex.RLock()
	defer cacheMutex.RUnlock()

	// Convert to JSON-serializable format
	jsonCache := make(map[string]*jsonCacheEntry)
	for id, entry := range transactionCache {
		jsonCache[id] = &jsonCacheEntry{
			Transaction: entry.transaction,
			LastUpdated: entry.lastUpdated.Format(time.RFC3339),
		}
	}

	data, err := json.Marshal(jsonCache)
	if err != nil {
		return
	}

	os.WriteFile(cacheFile, data, 0600)
}

// getCachedTransaction retrieves transaction from cache if available and not expired
func getCachedTransaction(id string) (*client.Transaction, bool) {
	cacheMutex.RLock()
	defer cacheMutex.RUnlock()

	entry, exists := transactionCache[id]
	if !exists {
		return nil, false
	}

	// Check if cache entry is expired
	if time.Since(entry.lastUpdated) > cacheExpiry {
		return nil, false
	}

	return entry.transaction, true
}

// setCachedTransaction stores transaction in cache
func setCachedTransaction(id string, transaction *client.Transaction) {
	cacheMutex.Lock()
	defer cacheMutex.Unlock()

	transactionCache[id] = &transactionCacheEntry{
		transaction: transaction,
		lastUpdated: time.Now(),
	}

	// Save to disk asynchronously
	go saveCacheToDisk()
}

// shouldRefreshTransaction determines if we need to refresh the transaction data
func shouldRefreshTransaction(transaction *client.Transaction) bool {
	if transaction == nil {
		return true
	}
	// Only refresh if status is "in process" - other statuses are final
	return transaction.Status == "in process"
}

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
	r.providerData = req.ProviderData.(*ProviderData)
}

func (r *serverOrderResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan serverOrderModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	keys := mustStringSliceCreate(ctx, resp, plan.Keys)
	addons := mustStringSliceCreate(ctx, resp, plan.Addons)
	if resp.Diagnostics.HasError() {
		return
	}

	tx, err := r.providerData.Client.OrderServer(client.OrderParams{
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

	// Cache the transaction data
	setCachedTransaction(tx.ID, tx)

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

	transactionID := state.ID.ValueString()

	// Try to get cached transaction first
	cachedTx, found := getCachedTransaction(transactionID)

	var tx *client.Transaction
	var err error

	// Determine if we need to refresh the data
	if found && !shouldRefreshTransaction(cachedTx) {
		// Use cached data - transaction is in final state
		tx = cachedTx
		tflog.Info(ctx, "Using cached transaction data", map[string]interface{}{
			"transaction_id": transactionID,
			"status":         tx.Status,
		})
	} else {
		// Make API call to get fresh data
		if found {
			tflog.Info(ctx, "Refreshing transaction data (status is in process)", map[string]interface{}{
				"transaction_id": transactionID,
				"cached_status":  cachedTx.Status,
			})
		} else {
			tflog.Info(ctx, "No cached data found, fetching transaction", map[string]interface{}{
				"transaction_id": transactionID,
			})
		}

		tx, err = r.providerData.Client.GetOrderTransaction(transactionID)
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		if err != nil {
			resp.Diagnostics.AddError("read transaction", err.Error())
			return
		}

		// Update cache with fresh data
		setCachedTransaction(transactionID, tx)
		tflog.Info(ctx, "Updated transaction cache", map[string]interface{}{
			"transaction_id": transactionID,
			"status":         tx.Status,
		})
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
	// Server order deletion is handled by the configuration resource
	// This resource only manages the order transaction, not server lifecycle
	tflog.Info(ctx, "server order resource deleted from state")
}

// helpers
func mustStringSliceCreate(ctx context.Context, resp *resource.CreateResponse, l types.List) []string {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	var out []string
	resp.Diagnostics.Append(l.ElementsAs(ctx, &out, false)...)
	return out
}
func mustStringSliceUpdate(ctx context.Context, resp *resource.UpdateResponse, l types.List) []string {
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
