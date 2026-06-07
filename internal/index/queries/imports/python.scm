; Python relative imports. @path captures the relative module (e.g. ".foo",
; "..pkg.mod"). Absolute imports are not resolved to local files, matching the
; existing resolver, so they are intentionally not captured.

(import_from_statement module_name: (relative_import) @path)
