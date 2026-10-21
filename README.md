# Cinnabar Portable Launcher

A small Windows launcher for [Cinnabar](https://github.com/cinnabar-mc/cinnabar) that keeps a portable copy up to date and runs it without admin rights.

The official installer puts Cinnabar in Program Files, which needs admin. This launcher unpacks the same official release into a folder next to itself instead.

## Use

1. Put `Cinnabar Launcher.exe` in any folder you can write to (Desktop, Downloads, a USB stick).
2. Run it. The first run downloads the latest official release into `app\` beside the launcher.
3. Every later run checks GitHub for a newer release, updates `app\` if there is one, then starts Cinnabar.

Windows SmartScreen may warn that the launcher is unsigned. Click "More info", then "Run anyway". This does not need admin.

## What it does

- Reads the latest release from the GitHub API (`/releases/latest`, so prereleases and nightlies are skipped).
- Downloads `Cinnabar-x64.msi` and checks it against the release's `SHA256SUMS.txt`.
- Unpacks the MSI's files without running the installer: it reads the File, Component, Directory and Media tables through `msi.dll`, pulls out the embedded cabinets, and extracts them with the built-in `expand.exe`.
- Swaps the new copy into `app\` and records the tag in `app\version.txt`.
- Starts `app\cinnabar.exe` with `CINNABAR_UPDATE_CHECK=0`, because the client's own updater installs the per-machine MSI and would ask for admin.

If GitHub can't be reached, it starts the copy you already have. If Cinnabar is running during an update, the update is skipped until the next launch.

Your sign-in, settings and worlds live in `%LOCALAPPDATA%\Cinnabar`, so deleting or moving the launcher folder doesn't touch them.

## Build

Needs Go 1.24 or newer. From any OS:

```sh
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "Cinnabar Launcher.exe" .
```

`rsrc_windows_amd64.syso` carries the icon and an `asInvoker` manifest, which stops Windows from guessing that the exe needs elevation. To regenerate it from `winres/`:

```sh
cd winres && x86_64-w64-mingw32-windres rsrc.rc -O coff -o ../rsrc_windows_amd64.syso
```

## Notes

This is an unofficial launcher and isn't affiliated with the Cinnabar project, Mojang or Microsoft. The icon in `winres/cinnabar.ico` comes from the Cinnabar repository and is used under the [PolyForm Noncommercial License 1.0.0](https://polyformproject.org/licenses/noncommercial/1.0.0).
