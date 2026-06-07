; Ruby require / require_relative. @_m carries the directive (resolved in Go),
; @path is the required string.
(call
  method: (identifier) @_m
  arguments: (argument_list (string (string_content) @path))
  (#match? @_m "^(require|require_relative)$"))
