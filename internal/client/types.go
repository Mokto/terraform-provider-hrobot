package client

type Transaction struct {
	ID           string `json:"id"`
	Date         string `json:"date"`
	Status       string `json:"status"` // "in process" | "ready" | "cancelled"
	ServerNumber *int   `json:"server_number"`
	ServerIP     string `json:"server_ip"`
}
type transactionEnv struct {
	Transaction Transaction `json:"transaction"`
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

type apiErr struct {
	Error struct {
		Status  int    `json:"status"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}
