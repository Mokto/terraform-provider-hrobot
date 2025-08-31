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
