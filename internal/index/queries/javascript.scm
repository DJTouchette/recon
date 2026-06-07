; JavaScript symbols.

(function_declaration name: (identifier) @function) @def
(generator_function_declaration name: (identifier) @function) @def

(class_declaration name: (identifier) @class) @def

(method_definition name: (property_identifier) @method) @def

(variable_declarator
  name: (identifier) @function
  value: [(arrow_function) (function_expression)]) @def

; top-level and exported value bindings. Arrow-function bindings also match the
; @function rule above and win the de-dup, so they stay functions.
(program (lexical_declaration
  (variable_declarator name: (identifier) @constant) @def))
(program (variable_declaration
  (variable_declarator name: (identifier) @constant) @def))
(program (export_statement (lexical_declaration
  (variable_declarator name: (identifier) @constant) @def)))
