; C symbols.

(function_definition
  declarator: (function_declarator declarator: (identifier) @function)) @def
(function_definition
  declarator: (pointer_declarator
    declarator: (function_declarator declarator: (identifier) @function))) @def

; Prototypes. A header is almost entirely prototypes, so without these a .h
; file contributes only its type definitions and its whole API is invisible.
(declaration
  declarator: (function_declarator declarator: (identifier) @function)) @def
(declaration
  declarator: (pointer_declarator
    declarator: (function_declarator declarator: (identifier) @function))) @def

(struct_specifier name: (type_identifier) @struct body: (_)) @def
(union_specifier name: (type_identifier) @struct body: (_)) @def
(enum_specifier name: (type_identifier) @enum body: (_)) @def
(type_definition declarator: (type_identifier) @type) @def
