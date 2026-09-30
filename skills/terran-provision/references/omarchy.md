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

## Fleet access (join the fleet)

- `sudo tailscale up --ssh` (above). Restrict Tailscale ACLs to the user's own devices.
- The tailnet policy must let fleet machines log in as this machine's user; that user goes in `command-centers.json` as `user`, with `"platform": "linux"`.
- Test from another Command Center: `ssh -o BatchMode=yes -o User=<user> <alias> .local/bin/terran version --json`.
- Then the `terran-fleet` procedure "Connect the fleet (SSH and Herdr)". This machine needs no `authorized_keys`; it still gets a fleet key so it can reach the Macs.

## Platform notes

- Items tagged `platforms: ["darwin"]` are `excluded` here; explain them, do not try to force them.
- Omarchy manages parts of its own desktop configuration. Terran only writes its fixed destinations; leave everything else to the user.
