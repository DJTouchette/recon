; C++ symbols.

(function_definition
  declarator: (function_declarator declarator: (identifier) @function)) @def
(function_definition
  declarator: (pointer_declarator
    declarator: (function_declarator declarator: (identifier) @function))) @def
(function_definition
  declarator: (function_declarator declarator: (field_identifier) @method)) @def
(function_definition
  declarator: (function_declarator
    declarator: (qualified_identifier name: (identifier) @method))) @def

; Declarations without a body. Headers are the primary place C++ declares its
; public API, and a header holds prototypes and in-class member declarations
; rather than definitions — without these patterns a header-only class
; contributes its name and nothing else.
(declaration
  declarator: (function_declarator declarator: (identifier) @function)) @def
(declaration
  declarator: (pointer_declarator
    declarator: (function_declarator declarator: (identifier) @function))) @def
(declaration
  declarator: (function_declarator
    declarator: (qualified_identifier name: (identifier) @method))) @def
(field_declaration
  declarator: (function_declarator declarator: (field_identifier) @method)) @def
(field_declaration
  declarator: (pointer_declarator
    declarator: (function_declarator declarator: (field_identifier) @method))) @def
; Plain member variables are deliberately not captured, to stay consistent with
; the other language queries (Go does not capture struct fields; C# captures
; properties but not fields).

(class_specifier name: (type_identifier) @class body: (_)) @def
(struct_specifier name: (type_identifier) @struct body: (_)) @def
(union_specifier name: (type_identifier) @struct body: (_)) @def
(enum_specifier name: (type_identifier) @enum) @def
(type_definition declarator: (type_identifier) @type) @def
(alias_declaration name: (type_identifier) @type) @def
(namespace_definition name: (namespace_identifier) @module) @def
