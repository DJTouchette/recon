; Go import specifiers. @path captures the module path without quotes by
; matching the inner content of the interpreted string literal.
(import_spec
  path: (interpreted_string_literal
          (interpreted_string_literal_content) @path))
