# upkeep

`upkeep` is a small CLI that runs configured package-update commands in workflows only when they're due.

Define the update commands you want to run and how often each workflow should run.
Use `upkeep` manually or invoke it from shell startup without running every update command on every terminal launch.

## Install & Update

Install or update `upkeep`:

```sh
curl -fsSL https://raw.githubusercontent.com/xkumiyu/upkeep/main/install.sh | sh
```

Existing configuration is preserved.

## Configure

Configure workflows with nested jobs in `~/.config/upkeep/config.toml`; see
[`config.example.toml`](config.example.toml) for the details.

## Use

```sh
upkeep run           # run due workflows; asks for approval
upkeep run --dry-run # show due workflows without running them
```

Run `upkeep --help` or `upkeep <command> --help` for other commands and options.

Commands run in the foreground. Standard output and standard error are shown
in the terminal and saved in the run log.

Concurrent invocations share a lock; a new invocation exits when another
`upkeep` process is already running.

## Recommended: run on shell startup

`upkeep` is designed to be invoked when an interactive shell starts.
It checks which workflows are due and only runs update commands when needed.

Add the following to your shell startup file (for example, `~/.bashrc`):

```sh
case $- in *i*) upkeep run ;; esac
```
