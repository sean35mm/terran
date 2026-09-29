# macOS (Darwin)

Every command below needs the user's approval before you run it.

## Base tools

1. Xcode Command Line Tools (provides `git`, `make`, compilers):

   ```sh
   xcode-select -p || xcode-select --install
   ```

   `--install` opens a system dialog; the user clicks Install and you wait until `xcode-select -p` succeeds.

2. mise. Prefer the vendor's release installer: download it, let the user read it, then run the local file. If the user already uses Homebrew, `brew install mise` is acceptable; Homebrew is otherwise only for GUI casks.

   ```sh
   command -v mise || echo "mise missing"
   ```

3. Tailscale. It is a GUI app: `brew install --cask tailscale` (a cask, so Homebrew is appropriate) or the user's preferred installer. The user signs in through the app. Set the machine name to the Command Center name in the Tailscale admin console or app settings.

4. `git`, `curl`, `tar`, and `shasum` ship with macOS and the Command Line Tools. `go` (1.24+) is only needed to build Terran from source; mise can provide it.

## Terran location

`install.sh` puts the binary at `$HOME/.local/bin/terran`. Fleet status runs `~/.local/bin/terran` over SSH, so keep it there. Add `$HOME/.local/bin` to `PATH` only with the user's approval.

## Fleet access (stage 9)

macOS has no Tailscale SSH server for the app builds, so use Remote Login:

- The user enables System Settings > General > Sharing > Remote Login (scoped to their own account).
- The controlling machine's public key goes in `~/.ssh/authorized_keys` on this machine. The user provides the public key; you never read or print private keys.
- `ssh` uses `BatchMode`, so key authentication must work non-interactively. Test with `ssh <name> true` from the controlling machine.
- Restrict Tailscale ACLs to the user's own devices where possible.

## Platform notes

- Items tagged `platforms: ["linux"]` are `excluded` here; explain them, do not try to force them.
- GUI applications (cask installs) belong in the overlay checklist, not in Terran.
