# A plain nixpkgs overlay — the standard final: prev: form, nothing
# sandboxer-specific. Expose computed packages (or files via writeTextDir) as
# attrs and list their names in image.packages.
final: prev: {
  greet = prev.writeShellScriptBin "greet" "echo hello from a custom toolbox";
}
