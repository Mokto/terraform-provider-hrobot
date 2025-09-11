# Terraform / OpenTofu Provider for Hetzner Robot

![build](https://github.com/mokto/terraform-provider-hrobot/actions/workflows/test.yml/badge.svg)
![release](https://github.com/mokto/terraform-provider-hrobot/actions/workflows/release.yml/badge.svg)

A [Terraform](https://www.terraform.io) / [OpenTofu](https://opentofu.org) provider to automate [Hetzner Robot](https://robot.hetzner.com/) bare-metal servers.

⚠️ **Status:** experimental, not affiliated with Hetzner.

---

## Features

- **Order servers** via `hrobot_server_order` resource (returns a transaction id).
- **Install operating systems** via `hrobot_configuration` resource:
  - activate Rescue
  - reboot
  - upload an `autosetup` file
  - run `installimage`
  - automatic LUKS encryption setup with keyfile-based auto-unlock

---

## Installation

### OpenTofu (recommended)

Add to your `.tf`:

```hcl
terraform {
  required_providers {
    hrobot = {
      source  = "mokto/hrobot"
      version = "~> 0.1"
    }
  }
}

provider "hrobot" {
  username = var.hrobot_user   # or HROBOT_USERNAME env
  password = var.hrobot_pass   # or HROBOT_PASSWORD env
}
```

#### Order a server

```hcl
resource "hrobot_server_order" "ex101" {
  product_id = "EX101"
  location   = "FSN1"

  addons = ["primary_ipv4"]

  # Use SSH keys already uploaded in Hetzner Robot
  authorized_key_fingerprints = [var.robot_key_fp]
}

output "order_transaction_id" {
  value = hrobot_server_order.ex101.transaction_id
}
```

At this stage, the order has been placed but the server may take hours/days to be “ready”.


server_status  = "in process" or "ready"


#### Configure a server

**Required Parameters:**
- `server_name`: Name for the server (used as hostname in autosetup)
- `server_ip`: The server's IP address
- `server_number`: Robot server number
- `arch`: Architecture for the OS image - "amd64" or "arm64"
- `cryptpassword`: Password for disk encryption
- `rescue_authorized_key_fingerprints`: SSH key fingerprints for rescue mode access

The `autosetup_content` is automatically generated with Ubuntu 24.04 Noble and the specified configuration. A comprehensive postinstall script for LUKS encryption setup is automatically included.

```hcl
resource "hrobot_configuration" "web_server" {
  server_number = hrobot_server_order.test.server_number  # Replace with your actual server number
  server_ip     = "1.2.3.4"  # Replace with your server's IP address
  server_name   = "web-server-01"
  count         = hrobot_server_order.test.status == "ready" ? 1 : 0

  # Required autosetup parameters
  arch          = "amd64"  # "amd64" or "arm64"
  cryptpassword = "your-secure-password"

  # SSH key fingerprints for rescue mode access
  rescue_authorized_key_fingerprints = [
    "your-ssh-key-fingerprint-1",
    "your-ssh-key-fingerprint-2"
  ]

}
```


```hcl
resource "hrobot_vswitch" "internal_network" {
  vlan = 4000
  name = "internal-network"
}
```

## License

MIT — see [LICENSE](LICENSE).
