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

(class_specifier name: (type_identifier) @class body: (_)) @def
(struct_specifier name: (type_identifier) @struct body: (_)) @def
(union_specifier name: (type_identifier) @struct body: (_)) @def
(enum_specifier name: (type_identifier) @enum) @def
(type_definition declarator: (type_identifier) @type) @def
(namespace_definition name: (namespace_identifier) @module) @def
