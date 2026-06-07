; Zig symbols. Functions are clean declarations; struct/enum/union types are
; PascalCase const bindings (Zig has no dedicated type-declaration node).

(function_declaration name: (identifier) @function) @def

(variable_declaration
  (identifier) @type
  (#match? @type "^[A-Z]")) @def
