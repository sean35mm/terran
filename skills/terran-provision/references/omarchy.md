# Omarchy and other Arch Linux

Every command below needs the user's approval before you run it. Commands that need `sudo` are run by you only after the user approves that exact line.

## Base tools

```sh
sudo pacman -S --needed base-devel git openssh tailscale
```

mise: check `command -v mise` first; Omarchy often has it. If missing, `sudo pacman -S --needed mise`, or the vendor's release installer (download, read, run the local file) if the package is unavailable.

mise then installs every CLI tool from the config Terran applies (`mise install`). Do not install those tools with pacman or Homebrew.

`curl`, `tar`, and `sha256sum` are in the base system. `go` (1.24+) is only needed to build Terran from source; mise can provide it.

## Tailscale

```sh
sudo systemctl enable --now tailscaled
sudo tailscale up --ssh
```

`tailscale up` prints a login URL; the user opens it and signs in. The machine name on the tailnet should equal the Command Center name (`sudo tailscale set --hostname <name>`). `--ssh` enables Tailscale SSH so the controlling machine can reach it without managing keys.

## Terran location

`install.sh` puts the binary at `$HOME/.local/bin/terran`. Fleet status runs `~/.local/bin/terran` over SSH, so keep it there. Non-interactive SSH sessions must find it by that path; Terran calls it by the relative path `.local/bin/terran`.

## Fleet access (stage 9)

- `sudo tailscale up --ssh` (above). Restrict Tailscale ACLs to the user's own devices.
- Test from the controlling machine: `ssh <name> .local/bin/terran version --json`.
- Add the machine to `command-centers.json` in the overlay with `"platform": "linux"`.

## Platform notes

- Items tagged `platforms: ["darwin"]` are `excluded` here; explain them, do not try to force them.
- Omarchy manages parts of its own desktop configuration. Terran only writes its fixed destinations; leave everything else to the user.
