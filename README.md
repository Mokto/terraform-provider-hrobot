# Terraform / OpenTofu Provider for Hetzner Robot

![build](https://github.com/mokto/terraform-provider-hrobot/actions/workflows/test.yml/badge.svg)
![release](https://github.com/mokto/terraform-provider-hrobot/actions/workflows/release.yml/badge.svg)

A [Terraform](https://www.terraform.io) / [OpenTofu](https://opentofu.org) provider to automate [Hetzner Robot](https://robot.hetzner.com/) bare-metal servers.

⚠️ **Status:** experimental, not affiliated with Hetzner.

---

## Features

- **Order servers** via `hrobot_server_order` resource (returns a transaction id).
- **Inspect order transactions** via `hrobot_order_transaction` data source (status, server number, IP).
- **Install operating systems** via `hrobot_installimage` resource:
  - activate Rescue
  - reboot
  - upload an `autosetup` file
  - run `installimage`
  - optional `post-install` or `ansible-pull`.

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

  # Use SSH keys already uploaded in Hetzner Robot
  authorized_key_fingerprints = [var.robot_key_fp]
}

output "order_transaction_id" {
  value = hrobot_server_order.ex101.transaction_id
}
```

At this stage, the order has been placed but the server may take hours/days to be “ready”.


#### Inspect the order transaction

```hcl
data "hrobot_order_transaction" "ex101" {
  transaction_id = hrobot_server_order.ex101.transaction_id
}

output "server_status" {
  value = data.hrobot_order_transaction.ex101.status
}

output "server_number" {
  value = data.hrobot_order_transaction.ex101.server_number
}

output "server_ip" {
  value = data.hrobot_order_transaction.ex101.server_ip
}
```

server_status  = "in process" or "ready"


#### Install an OS automatically

```hcl
resource "hrobot_installimage" "bootstrap" {
  # Only run installimage if the server is ready
  count         = data.hrobot_order_transaction.ex101.status == "ready" ? 1 : 0
  server_number = data.hrobot_order_transaction.ex101.server_number

  autosetup_content = <<-EOT
    HOSTNAME mynode1
    DRIVE1 /dev/nvme0n1
    SWRAID 0
    BOOTLOADER grub
    PART /boot  ext4 1024M
    PART /      ext4 all
    IMAGE /images/Ubuntu-2404-noble-64-minimal.tar.gz
    POST_INSTALL /root/post-install.sh
  EOT

  # Optional: run ansible-pull on first boot
  ansible_repo     = "https://github.com/yourorg/infra.git"
  ansible_playbook = "site.yml"
  ansible_extra    = "role=k3s_server env=prod"

  # Ensure Rescue accepts our SSH key
  rescue_authorized_key_fingerprints = [var.robot_key_fp]
}

output "installed_server_ip" {
  value = hrobot_installimage.bootstrap[0].server_ip
}
```

## License

MIT — see [LICENSE](LICENSE).
