; Python symbols. Module-level defs are functions; defs inside a class are
; methods. Module-level UPPER_SNAKE assignments are constants.

(module (function_definition name: (identifier) @function) @def)
(module (decorated_definition (function_definition name: (identifier) @function) @def))

(class_definition name: (identifier) @class) @def
(decorated_definition (class_definition name: (identifier) @class) @def)

(class_definition body: (block
  (function_definition name: (identifier) @method) @def))
(class_definition body: (block
  (decorated_definition (function_definition name: (identifier) @method) @def)))

(module (expression_statement
  (assignment left: (identifier) @constant) @def)
  (#match? @constant "^[A-Z_][A-Z0-9_]*$"))
