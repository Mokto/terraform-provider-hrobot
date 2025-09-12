# Test configuration to verify auction order matches your working curl command
resource "hrobot_server_auction_order" "test" {
  product_id = 2783507

  authorized_key_fingerprints = [
    "34:f7:70:9f:16:82:57:80:e8:90:5f:88:48:a7:8e:a9"
  ]

  addons = ["primary_ipv4"]

  # Set test = true for dry run to avoid actually ordering
  test = true
}

output "auction_order_details" {
  value = {
    transaction_id = hrobot_server_auction_order.test.transaction_id
    status         = hrobot_server_auction_order.test.status
    server_number  = hrobot_server_auction_order.test.server_number
    server_ip      = hrobot_server_auction_order.test.server_ip
  }
}
