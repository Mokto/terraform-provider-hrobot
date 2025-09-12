package client

import (
	"encoding/json"
)

type Product struct {
	ID          int      `json:"id"`
	Name        string   `json:"name"`
	Description []string `json:"description"`
	Traffic     string   `json:"traffic"`
	Location    []string `json:"location"`
}

// UnmarshalJSON custom unmarshaling for Product to handle location as either string or []string
func (p *Product) UnmarshalJSON(data []byte) error {
	type Alias Product
	aux := &struct {
		Location interface{} `json:"location"`
		*Alias
	}{
		Alias: (*Alias)(p),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	// Handle location field - can be string or []string
	switch v := aux.Location.(type) {
	case string:
		if v != "" {
			p.Location = []string{v}
		} else {
			p.Location = []string{}
		}
	case []string:
		p.Location = v
	case []interface{}:
		// Handle case where it's an array of mixed types
		p.Location = make([]string, len(v))
		for i, item := range v {
			if str, ok := item.(string); ok {
				p.Location[i] = str
			}
		}
	default:
		p.Location = []string{}
	}

	return nil
}

type Transaction struct {
	ID           string   `json:"id"`
	Date         string   `json:"date"`
	Status       string   `json:"status"` // "in process" | "ready" | "cancelled"
	ServerNumber *int     `json:"server_number"`
	ServerIP     string   `json:"server_ip"`
	Product      *Product `json:"-"` // Handle with custom unmarshaling
	ProductID    int      `json:"-"` // Store product ID when it's an integer
	Addons       []string `json:"addons,omitempty"`
}

// UnmarshalJSON custom unmarshaling for Transaction to handle product as either string or object
func (t *Transaction) UnmarshalJSON(data []byte) error {
	type Alias Transaction
	aux := &struct {
		Product interface{} `json:"product,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(t),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	// Handle product field - can be integer (product ID) or object (Product struct)
	switch v := aux.Product.(type) {
	case float64:
		// JSON numbers are unmarshaled as float64
		t.ProductID = int(v)
		t.Product = nil
	case int:
		t.ProductID = v
		t.Product = nil
	case map[string]interface{}:
		// Convert back to JSON and unmarshal as Product
		productJSON, err := json.Marshal(v)
		if err == nil {
			var product Product
			if err := json.Unmarshal(productJSON, &product); err == nil {
				t.Product = &product
				t.ProductID = product.ID
			}
		}
	case nil:
		t.Product = nil
		t.ProductID = 0
	}

	return nil
}

type transactionEnv struct {
	Transaction Transaction `json:"transaction"`
}

type productEnv struct {
	Product Product `json:"product"`
}

type productListEnv struct {
	Products []Product `json:"product"`
}

type Rescue struct {
	ServerIP       string `json:"server_ip"`
	Active         bool   `json:"active"`
	Password       string `json:"password"`
	AuthorizedKeys []struct {
		Key struct {
			Fingerprint string `json:"fingerprint"`
		} `json:"key"`
	} `json:"authorized_key"`
}
type rescueEnv struct {
	Rescue Rescue `json:"rescue"`
}

type VSwitch struct {
	ID   int    `json:"id"`
	VLAN int    `json:"vlan"`
	Name string `json:"name"`
}

type vswitchEnv struct {
	VSwitch VSwitch `json:"vswitch"`
}

type vswitchListEnv struct {
	VSwitches []VSwitch `json:"vswitch"`
}

type Server struct {
	ServerNumber int    `json:"server_number"`
	ServerName   string `json:"server_name"`
	ServerIP     string `json:"server_ip"`
	Status       string `json:"status"`
	Product      string `json:"product"`
	Location     string `json:"location"`
	// Add other fields as needed based on Hetzner API response
}

type serversResponse struct {
	Server []Server `json:"server"`
}

type apiErr struct {
	Error struct {
		Status  int    `json:"status"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}
