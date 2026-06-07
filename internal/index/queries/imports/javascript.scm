; JavaScript import specifiers. @path captures the module string (no quotes).
; Covers static imports, re-exports (export … from), dynamic import(), require().

(import_statement source: (string (string_fragment) @path))
(export_statement source: (string (string_fragment) @path))

(call_expression
  function: (import)
  arguments: (arguments (string (string_fragment) @path)))

(call_expression
  function: (identifier) @_req
  arguments: (arguments (string (string_fragment) @path))
  (#eq? @_req "require"))
