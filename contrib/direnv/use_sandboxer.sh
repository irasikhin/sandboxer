#!/usr/bin/env bash
# use_sandboxer — a direnv stdlib function that surfaces the active sandboxer
# sandbox into the shell whenever you cd into the project, and reloads direnv
# when the active sandbox changes.
#
# Install (once): copy this file's function into ~/.config/direnv/direnvrc, e.g.
#
#   mkdir -p ~/.config/direnv
#   cat contrib/direnv/use_sandboxer.sh >> ~/.config/direnv/direnvrc
#
# Then, in a project's .envrc:
#
#   use sandboxer
#
# This is read-only: `sandboxer hook direnv` only prints the already-persisted
# active sandbox — nothing is built or started on `cd`. It is a no-op outside a
# sandboxer project or when no sandbox is active, so the .envrc never errors.
use_sandboxer() {
	# eval the host-shell exports the hook prints (SANDBOXER_SLUG/SRC/…).
	# NOTE: the active-sandbox pointer lives in the XDG state dir OUTSIDE the
	# repo, so direnv cannot watch it — run `direnv reload` after
	# `sandboxer use <slug>` to refresh the exports.
	eval "$(sandboxer hook direnv)"
}
