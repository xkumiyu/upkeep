# upkeep

`upkeep` is a small CLI that runs configured package-update commands only when they're due, with separate intervals for each job.

Define the update commands you want to run and how often each should run.
Use `upkeep` manually or invoke it from shell startup without running every update command on every terminal launch.

## Requirements

- Go 1.26 or later

## Install & Update

Install or update `upkeep`:

```sh
curl -fsSL https://raw.githubusercontent.com/xkumiyu/upkeep/main/install.sh | sh
```

Existing configuration is preserved.

## Configure

Configure jobs in `~/.config/upkeep/config.toml`; see
[`config.example.toml`](config.example.toml) for the details.

## Use

```sh
upkeep run           # run due jobs; asks for approval
upkeep run --dry-run # show due jobs without running them
```

Run `upkeep --help` or `upkeep <command> --help` for other commands and options.

Commands run in the foreground. Standard output and standard error are shown
in the terminal and saved in the run log.

Concurrent invocations share a lock; a new invocation exits when another
`upkeep` process is already running.

## Recommended: run on shell startup

`upkeep` is designed to be invoked when an interactive shell starts.
It checks which jobs are due and only runs update commands when needed.

Add the following to your shell startup file (for example, `~/.bashrc`):

```sh
case $- in *i*) upkeep run ;; esac
```
